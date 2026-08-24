package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// vpcNetnsName returns the network namespace for a VPC.
// Every VM in the same VPC shares this namespace.
func vpcNetnsName(vpcID int) string {
	return fmt.Sprintf("atlas-vpc-%d", vpcID)
}

// vpcVethNames returns the veth pair that connects a VPC's namespace to
// root. Names stay short: IFNAMSIZ allows at most 15 characters.
func vpcVethNames(vpcID int) (rootSide, nsSide string) {
	return fmt.Sprintf("vp%dr", vpcID), fmt.Sprintf("vp%dn", vpcID)
}

// vpcVethAddress4 returns the /31 IPv4 pair for a VPC's veth. Each VPC gets
// a different pair, so root can tell VPCs apart by this address.
func vpcVethAddress4(vpcID int) (rootAddr, nsAddr string) {
	base := (vpcID * 2) & 0xFFFF
	root := net.IPv4(169, 254, byte(base>>8), byte(base))
	ns := net.IPv4(169, 254, byte((base+1)>>8), byte(base+1))
	return root.String() + "/31", ns.String() + "/31"
}

// vpcVethAddress6 is the IPv6 (ULA) equivalent of vpcVethAddress4.
func vpcVethAddress6(vpcID int) (rootAddr, nsAddr string) {
	base := (vpcID * 2) & 0xFFFF
	return fmt.Sprintf("fd01::%x/127", base), fmt.Sprintf("fd01::%x/127", base+1)
}

// ensureVPCNetwork creates the VPC's namespace and its veth link to root,
// and adds the VPC to the shared uplink table. It is safe to call every
// time a VM starts: each step only runs if it has not run before.
//
// A file lock guards the whole function. Several VMs in the same VPC can
// start at the same time, for example many systemd units at boot.
func ensureVPCNetwork(vpcID int) (string, error) {
	netns := vpcNetnsName(vpcID)

	lockFile, err := os.OpenFile("/run/atlas-network.lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("network lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	exec.Command("ip", "netns", "add", netns).Run()
	exec.Command("ip", "-n", netns, "link", "set", "lo", "up").Run()

	rootVeth, nsVeth := vpcVethNames(vpcID)
	if exec.Command("ip", "link", "show", rootVeth).Run() != nil {
		createVeth(vpcID, netns, rootVeth, nsVeth)
	}

	exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	exec.Command("sysctl", "-w", "net.ipv6.conf.all.forwarding=1").Run()
	ensureUplinkTable(rootVeth)

	return netns, nil
}

// createVeth adds the veth pair for a VPC, moves one end into the VPC's
// namespace, and sets a default route on each end toward the other.
func createVeth(vpcID int, netns, rootVeth, nsVeth string) {
	rootAddr4, nsAddr4 := vpcVethAddress4(vpcID)
	rootAddr6, nsAddr6 := vpcVethAddress6(vpcID)
	rootIP4, _, _ := net.ParseCIDR(rootAddr4)
	rootIP6, _, _ := net.ParseCIDR(rootAddr6)

	exec.Command("ip", "link", "add", rootVeth, "type", "veth", "peer", "name", nsVeth).Run()
	exec.Command("ip", "link", "set", nsVeth, "netns", netns).Run()

	exec.Command("ip", "addr", "add", rootAddr4, "dev", rootVeth).Run()
	exec.Command("ip", "-6", "addr", "add", rootAddr6, "dev", rootVeth).Run()
	exec.Command("ip", "link", "set", rootVeth, "up").Run()

	exec.Command("ip", "-n", netns, "addr", "add", nsAddr4, "dev", nsVeth).Run()
	exec.Command("ip", "-n", netns, "-6", "addr", "add", nsAddr6, "dev", nsVeth).Run()
	exec.Command("ip", "-n", netns, "link", "set", nsVeth, "up").Run()
	exec.Command("ip", "-n", netns, "route", "add", "default", "via", rootIP4.String(), "dev", nsVeth).Run()
	exec.Command("ip", "-n", netns, "-6", "route", "add", "default", "via", rootIP6.String(), "dev", nsVeth).Run()
}

// ensureUplinkTable creates the shared "atlas-uplink" nft table if it does
// not exist, and adds a VPC's root-side veth to its set of interfaces. This
// is the second hop of a two-hop NAT; see network.go's setupNftables.
func ensureUplinkTable(rootVeth string) {
	if exec.Command("nft", "list", "table", "inet", "atlas-uplink").Run() != nil {
		script := `
add table inet atlas-uplink
add chain inet atlas-uplink postrouting { type nat hook postrouting priority srcnat ; }
add set inet atlas-uplink veth_set { type ifname ; }
add rule inet atlas-uplink postrouting iifname @veth_set masquerade
`
		command := exec.Command("nft", "-f", "-")
		command.Stdin = strings.NewReader(script)
		command.Run()
	}

	exec.Command("nft", "add", "element", "inet", "atlas-uplink", "veth_set", "{ "+rootVeth+" }").Run()
}

// nsIPCommand runs an `ip` subcommand inside a network namespace.
func nsIPCommand(netns string, args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"-n", netns}, args...)...)
}

// nsExecCommand runs a command inside a network namespace.
func nsExecCommand(netns, binary string, args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"netns", "exec", netns, binary}, args...)...)
}
