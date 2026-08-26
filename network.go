package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// Fixed host-side addresses on every TAP. They live inside each VPC's
// namespace, so all TAPs can share them.
const (
	tapHostAddress4 = "169.254.1.1"
	tapHostAddress6 = "fd00::1"
)

// Network manages the TAP device, routing, NAT, and bandwidth for one VM.
type Network struct {
	instanceID      string
	config          NetworkConfig
	firewall        FirewallConfig
	TapName         string
	namespace       string
	nodeConfig      *NodeConfig
	beacon          *BeaconClient
	membersPath     string
	members         map[string]vpcMember
	membersRevision int64
	watchStop       context.CancelFunc
	watchDone       chan struct{}
}

func NewNetwork(instanceID string, config NetworkConfig, firewall FirewallConfig, nodeConfig *NodeConfig, beacon *BeaconClient, membersPath string) *Network {
	return &Network{
		instanceID:  instanceID,
		config:      config,
		firewall:    firewall,
		nodeConfig:  nodeConfig,
		beacon:      beacon,
		membersPath: membersPath,
		members:     map[string]vpcMember{},
	}
}

func (network *Network) NetnsName() string {
	return network.namespace
}

// Create sets up the VM's network inside its VPC's namespace.
func (network *Network) Create() error {
	namespace, err := vpcEnter(network.config.VPC, network.instanceID)
	if err != nil {
		return fmt.Errorf("vpc network: %w", err)
	}
	network.namespace = namespace

	if network.nodeConfig != nil && network.beacon != nil {
		network.watchVPCMembers()
	}

	// Create the TAP, assign host-side addresses, and route the guest to it.
	network.TapName = "atap-" + network.instanceID
	tap := network.TapName
	runIP(network.namespace, []string{
		"tuntap add " + tap + " mode tap",
		"addr add " + tapHostAddress4 + "/32 dev " + tap,
		"addr add " + tapHostAddress6 + "/128 dev " + tap,
		"link set " + tap + " up",
		"route add " + network.guestAddress() + " dev " + tap,
		"route add " + network.guestAddress6IP() + " dev " + tap,
	})

	network.setupNftables()
	return network.SetBandwidth(network.config.IngressBandwidth, network.config.EgressBandwidth)
}

// Delete tears down the VM's network.
func (network *Network) Delete() error {
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "root").Run()
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "ingress").Run()
	network.ip("link", "set", network.TapName, "down").Run()
	network.ip("tuntap", "del", network.TapName, "mode", "tap").Run()
	network.deleteNftables()

	if network.watchStop != nil {
		network.watchStop()
		<-network.watchDone
		network.watchStop = nil
		network.watchDone = nil
	}

	// Withdraw this VM's address after the watcher stops, so that it cannot
	// publish the address again.
	if network.beacon != nil && network.nodeConfig != nil {
		_ = network.beacon.Delete(vpcMemberKey(network.config.VPC, network.instanceID))
	}
	return vpcLeave(network.config.VPC, network.instanceID)
}

// SetBandwidth applies tc rate-limiting on the TAP device.
// Egress uses TBF. Ingress uses an ingress qdisc with a police filter.
func (network *Network) SetBandwidth(ingressBandwidth, egressBandwidth int64) error {
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "root").Run()
	network.exec("tc", "qdisc", "del", "dev", network.TapName, "ingress").Run()

	if egressBandwidth > 0 {
		if err := network.exec("tc", "qdisc", "add", "dev", network.TapName, "root", "tbf",
			"rate", fmt.Sprintf("%dbps", egressBandwidth),
			"burst", "32kbit",
			"latency", "50ms",
		).Run(); err != nil {
			return fmt.Errorf("tc egress: %w", err)
		}
	}

	if ingressBandwidth > 0 {
		if err := network.exec("tc", "qdisc", "add", "dev", network.TapName, "ingress").Run(); err != nil {
			return fmt.Errorf("tc ingress qdisc: %w", err)
		}
		if err := network.exec("tc", "filter", "add", "dev", network.TapName, "parent", "ffff:", "protocol", "ip",
			"u32", "match", "u32", "0", "0",
			"police", "rate", fmt.Sprintf("%dbps", ingressBandwidth),
			"burst", "32kbit",
			"drop",
		).Run(); err != nil {
			return fmt.Errorf("tc ingress filter: %w", err)
		}
	}
	return nil
}

// NAT and public IP routing

// SyncPublicIPv4 moves the 1:1 NAT mapping from oldIP to newIP.
// An empty newIP clears the mapping.
func (network *Network) SyncPublicIPv4(oldIP, newIP string) error {
	network.syncNATMap(oldIP, newIP, network.guestAddress(), "dnat_map", "snat_map")
	network.syncPublicRoute(oldIP, newIP, "-4", "/32")
	return nil
}

