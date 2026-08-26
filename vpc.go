package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// A VPC is a Linux network namespace shared by all VMs on this node
// that belong to the same VPC. VMs from different VPCs are isolated
// because they live in different namespaces.

// VM lifecycle: enter and leave the VPC

// vpcEnter is called when a VM starts. It creates a marker file so that we
// know this node has VMs in the VPC, then creates the namespace and the veth
// if they do not exist.
//
// It returns the namespace name so the caller can create the TAP inside it.
func vpcEnter(vpcID int, instanceID string) (string, error) {
	vmDir := filepath.Join(vpcRunDir(vpcID), "vms")
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return "", err
	}

	lock := newFileLock(vpcLockPath(vpcID))
	if err := lock.lock(); err != nil {
		return "", err
	}
	defer lock.unlock()

	// Touch a file named after this VM. The count of files tells us
	// whether any VMs are still running in this VPC on this node.
	if err := os.WriteFile(filepath.Join(vmDir, instanceID), nil, 0644); err != nil {
		return "", err
	}

	// Create namespace, veth, and uplink NAT if missing.
	if err := ensureVPCNetwork(vpcID); err != nil {
		return "", err
	}

	return vpcNamespaceName(vpcID), nil
}

// vpcLeave is called when a VM stops. It removes the marker file. If no VMs
// remain, it deletes the namespace, which also removes the GRE tunnels and
// the veth pair.
func vpcLeave(vpcID int, instanceID string) error {
	if err := os.MkdirAll(filepath.Join(vpcRunDir(vpcID), "vms"), 0755); err != nil {
		return err
	}

	lock := newFileLock(vpcLockPath(vpcID))
	if err := lock.lock(); err != nil {
		return err
	}
	defer lock.unlock()

	vmDir := filepath.Join(vpcRunDir(vpcID), "vms")
	os.Remove(filepath.Join(vmDir, instanceID))
	if entries, _ := os.ReadDir(vmDir); len(entries) > 0 {
		return nil
	}

	rootVeth, _ := vpcVethNames(vpcID)
	exec.Command("nft", "delete", "element", "inet", "atlas-uplink", "veth_set", "{ "+rootVeth+" }").Run()
	exec.Command("ip", "netns", "del", vpcNamespaceName(vpcID)).Run()
	os.RemoveAll(vpcRunDir(vpcID))
	return nil
}

// Namespace and veth setup

func ensureVPCNetwork(vpcID int) error {
	namespace := vpcNamespaceName(vpcID)

	lock := newFileLock(networkLockPath)
	if err := lock.lock(); err != nil {
		return err
	}
	defer lock.unlock()

	exec.Command("ip", "netns", "add", namespace).Run()
	exec.Command("ip", "-n", namespace, "link", "set", "lo", "up").Run()

	rootVeth, namespaceVeth := vpcVethNames(vpcID)
	if exec.Command("ip", "link", "show", rootVeth).Run() != nil {
		createVeth(vpcID, namespace, rootVeth, namespaceVeth)
	}

	exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	exec.Command("sysctl", "-w", "net.ipv6.conf.all.forwarding=1").Run()
	ensureUplinkTable(rootVeth)
	ensureGREFilter()

	return nil
}

func createVeth(vpcID int, namespace, rootVeth, namespaceVeth string) {
	rootAddr4, namespaceAddr4 := vpcVethAddress4(vpcID)
	rootAddr6, namespaceAddr6 := vpcVethAddress6(vpcID)
	rootIP4, _, _ := net.ParseCIDR(rootAddr4)
	rootIP6, _, _ := net.ParseCIDR(rootAddr6)

	runIP("", []string{
		"link add " + rootVeth + " type veth peer name " + namespaceVeth,
		"link set " + namespaceVeth + " netns " + namespace,
		"addr add " + rootAddr4 + " dev " + rootVeth,
		"addr add " + rootAddr6 + " dev " + rootVeth,
		"link set " + rootVeth + " up",
	})

	runIP(namespace, []string{
		"addr add " + namespaceAddr4 + " dev " + namespaceVeth,
		"addr add " + namespaceAddr6 + " dev " + namespaceVeth,
		"link set " + namespaceVeth + " up",
		"route add default via " + rootIP4.String() + " dev " + namespaceVeth,
		"route add default via " + rootIP6.String() + " dev " + namespaceVeth,
	})
}

// ensureUplinkTable makes the NAT that the VPC uplinks share in the root
// namespace. It masquerades a veth source address only, and holds the IPv6
// rule only when the host has a global IPv6. The flush keeps veth_set.
func ensureUplinkTable(rootVeth string) {
	script := fmt.Sprintf(`
add table inet atlas-uplink
add chain inet atlas-uplink postrouting { type nat hook postrouting priority srcnat ; }
add set inet atlas-uplink veth_set { type ifname ; }
flush chain inet atlas-uplink postrouting
add rule inet atlas-uplink postrouting iifname @veth_set ip saddr %s masquerade
%s`, vethRange4, uplinkRule6())
	runScript(exec.Command("nft", "-f", "-"), script)
	exec.Command("nft", "add", "element", "inet", "atlas-uplink", "veth_set", "{ "+rootVeth+" }").Run()
}

func uplinkRule6() string {
	if !hasHostGlobalIPv6() {
		return ""
	}
	return fmt.Sprintf("add rule inet atlas-uplink postrouting iifname @veth_set ip6 saddr %s masquerade\n", vethRange6)
}

const networkLockPath = "/run/atlas-network.lock"

func vpcNamespaceName(vpcID int) string {
	return fmt.Sprintf("atlas-vpc-%d", vpcID)
}

func vpcRunDir(vpcID int) string {
	return fmt.Sprintf("/run/atlas-vpc-%d", vpcID)
}

func vpcLockPath(vpcID int) string {
	return filepath.Join(vpcRunDir(vpcID), "lock")
}

func vpcMemberKey(vpcID int, instanceID string) string {
	return fmt.Sprintf("vpc-member/%d/%s", vpcID, instanceID)
}

func vpcVethNames(vpcID int) (rootSide, namespaceSide string) {
	return fmt.Sprintf("vp%dr", vpcID), fmt.Sprintf("vp%dn", vpcID)
}

const vethRangeStart = 0x8000

// The full ranges that vpcVethAddress4 and vpcVethAddress6 draw from.
const (
	vethRange4 = "169.254.128.0/17"
	vethRange6 = "fd01::/112"
)

func vpcVethAddress4(vpcID int) (rootAddr, namespaceAddr string) {
	base := vethRangeStart | ((vpcID * 2) & 0x7FFE)
	root := net.IPv4(169, 254, byte(base>>8), byte(base))
	namespace := net.IPv4(169, 254, byte((base+1)>>8), byte(base+1))
	return root.String() + "/31", namespace.String() + "/31"
}

func vpcVethAddress6(vpcID int) (rootAddr, namespaceAddr string) {
	base := (vpcID * 2) & 0xFFFE
	return fmt.Sprintf("fd01::%x/127", base), fmt.Sprintf("fd01::%x/127", base+1)
}
