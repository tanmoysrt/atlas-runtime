package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runtime is the orchestrator. It owns config, metadata, firecracker, network, and console.
type Runtime struct {
	machineDir  string
	runtimeDir  string
	configPath  string
	metaPath    string
	rootfsPath  string
	config      *Config
	meta        *Metadata
	firecracker *Firecracker
	network     *Network
	console     *Console
}

// NewRuntime creates a runtime for the VM described by the given config file.
func NewRuntime(configPath string, config *Config, nodeConfig *NodeConfig, beacon *BeaconClient) (*Runtime, error) {
	machineDir := filepath.Dir(configPath)
	instance := &Runtime{
		machineDir: machineDir,
		runtimeDir: filepath.Join(machineDir, "runtime"),
		configPath: configPath,
		metaPath:   filepath.Join(machineDir, "metadata.json"),
		rootfsPath: filepath.Join(machineDir, "rootfs"),
		config:     config,
	}

	os.MkdirAll(instance.runtimeDir, 0755)

	var err error
	instance.meta, err = LoadMetadata(instance.metaPath)
	if err != nil {
		instance.meta = &Metadata{
			InstanceID:   filepath.Base(machineDir),
			Hostname:     bootHostname(config.Boot.Hostname, machineDir),
			DesiredState: "stopped",
			PrivateIP:    config.Network.Address,
		}
		SaveMetadata(instance.metaPath, instance.meta)
	}

	instance.firecracker = NewFirecracker(instance.runtimeDir)
	instance.network = NewNetwork(instance.meta.InstanceID, config.Network, nodeConfig, beacon)
	instance.console = NewConsole(filepath.Join(machineDir, "console.log"))
	return instance, nil
}

// Run starts the HTTP API and recovers any VM that should be running.
func (instance *Runtime) Run(ctx context.Context, api *API) error {
	if instance.meta.DesiredState == "running" {
		_ = instance.Start()
	}
	return api.Serve(ctx, instance.config.Runtime.Listen)
}

// Start persists desired_state=running, then brings up network and VM.
func (instance *Runtime) Start() error {
	instance.meta.DesiredState = "running"
	SaveMetadata(instance.metaPath, instance.meta)

	if err := instance.network.Create(); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if err := instance.applyPublicIPs(); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	serialOutput, serialInput := instance.firecracker.SerialPaths()
	if instance.console.cancel == nil {
		_ = instance.firecracker.CreateSerialFIFOs()
		_ = instance.console.Attach(serialOutput, serialInput)
	}

	if !instance.meta.Initialized {
		if err := instance.initialize(); err != nil {
			_ = instance.console.Detach()
			return fmt.Errorf("init: %w", err)
		}
	}

	if !instance.firecracker.Running() {
		if err := instance.firecracker.Start(instance.config, instance.rootfsPath, instance.network.TapName, instance.meta.InstanceID, instance.network.NetnsName()); err != nil {
			_ = instance.console.Detach()
			return fmt.Errorf("firecracker: %w", err)
		}
	}

	_ = instance.firecracker.ConfigureMMDS(instance.mmdsData())
	return nil
}

func (instance *Runtime) initialize() error {
	if instance.config.BootSourceCount() != 1 {
		return fmt.Errorf("need exactly one boot source")
	}

	if instance.config.Boot.Snapshot != "" {
		snapshotDirectory := filepath.Join(instance.machineDir, "..", "..", "snapshots", instance.config.Boot.Snapshot)
		if err := ReflinkSnapshot(filepath.Join(snapshotDirectory, "rootfs"), instance.rootfsPath); err != nil {
			return fmt.Errorf("copy snap rootfs: %w", err)
		}
		if err := instance.firecracker.Restore(instance.config, instance.rootfsPath, instance.network.TapName, instance.meta.InstanceID, snapshotDirectory, instance.network.NetnsName()); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
	} else {
		imagePath, err := ResolveImage(instance.config.Boot.Image, filepath.Dir(instance.machineDir))
		if err != nil {
			return fmt.Errorf("resolve image: %w", err)
		}
		if err := PrepareRootfs(imagePath, instance.rootfsPath, instance.config.Rootfs.Size); err != nil {
			return fmt.Errorf("prepare rootfs: %w", err)
		}
		if err := SetupProjectQuota(instance.machineDir, instance.meta.InstanceID, instance.config.Rootfs.Size); err != nil {
			return fmt.Errorf("quota: %w", err)
		}
	}

	instance.meta.Initialized = true
	instance.meta.PrivateIP = instance.config.Network.Address
	return SaveMetadata(instance.metaPath, instance.meta)
}

