package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Firecracker wraps a single Firecracker process and its Unix-socket API.
type Firecracker struct {
	PID              int
	Socket           string
	serialInputPath  string
	serialOutputPath string
	command          *exec.Cmd
	client           *http.Client
}

// NewFirecracker creates a new Firecracker wrapper for the given runtime directory.
func NewFirecracker(directory string) *Firecracker {
	vm := &Firecracker{
		Socket:           filepath.Join(directory, "firecracker.sock"),
		serialInputPath:  filepath.Join(directory, "serial.in"),
		serialOutputPath: filepath.Join(directory, "serial.out"),
	}
	vm.client = &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", vm.Socket)
			},
		},
	}
	return vm
}

// request sends an HTTP request to Firecracker's Unix-socket API.
func (vm *Firecracker) request(method, path string, body any) error {
	var reader io.Reader
	if body != nil {
		bodyBytes, _ := json.Marshal(body)
		reader = bytes.NewReader(bodyBytes)
	}

	request, _ := http.NewRequest(method, "http://localhost"+path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := vm.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(response.Body)
		return fmt.Errorf("%s %s: %s", method, path, string(bodyBytes))
	}
	return nil
}

// Running reports whether the Firecracker process is still alive.
func (vm *Firecracker) Running() bool {
	return vm.PID != 0 && syscall.Kill(vm.PID, 0) == nil
}

// CreateSerialFIFOs creates the serial input and output FIFOs if they do not exist.
func (vm *Firecracker) CreateSerialFIFOs() error {
	for _, path := range []string{vm.serialInputPath, vm.serialOutputPath} {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := syscall.Mkfifo(path, 0644); err != nil {
			return fmt.Errorf("mkfifo %s: %w", path, err)
		}
	}
	return nil
}

// startProcess launches the firecracker binary with a Unix socket and
// FIFO-based serial. When netnsName is set, it launches inside that network
// namespace through nsenter, since Firecracker opens its TAP device by name
// and the TAP device lives there. nsenter replaces its own process image
// with Firecracker's, so vm.command.Process.Pid is still Firecracker's PID.
func (vm *Firecracker) startProcess(instanceID, netnsName string) error {
	if vm.Running() {
		return nil
	}

	os.Remove(vm.Socket)

	args := []string{"firecracker", "--api-sock", vm.Socket, "--id", instanceID}
	if netnsName != "" {
		args = append([]string{"--net=/var/run/netns/" + netnsName, "--"}, args...)
		vm.command = exec.Command("nsenter", args...)
	} else {
		vm.command = exec.Command(args[0], args[1:]...)
	}

	// The console reader must already have the FIFO open before we open it for writing.
	vm.command.Stdout, _ = os.OpenFile(vm.serialOutputPath, os.O_WRONLY, 0644)
	inputFile, _ := os.OpenFile(vm.serialInputPath, os.O_RDWR, 0644)
	vm.command.Stdin = inputFile

	if err := vm.command.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}
	vm.PID = vm.command.Process.Pid

	// Reap the child process in the background so it does not become a zombie.
	go vm.command.Wait()

	// Wait for the API socket to appear (max ~5 seconds).
	for attempt := 0; attempt < 250; attempt++ {
		if _, err := os.Stat(vm.Socket); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("firecracker socket did not appear")
}

// drivePayload builds the /drives/rootfs request body, with a token-bucket
// rate limiter for bandwidth and IOPS when the config sets either one.
func drivePayload(rootfs RootfsConfig, rootfsPath string) map[string]any {
	payload := map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   rootfsPath,
		"is_root_device": true,
		"is_read_only":   false,
	}
	if rootfs.Bandwidth <= 0 && rootfs.IOPS <= 0 {
		return payload
	}

	rateLimiter := map[string]any{}
	if rootfs.Bandwidth > 0 {
		rateLimiter["bandwidth"] = map[string]any{"size": rootfs.Bandwidth, "refill_time": 1000}
	}
	if rootfs.IOPS > 0 {
		rateLimiter["ops"] = map[string]any{"size": rootfs.IOPS, "refill_time": 1000}
	}
	payload["rate_limiter"] = rateLimiter
	return payload
}

// SetRootfsLimits updates the rootfs limiter on a running VM. Firecracker keeps
// the current limiter when a PATCH carries none, so clearing one needs a restart.
func (vm *Firecracker) SetRootfsLimits(rootfs RootfsConfig, rootfsPath string) error {
	payload := drivePayload(rootfs, rootfsPath)
	if payload["rate_limiter"] == nil {
		return nil
	}
	// A PATCH takes drive_id, path_on_host and rate_limiter only.
	delete(payload, "is_root_device")
	delete(payload, "is_read_only")
	return vm.request("PATCH", "/drives/rootfs", payload)
}

