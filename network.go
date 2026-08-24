package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// tapHostAddress4/6 are the fixed host-side addresses on every TAP. They
// live inside each VPC's namespace, so all TAPs can share them.
const (
	tapHostAddress4 = "169.254.1.1"
	tapHostAddress6 = "fd00::1"
)

// Network manages the TAP device, routing, NAT, and bandwidth for one VM.
// It runs inside the VM's VPC network namespace (see vpc.go).
type Network struct {
	instanceID string
	config     NetworkConfig
	TapName    string
	netns      string
}

// NewNetwork creates a network manager for the given VM instance.
func NewNetwork(instanceID string, config NetworkConfig) *Network {
	return &Network{instanceID: instanceID, config: config}
}

// NetnsName returns the VPC namespace this VM's TAP lives in.
func (network *Network) NetnsName() string {
	return network.netns
}

// Create sets up the VM's network inside its VPC's namespace.
func (network *Network) Create() error {
	netns, err := ensureVPCNetwork(network.config.VPC)
	if err != nil {
		return fmt.Errorf("vpc network: %w", err)
	}
	network.netns = netns

	network.TapName = "atap-" + network.instanceID
	network.ip("tuntap", "add", network.TapName, "mode", "tap").Run()
	network.ip("addr", "add", tapHostAddress4+"/32", "dev", network.TapName).Run()
	network.ip("-6", "addr", "add", tapHostAddress6+"/128", "dev", network.TapName).Run()
	network.ip("link", "set", network.TapName, "up").Run()
	// A route with no gateway needs its device up first.
	network.ip("route", "add", network.guestAddress()+"/32", "dev", network.TapName).Run()
	network.ip("-6", "route", "add", network.guestAddress6IP()+"/128", "dev", network.TapName).Run()

	network.setupNftables()
	return network.SetBandwidth(network.config.IngressBandwidth, network.config.EgressBandwidth)
}

// Delete tears down everything Create set up for this VM. It never touches
// the VPC's namespace/veth itself: other VMs of the same VPC may still be using it.
func (network *Network) Delete() error {
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "root").Run()
	network.ip("link", "set", network.TapName, "down").Run()
	network.ip("tuntap", "del", network.TapName, "mode", "tap").Run()
	network.deleteNftables()
	return nil
}

// SyncPublicIPv4 moves the 1:1 NAT mapping for this VM's private address
// from oldIP to newIP. An empty newIP clears the mapping.
func (network *Network) SyncPublicIPv4(oldIP, newIP string) error {
	network.syncPublicIP(oldIP, newIP, network.guestAddress(), "dnat_map", "snat_map")
	_, nsAddr := vpcVethAddress4(network.config.VPC)
	via, _, _ := net.ParseCIDR(nsAddr)
	network.syncPublicRoute(oldIP, newIP, "32", nil, via.String())
	return nil
}

// SyncPublicIPv6 is the IPv6 equivalent of SyncPublicIPv4.
func (network *Network) SyncPublicIPv6(oldIP, newIP string) error {
	network.syncPublicIP(oldIP, newIP, network.guestAddress6IP(), "dnat6_map", "snat6_map")
	_, nsAddr := vpcVethAddress6(network.config.VPC)
	via, _, _ := net.ParseCIDR(nsAddr)
	network.syncPublicRoute(oldIP, newIP, "128", []string{"-6"}, via.String())
	return nil
}

// syncPublicIP adds or removes the DNAT/SNAT elements for this VM's VPC.
func (network *Network) syncPublicIP(oldIP, newIP, guestIP, dnatMap, snatMap string) {
	if oldIP != "" && oldIP != newIP {
		network.exec("nft", "delete", "element", "inet", network.table(), dnatMap, "{"+oldIP+"}").Run()
		network.exec("nft", "delete", "element", "inet", network.table(), snatMap, "{"+guestIP+"}").Run()
	}
	if newIP != "" {
		network.exec("nft", "add", "element", "inet", network.table(), dnatMap, "{"+newIP+" : "+guestIP+"}").Run()
		network.exec("nft", "add", "element", "inet", network.table(), snatMap, "{"+guestIP+" : "+newIP+"}").Run()
	}
}

// syncPublicRoute keeps the root-level route to a public IP in sync with
// the mapping. It points via the VPC's ns-side veth address because the
// public IP is not on any link, so root cannot resolve it directly.
func (network *Network) syncPublicRoute(oldIP, newIP, mask string, family []string, via string) {
	rootVeth, _ := vpcVethNames(network.config.VPC)
	if oldIP != "" && oldIP != newIP {
		exec.Command("ip", append(append([]string{}, family...), "route", "del", oldIP+"/"+mask, "via", via, "dev", rootVeth)...).Run()
	}
	if newIP != "" {
		exec.Command("ip", append(append([]string{}, family...), "route", "add", newIP+"/"+mask, "via", via, "dev", rootVeth)...).Run()
	}
}