// Stop persists desired_state=stopped, then tears everything down.
func (instance *Runtime) Stop() error {
	instance.meta.DesiredState = "stopped"
	SaveMetadata(instance.metaPath, instance.meta)
	_ = instance.console.Detach()
	_ = instance.firecracker.Stop()

	instance.meta.PublicIPv4 = ""
	instance.meta.PublicIPv6 = ""
	SaveMetadata(instance.metaPath, instance.meta)

	_ = instance.network.Delete()
	_ = RemoveProjectQuota(instance.machineDir, instance.meta.InstanceID)
	return nil
}

// Reboot stops Firecracker and starts it again.
func (instance *Runtime) Reboot() error {
	_ = instance.firecracker.Stop()
	_ = instance.console.Detach()
	return instance.Start()
}

// Reload re-reads config.toml and applies changes that don't need a reboot.
func (instance *Runtime) Reload() error {
	config, err := LoadConfig(instance.configPath)
	if err != nil {
		return fmt.Errorf("reload: %w", err)
	}
	if err := config.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return instance.applyConfig(config)
}

// ResizeRootfs grows the root disk and sets its I/O limits in config.toml.
func (instance *Runtime) ResizeRootfs(sizeBytes, bandwidth int64, iops int) error {
	config, err := LoadConfig(instance.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if sizeBytes > config.Rootfs.Size {
		running := instance.firecracker.Running()
		if err := GrowRootfs(instance.rootfsPath, sizeBytes, !running); err != nil {
			return err
		}
		if running {
			if err := instance.firecracker.SetRootfsSize(instance.rootfsPath); err != nil {
				return err
			}
		}
		if err := SetupProjectQuota(instance.machineDir, instance.meta.InstanceID, sizeBytes); err != nil {
			return err
		}
		config.Rootfs.Size = sizeBytes
	}
	config.Rootfs.Bandwidth = bandwidth
	config.Rootfs.IOPS = iops

	if err := SaveConfig(instance.configPath, config); err != nil {
		return err
	}
	return instance.applyConfig(config)
}

// Resize updates the VM's CPU and memory allocation in config.toml.
func (instance *Runtime) Resize(cpuCount int, memoryBytes int64) error {
	if instance.firecracker.Running() {
		return fmt.Errorf("vm must be stopped before resize")
	}

	config, err := LoadConfig(instance.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	config.Resources.CPUs = cpuCount
	config.Resources.Memory = memoryBytes

	if err := SaveConfig(instance.configPath, config); err != nil {
		return err
	}
	instance.config = config
	return nil
}

// SysRq sends a raw character to the VM's serial console.
func (instance *Runtime) SysRq(key string) error {
	return instance.firecracker.SysRq(key)
}

// Snapshot pauses the VM, saves state+memory+rootfs, and writes snapshot metadata.
func (instance *Runtime) Snapshot(snapshotID string) error {
	snapshotDirectory := filepath.Join(instance.machineDir, "..", "..", "snapshots", snapshotID)
	os.MkdirAll(snapshotDirectory, 0755)
	if err := instance.firecracker.Snapshot(snapshotDirectory, instance.rootfsPath); err != nil {
		return err
	}
	metadata := *instance.meta
	metadata.Initialized = true
	metadata.DesiredState = "stopped"
	return SaveMetadata(filepath.Join(snapshotDirectory, "metadata.json"), &metadata)
}

func (instance *Runtime) applyConfig(config *Config) error {
	instance.config = config

	if instance.firecracker.Running() {
		_ = instance.firecracker.UpdateMMDS(instance.mmdsData())
		_ = instance.firecracker.SetRootfsLimits(config.Rootfs, instance.rootfsPath)
	}

	if err := instance.applyPublicIPs(); err != nil {
		return err
	}
	return instance.network.SetBandwidth(config.Network.IngressBandwidth, config.Network.EgressBandwidth)
}

func (instance *Runtime) applyPublicIPs() error {
	if err := instance.network.SyncPublicIPv4(instance.meta.PublicIPv4, instance.config.Network.PublicIPv4); err != nil {
		return fmt.Errorf("public ipv4: %w", err)
	}
	if err := instance.network.SyncPublicIPv6(instance.meta.PublicIPv6, instance.config.Network.PublicIPv6); err != nil {
		return fmt.Errorf("public ipv6: %w", err)
	}
	instance.meta.PublicIPv4 = instance.config.Network.PublicIPv4
	instance.meta.PublicIPv6 = instance.config.Network.PublicIPv6
	return SaveMetadata(instance.metaPath, instance.meta)
}

func (instance *Runtime) mmdsData() map[string]any {
	return map[string]any{
		"instance-id":    instance.meta.InstanceID,
		"local-hostname": instance.meta.Hostname,
		"private-ip":     instance.meta.PrivateIP,
		"ssh":            map[string]any{"authorized_keys": strings.Join(instance.config.SSH.AuthorizedKeys, "\n")},
		"cloud-init":     map[string]any{"user-data": instance.config.CloudInit.UserData},
	}
}

func bootHostname(configured, machineDir string) string {
	if configured != "" {
		return configured
	}
	return filepath.Base(machineDir)
}