// SyncPublicIPv6 is the IPv6 equivalent of SyncPublicIPv4.
func (network *Network) SyncPublicIPv6(oldIP, newIP string) error {
	network.syncNATMap(oldIP, newIP, network.guestAddress6IP(), "dnat6_map", "snat6_map")
	network.syncPublicRoute(oldIP, newIP, "-6", "/128")
	return nil
}

// setupNftables builds this VM's nftables table as one atomic script.
// "destroy" does not error when the table is missing, so the same script
// works on first Create and on a later Reboot.
//
// The forward chain clamps the TCP MSS to the route MTU. A GRE tunnel to
// another node holds 1472 bytes, not 1500, and the clamp must come before
// the accept rules, because an accept ends the chain.
//
// The firewall lives in the fw chain, which ends in a drop rule. Forward jumps
// to it only for this VM's own traffic, so packets of other VMs in the same
// namespace pass through without hitting the drop.
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
add chain inet %[1]s fw
add map inet %[1]s dnat_map { type ipv4_addr : ipv4_addr ; }
add map inet %[1]s dnat6_map { type ipv6_addr : ipv6_addr ; }
add map inet %[1]s snat_map { type ipv4_addr : ipv4_addr ; }
add map inet %[1]s snat6_map { type ipv6_addr : ipv6_addr ; }
add rule inet %[1]s prerouting dnat to ip daddr map @dnat_map
add rule inet %[1]s prerouting dnat to ip6 daddr map @dnat6_map
add rule inet %[1]s postrouting snat to ip saddr map @snat_map
add rule inet %[1]s postrouting snat to ip6 saddr map @snat6_map
%[2]s
add rule inet %[1]s forward tcp flags syn tcp option maxseg size set rt mtu
add rule inet %[1]s forward ct state established,related accept
add rule inet %[1]s forward iif %[3]q jump fw
add rule inet %[1]s forward oif %[3]q jump fw
%[4]s
`, table, network.masqueradeRules(table, uplink), network.TapName, network.fwRules())

	runScript(network.exec("nft", "-f", "-"), script)
}

// SetFirewall replaces the VM's firewall rules. When the network exists, the
// fw chain is rebuilt atomically. Otherwise the rules apply at Create.
func (network *Network) SetFirewall(firewall FirewallConfig) error {
	network.firewall = firewall
	if network.TapName == "" {
		return nil
	}

	script := fmt.Sprintf("flush chain inet %s fw\n%s", network.table(), network.fwRules())
	command := network.exec("nft", "-f", "-")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("nft firewall: %w: %s", err, output)
	}
	return nil
}

// dnsRules renders the always-allowed DNS lookups to the configured
// nameservers. This rule is not user-facing and cannot be removed.
func (network *Network) dnsRules() string {
	var builder strings.Builder
	for _, nameserver := range network.config.Nameservers {
		address := strings.TrimSpace(nameserver)
		if address == "" {
			continue
		}
		fmt.Fprintf(&builder, "add rule inet %s fw iif %q udp dport 53 %s accept\n",
			network.table(), network.TapName, addrMatch(address, "daddr"))
	}
	return builder.String()
}

// fwRules renders every user firewall rule as full nft statements in the fw
// chain, preceded by the internal DNS rules and followed by the drop that
// gives the chain its deny-by-default policy. nft regular chains cannot set a
// policy, so the drop is an explicit rule.
func (network *Network) fwRules() string {
	var builder strings.Builder
	builder.WriteString(network.dnsRules())
	for _, rule := range network.firewall.Ingress {
		for _, line := range network.ruleLines("oif", rule) {
			builder.WriteString(line)
		}
	}
	for _, rule := range network.firewall.Egress {
		for _, line := range network.ruleLines("iif", rule) {
			builder.WriteString(line)
		}
	}
	fmt.Fprintf(&builder, "add rule inet %s fw drop\n", network.table())
	return builder.String()
}

// ruleLines renders one rule as one or more nft statements. icmp produces two
// statements, one for each address family.
func (network *Network) ruleLines(direction string, rule FirewallRule) []string {
	match := direction + " " + network.TapName
	if rule.Source != "" {
		match += " " + addrMatch(rule.Source, "saddr")
	}
	if rule.Destination != "" {
		match += " " + addrMatch(rule.Destination, "daddr")
	}

	switch rule.Protocol {
	case "tcp", "udp":
		port := strconv.Itoa(rule.Port)
		if rule.PortEnd != 0 && rule.PortEnd != rule.Port {
			port = strconv.Itoa(rule.Port) + "-" + strconv.Itoa(rule.PortEnd)
		}
		return []string{fmt.Sprintf("add rule inet %s fw %s %s dport %s accept\n", network.table(), match, rule.Protocol, port)}
	case "icmp":
		return []string{
			fmt.Sprintf("add rule inet %s fw %s icmp type echo-request accept\n", network.table(), match),
			fmt.Sprintf("add rule inet %s fw %s icmpv6 type echo-request accept\n", network.table(), match),
		}
	case "all":
		return []string{fmt.Sprintf("add rule inet %s fw %s accept\n", network.table(), match)}
	}
	return nil
}

// addrMatch renders an nft address match for an IP or CIDR, with the correct
// address family for the inet table.
func addrMatch(address, field string) string {
	return addrFamily(address) + " " + field + " " + address
}

// addrFamily reports whether an address is IPv4 or IPv6. It accepts a bare IP
// or a CIDR. Anything unknown is treated as IPv6, which is the safe default
// only after Validate has already accepted the address.
func addrFamily(address string) string {
	if ip, _, err := net.ParseCIDR(address); err == nil {
		if ip.To4() != nil {
			return "ip"
		}
		return "ip6"
	}
	if ip := net.ParseIP(address); ip != nil && ip.To4() != nil {
		return "ip"
	}
	return "ip6"
}

// masqueradeRules renders the NAT to the uplink veth address, for a VM with
// no public address. IPv4 follows network.egress_v4, IPv6 network.egress_v6.
// IPv6 also needs a global IPv6 on the host.
//
// Every VM of a VPC shares the namespace, so each rule matches the address of
// its own guest. Without that match it would also carry the other VMs.
func (network *Network) masqueradeRules(table, uplink string) string {
	var builder strings.Builder
	if network.config.EgressV4 == "host" {
		fmt.Fprintf(&builder, "add rule inet %s postrouting oif %q ip saddr %s masquerade\n",
			table, uplink, network.guestAddress())
	}
	if network.config.EgressV6 == "host" && hasHostGlobalIPv6() {
		fmt.Fprintf(&builder, "add rule inet %s postrouting oif %q ip6 saddr %s masquerade\n",
			table, uplink, network.guestAddress6IP())
	}
	return strings.TrimRight(builder.String(), "\n")
}

// hasHostGlobalIPv6 reports whether the host has an IPv6 address that the
// internet can reach. A unique local address does not count.
func hasHostGlobalIPv6() bool {
	output, err := exec.Command("ip", "-6", "addr", "show", "scope", "global").Output()
	if err != nil {
		return false
	}
	for _, field := range strings.Fields(string(output)) {
		address, _, err := net.ParseCIDR(field)
		if err != nil || address.To4() != nil {
			continue
		}
		if address.IsGlobalUnicast() && !uniqueLocalIPv6(address) {
			return true
		}
	}
	return false
}

// uniqueLocalIPv6 reports whether an address is in fc00::/7.
func uniqueLocalIPv6(address net.IP) bool {
	address = address.To16()
	return address != nil && address[0]&0xfe == 0xfc
}

func (network *Network) deleteNftables() {
	network.exec("nft", "delete", "table", "inet", network.table()).Run()
}

func (network *Network) table() string {
	return "atlas-" + network.instanceID
}

func (network *Network) syncNATMap(oldIP, newIP, guestIP, dnatMap, snatMap string) {
	if oldIP != "" && oldIP != newIP {
		network.exec("nft", "delete", "element", "inet", network.table(), dnatMap, "{"+oldIP+"}").Run()
		network.exec("nft", "delete", "element", "inet", network.table(), snatMap, "{"+guestIP+"}").Run()
	}
	if newIP != "" {
		network.exec("nft", "add", "element", "inet", network.table(), dnatMap, "{"+newIP+" : "+guestIP+"}").Run()
		network.exec("nft", "add", "element", "inet", network.table(), snatMap, "{"+guestIP+" : "+newIP+"}").Run()
	}
}

// syncPublicRoute keeps the root-level route in sync. The route points via
// the VPC's ns-side veth because the public IP is not on any local link.
func (network *Network) syncPublicRoute(oldIP, newIP, family, mask string) {
	_, nsAddr := vpcVethAddress4(network.config.VPC)
	if family == "-6" {
		_, nsAddr = vpcVethAddress6(network.config.VPC)
	}
	via, _, _ := net.ParseCIDR(nsAddr)
	rootVeth, _ := vpcVethNames(network.config.VPC)

	if oldIP != "" && oldIP != newIP {
		exec.Command("ip", family, "route", "del", oldIP+mask, "via", via.String(), "dev", rootVeth).Run()
	}
	if newIP != "" {
		exec.Command("ip", family, "route", "add", newIP+mask, "via", via.String(), "dev", rootVeth).Run()
	}
}

func (network *Network) guestAddress() string {
	return guestIP(network.config.Address).String()
}

func (network *Network) guestAddress6IP() string {
	return guestIPv6(network.config.Address)
}

// uplink returns the default-route interface inside the VPC namespace.
func (network *Network) uplink() string {
	output, _ := network.ip("route", "show", "default").Output()
	fields := strings.Fields(string(output))
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dev" {
			return fields[i+1]
		}
	}
	return ""
}

// ip runs an `ip` subcommand inside this VM's VPC namespace.
func (network *Network) ip(args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"-n", network.namespace}, args...)...)
}

// exec runs an arbitrary command inside this VM's VPC namespace.
func (network *Network) exec(binary string, args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"netns", "exec", network.namespace, binary}, args...)...)
}