// SetRootfsSize makes Firecracker re-read the file length and notify the guest.
// Growing the file on the host alone never reaches the guest.
func (vm *Firecracker) SetRootfsSize(rootfsPath string) error {
	return vm.request("PATCH", "/drives/rootfs", map[string]any{
		"drive_id":     "rootfs",
		"path_on_host": rootfsPath,
	})
}

// Start configures and boots a fresh VM.
func (vm *Firecracker) Start(config *Config, rootfsPath, tapDevice, instanceID, netnsName string) error {
	if err := vm.startProcess(instanceID, netnsName); err != nil {
		return err
	}

	kernelPath := strings.TrimPrefix(config.Boot.Kernel, "file://")
	if err := vm.request("PUT", "/boot-source", map[string]any{
		"kernel_image_path": kernelPath,
		"boot_args":         config.Boot.Cmdline,
	}); err != nil {
		return fmt.Errorf("boot source: %w", err)
	}

	if err := vm.request("PUT", "/drives/rootfs", drivePayload(config.Rootfs, rootfsPath)); err != nil {
		return fmt.Errorf("drive: %w", err)
	}

	if err := vm.request("PUT", "/network-interfaces/eth0", map[string]any{
		"iface_id":      "eth0",
		"guest_mac":     config.Network.MAC,
		"host_dev_name": tapDevice,
	}); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	if err := vm.request("PUT", "/machine-config", map[string]any{
		"vcpu_count":   config.Resources.CPUs,
		"mem_size_mib": config.Resources.Memory / (1 << 20),
	}); err != nil {
		return fmt.Errorf("machine config: %w", err)
	}

	// MMDS must be configured before the VM starts.
	if err := vm.request("PUT", "/mmds/config", map[string]any{
		"version":            "V2",
		"network_interfaces": []string{"eth0"},
	}); err != nil {
		return fmt.Errorf("mmds config: %w", err)
	}

	if err := vm.request("PUT", "/actions", map[string]any{"action_type": "InstanceStart"}); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// Restore brings a VM back from a snapshot instead of a clean boot.
func (vm *Firecracker) Restore(config *Config, _, tapDevice, instanceID, snapshotDirectory, netnsName string) error {
	if err := vm.startProcess(instanceID, netnsName); err != nil {
		return err
	}

	if err := vm.request("PUT", "/network-interfaces/eth0", map[string]any{
		"iface_id":      "eth0",
		"guest_mac":     config.Network.MAC,
		"host_dev_name": tapDevice,
	}); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	if err := vm.request("PUT", "/snapshot/load", map[string]any{
		"snapshot_path": filepath.Join(snapshotDirectory, "state"),
		"mem_file_path": filepath.Join(snapshotDirectory, "memory"),
	}); err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}

	if err := vm.request("PUT", "/actions", map[string]any{"action_type": "InstanceStart"}); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// Stop tries graceful shutdown first, then forces kill.
func (vm *Firecracker) Stop() error {
	if !vm.Running() {
		return nil
	}

	_ = vm.request("PUT", "/actions", map[string]any{"action_type": "SendCtrlAltDel"})
	time.Sleep(500 * time.Millisecond)

	if vm.Running() {
		_ = vm.request("PUT", "/actions", map[string]any{"action_type": "InstanceHalt"})
		time.Sleep(200 * time.Millisecond)
	}

	if vm.Running() {
		syscall.Kill(vm.PID, syscall.SIGKILL)
	}
	vm.PID = 0
	return nil
}

// SysRq writes a raw character to the serial input FIFO.
func (vm *Firecracker) SysRq(key string) error {
	inputFile, err := os.OpenFile(vm.serialInputPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer inputFile.Close()
	_, err = inputFile.Write([]byte(key))
	return err
}

// Snapshot asks Firecracker to dump state+memory and copies the rootfs.
// Firecracker pauses and resumes the VM automatically during snapshot creation.
func (vm *Firecracker) Snapshot(directory, rootfsPath string) error {
	if err := vm.request("PUT", "/snapshot/create", map[string]any{
		"snapshot_path": filepath.Join(directory, "state"),
		"mem_file_path": filepath.Join(directory, "memory"),
		"snapshot_type": "Full",
	}); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	if err := ReflinkSnapshot(rootfsPath, filepath.Join(directory, "rootfs")); err != nil {
		return fmt.Errorf("copy rootfs: %w", err)
	}
	return nil
}

// ConfigureMMDS writes the metadata payload served to the guest.
// The MMDS config (network binding and version) is set in Start before boot.
func (vm *Firecracker) ConfigureMMDS(data map[string]any) error {
	return vm.request("PUT", "/mmds", data)
}

// UpdateMMDS patches existing MMDS data with new values.
func (vm *Firecracker) UpdateMMDS(data map[string]any) error {
	return vm.request("PATCH", "/mmds", data)
}

// SerialPaths returns the paths to the serial output and input FIFOs.
func (vm *Firecracker) SerialPaths() (string, string) {
	return vm.serialOutputPath, vm.serialInputPath
}
