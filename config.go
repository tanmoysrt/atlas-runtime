package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the user-facing configuration for a single VM.
type Config struct {
	Runtime   RuntimeConfig   `toml:"runtime" json:"runtime"`
	Resources ResourcesConfig `toml:"resources" json:"resources"`
	Boot      BootConfig      `toml:"boot" json:"boot"`
	Network   NetworkConfig   `toml:"network" json:"network"`
	Rootfs    RootfsConfig    `toml:"rootfs" json:"rootfs"`
	SSH       SSHConfig       `toml:"ssh" json:"ssh"`
	CloudInit CloudInitConfig `toml:"cloud_init" json:"cloud_init"`
	Firewall  FirewallConfig  `toml:"firewall" json:"firewall"`
}

// RuntimeConfig controls the atlas-runtime HTTP server.
type RuntimeConfig struct {
	Listen string `toml:"listen" json:"listen"`
}

// ResourcesConfig defines CPU and memory limits.
type ResourcesConfig struct {
	CPUs   int   `toml:"cpus" json:"cpus"`
	Memory int64 `toml:"memory" json:"memory"`
}

// BootConfig defines the kernel and rootfs source. Only used on first boot.
type BootConfig struct {
	Image    string `toml:"image" json:"image"`
	Snapshot string `toml:"snapshot" json:"snapshot"`
	Kernel   string `toml:"kernel" json:"kernel"`
	Cmdline  string `toml:"cmdline" json:"cmdline"`
	Hostname string `toml:"hostname" json:"hostname"`
}

// NetworkConfig defines the single NIC for this VM.
type NetworkConfig struct {
	VPC              int      `toml:"vpc" json:"vpc"`
	Address          string   `toml:"address" json:"address"`
	Egress           string   `toml:"egress" json:"egress"`
	IngressBandwidth int64    `toml:"ingress_bandwidth" json:"ingress_bandwidth"`
	EgressBandwidth  int64    `toml:"egress_bandwidth" json:"egress_bandwidth"`
	Nameservers      []string `toml:"nameservers" json:"nameservers"`
	PublicIPv4       string   `toml:"public_ipv4" json:"public_ipv4"`
	PublicIPv6       string   `toml:"public_ipv6" json:"public_ipv6"`
}

// SSHConfig holds authorized keys injected via MMDS.
type SSHConfig struct {
	AuthorizedKeys []string `toml:"authorized_keys" json:"authorized_keys"`
}

// RootfsConfig defines the size and I/O rate limits of the VM's root disk.
type RootfsConfig struct {
	Size      int64 `toml:"size" json:"size"`
	Bandwidth int64 `toml:"bandwidth" json:"bandwidth"` // bytes/sec, 0 = unlimited
	IOPS      int   `toml:"iops" json:"iops"`           // ops/sec, 0 = unlimited
}

// CloudInitConfig holds cloud-init user-data passed through MMDS.
type CloudInitConfig struct {
	UserData string `toml:"user_data" json:"user_data"`
}

// FirewallConfig lists the rules that allow traffic in and out of the VM.
// Both directions are deny-by-default. Atlas always allows DNS to the
// configured nameservers, regardless of these rules.
type FirewallConfig struct {
	Ingress []FirewallRule `toml:"ingress" json:"ingress"`
	Egress  []FirewallRule `toml:"egress" json:"egress"`
}

// FirewallRule allows one protocol between the VM and an optional peer.
// An empty source or destination means anywhere.
type FirewallRule struct {
	Protocol    string `toml:"protocol" json:"protocol"`       // tcp, udp, icmp, or all
	Port        int    `toml:"port" json:"port"`               // single port or range start
	PortEnd     int    `toml:"port_end" json:"port_end"`       // range end; 0 or equal means one port
	Source      string `toml:"source" json:"source"`           // optional IP or CIDR
	Destination string `toml:"destination" json:"destination"` // optional IP or CIDR
}

// LoadConfig parses a TOML file into a Config struct.
func LoadConfig(path string) (*Config, error) {
	var config Config
	if _, err := toml.DecodeFile(path, &config); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return &config, nil
}