// SetBandwidth applies tc TBF rate-limiting on the TAP device.
func (network *Network) SetBandwidth(ingressBandwidth, egressBandwidth int64) error {
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "root").Run()
	if egressBandwidth > 0 {
		if err := network.exec("tc", "qdisc", "add", "dev", network.TapName, "root", "tbf",
			"rate", fmt.Sprintf("%dbps", egressBandwidth),
			"burst", "32kbit",
			"latency", "50ms",
		).Run(); err != nil {
			return fmt.Errorf("tc egress: %w", err)
		}
	}
	return nil
}

// setupNftables builds this VM's nftables table as one atomic script.
// "destroy" does not error when the table is missing, so the same script
// works on first Create and on a later Reboot. Egress=="host" masquerades
// to the VPC's veth; vpc.go masquerades a second time to the physical NIC.
func (network *Network) setupNftables() {
	uplink := network.uplink()
	if uplink == "" {
		return
	}

	network.exec("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	network.exec("sysctl", "-w", "net.ipv6.conf.all.forwarding=1").Run()

	table := network.table()
	script := fmt.Sprintf(`
destroy table inet %[1]s
add table inet %[1]s
add chain inet %[1]s prerouting { type nat hook prerouting priority dstnat ; }
add chain inet %[1]s postrouting { type nat hook postrouting priority srcnat ; }
add chain inet %[1]s forward { type filter hook forward priority 0 ; }
add map inet %[1]s dnat_map { type ipv4_addr : ipv4_addr ; }
add map inet %[1]s dnat6_map { type ipv6_addr : ipv6_addr ; }
add map inet %[1]s snat_map { type ipv4_addr : ipv4_addr ; }
add map inet %[1]s snat6_map { type ipv6_addr : ipv6_addr ; }
add rule inet %[1]s prerouting dnat to ip daddr map @dnat_map
add rule inet %[1]s prerouting dnat to ip6 daddr map @dnat6_map
add rule inet %[1]s postrouting snat to ip saddr map @snat_map
add rule inet %[1]s postrouting snat to ip6 saddr map @snat6_map
%[2]s
add rule inet %[1]s forward iif %[3]q oif %[4]q accept
add rule inet %[1]s forward iif %[4]q oif %[3]q ct state established,related accept
`, table, masqueradeRule(network.config.Egress, table, uplink), network.TapName, uplink)

	command := network.exec("nft", "-f", "-")
	command.Stdin = strings.NewReader(script)
	command.Run()
}

// masqueradeRule returns the egress=host MASQUERADE rule line, or "" if not configured.
func masqueradeRule(egress, table, uplink string) string {
	if egress != "host" {
		return ""
	}
	return fmt.Sprintf("add rule inet %s postrouting oif %q masquerade", table, uplink)
}

func (network *Network) deleteNftables() {
	network.exec("nft", "delete", "table", "inet", network.table()).Run()
}

func (network *Network) table() string {
	return "atlas-" + network.instanceID
}

func (network *Network) guestAddress() string {
	ip, _, _ := net.ParseCIDR(network.config.Address)
	return ip.String()
}

func (network *Network) guestAddress6IP() string {
	ip4 := mustIPv4(network.config.Address)
	return fmt.Sprintf("fd00::%02x%02x:%02x%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

func mustIPv4(cidr string) net.IP {
	ip, _, _ := net.ParseCIDR(cidr)
	return ip.To4()
}

// uplink returns the default-route interface inside this VM's VPC
// namespace: the veth set up by ensureVPCNetwork (see vpc.go).
func (network *Network) uplink() string {
	output, _ := network.ip("route", "show", "default").Output()
	fields := strings.Fields(string(output))
	for index := 0; index < len(fields)-1; index++ {
		if fields[index] == "dev" {
			return fields[index+1]
		}
	}
	return ""
}

// ip runs an `ip` subcommand inside this VM's VPC namespace.
func (network *Network) ip(args ...string) *exec.Cmd {
	return nsIPCommand(network.netns, args...)
}

// exec runs an arbitrary command inside this VM's VPC namespace.
func (network *Network) exec(binary string, args ...string) *exec.Cmd {
	return nsExecCommand(network.netns, binary, args...)
}