// SaveConfig writes config.toml atomically: temp file -> fsync -> rename.
func SaveConfig(path string, config *Config) error {
	tempFile, err := os.CreateTemp(filepath.Dir(path), "config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())

	// os.CreateTemp makes a 0600 file, so restore the mode being replaced.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := tempFile.Chmod(mode); err != nil {
		tempFile.Close()
		return err
	}
	if err := toml.NewEncoder(tempFile).Encode(config); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return os.Rename(tempFile.Name(), path)
}

// Validate checks that all required fields are present and well-formed.
func (config *Config) Validate() error {
	if err := config.Resources.validate(); err != nil {
		return err
	}
	if config.Rootfs.Size <= 0 {
		return fmt.Errorf("rootfs.size must be > 0")
	}
	if err := config.Network.validate(); err != nil {
		return err
	}
	if err := config.Firewall.validate(); err != nil {
		return err
	}
	if _, err := net.ResolveTCPAddr("tcp", config.Runtime.Listen); err != nil {
		return fmt.Errorf("invalid listen: %w", err)
	}
	return nil
}

func (resources ResourcesConfig) validate() error {
	if resources.CPUs <= 0 {
		return fmt.Errorf("cpus must be > 0")
	}
	if resources.Memory <= 0 {
		return fmt.Errorf("memory must be > 0")
	}
	return nil
}

func (network NetworkConfig) validate() error {
	if network.VPC <= 0 || network.VPC > 16384 {
		return fmt.Errorf("vpc must be between 1 and 16384")
	}
	if guestIP(network.Address) == nil {
		return fmt.Errorf("address must be one IPv4 address: %s", network.Address)
	}
	return network.validatePublicIPs()
}

func (firewall FirewallConfig) validate() error {
	for _, rule := range firewall.Ingress {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("firewall ingress: %w", err)
		}
	}
	for _, rule := range firewall.Egress {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("firewall egress: %w", err)
		}
	}
	return nil
}

// nonNil returns the firewall with nil slices replaced by empty slices, so it
// encodes as empty lists rather than null.
func (firewall FirewallConfig) nonNil() FirewallConfig {
	if firewall.Ingress == nil {
		firewall.Ingress = []FirewallRule{}
	}
	if firewall.Egress == nil {
		firewall.Egress = []FirewallRule{}
	}
	return firewall
}

func (rule FirewallRule) validate() error {
	switch rule.Protocol {
	case "tcp", "udp":
		if rule.Port < 1 || rule.Port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
		if rule.PortEnd != 0 && (rule.PortEnd < rule.Port || rule.PortEnd > 65535) {
			return fmt.Errorf("port_end must be between port and 65535")
		}
	case "icmp", "all":
		if rule.Port != 0 || rule.PortEnd != 0 {
			return fmt.Errorf("port is not used with protocol %s", rule.Protocol)
		}
	default:
		return fmt.Errorf("protocol must be tcp, udp, icmp, or all")
	}
	if rule.Source != "" && !validAddress(rule.Source) {
		return fmt.Errorf("invalid source: %s", rule.Source)
	}
	if rule.Destination != "" && !validAddress(rule.Destination) {
		return fmt.Errorf("invalid destination: %s", rule.Destination)
	}
	return nil
}

// validAddress reports whether an address is a bare IP or a CIDR.
func validAddress(address string) bool {
	if net.ParseIP(address) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(address)
	return err == nil
}

func (network NetworkConfig) validatePublicIPs() error {
	if network.PublicIPv4 != "" && net.ParseIP(network.PublicIPv4).To4() == nil {
		return fmt.Errorf("invalid public_ipv4: %s", network.PublicIPv4)
	}
	if network.PublicIPv6 == "" {
		return nil
	}
	ip := net.ParseIP(network.PublicIPv6)
	if ip == nil || ip.To4() != nil {
		return fmt.Errorf("invalid public_ipv6: %s", network.PublicIPv6)
	}
	return nil
}

// BootSourceCount returns how many boot sources are configured.
// There must be exactly one on first creation.
func (config *Config) BootSourceCount() int {
	count := 0
	if config.Boot.Image != "" {
		count++
	}
	if config.Boot.Snapshot != "" {
		count++
	}
	return count
}
