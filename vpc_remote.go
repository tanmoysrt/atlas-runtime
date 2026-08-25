package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// watchVPCMembers publishes this VM, then watches beacon for the other VMs of
// the VPC. It reconnects on error.
func (network *Network) watchVPCMembers() {
	if network.watchStop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	network.watchStop = cancel
	network.watchDone = make(chan struct{})
	go func() {
		defer close(network.watchDone)
		network.runVPCWatch(ctx)
	}()
}

func (network *Network) runVPCWatch(ctx context.Context) {
	labels := map[string]string{
		"vpc":  fmt.Sprintf("%d", network.config.VPC),
		"type": "vpc-membership",
	}
	healthy := true

	for ctx.Err() == nil {
		// Publish on every attempt. If beacon was down when this VM started,
		// this is what makes the node visible to its peers again.
		err := network.publishMembership()
		if err == nil {
			healthy = true
			err = network.beacon.watchOnce(ctx, labels, func(obj BeaconObject) {
				routeRemoteVM(network.config.VPC, network.nodeConfig, obj)
			})
		}
		if err != nil && healthy && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "vpc %d beacon: %v\n", network.config.VPC, err)
			healthy = false
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// publishMembership tells the other nodes where this VM is. The address and
// the node are labels, because beacon keeps labels on a tombstone: a watcher
// can still remove the right route after the VM is gone. The key holds the VM
// id, which stays the same when the VM moves.
func (network *Network) publishMembership() error {
	node := network.nodeConfig
	value := fmt.Sprintf(`{"gre_address":%q}`, node.Network.GreAddress)
	return network.beacon.Put(vpcMemberKey(network.config.VPC, network.instanceID), value,
		map[string]string{
			"vpc":  fmt.Sprintf("%d", network.config.VPC),
			"node": fmt.Sprintf("%d", node.NodeID),
			"ip":   network.guestAddress(),
			"type": "vpc-membership",
		})
}

// routeRemoteVM adds or removes the route to one remote VM. Several watchers
// share the namespace, so it takes the same lock as the setup path.
func routeRemoteVM(vpcID int, nodeConfig *NodeConfig, obj BeaconObject) {
	address := obj.Labels["ip"]
	if address == "" {
		return
	}
	if obj.Labels["node"] == strconv.Itoa(nodeConfig.NodeID) {
		return
	}

	var owner struct {
		GreAddress string `json:"gre_address"`
	}
	json.Unmarshal([]byte(obj.Value), &owner)

	lock := newFileLock(networkLockPath)
	if lock.lock() != nil {
		return
	}
	defer lock.unlock()

	namespace := vpcNamespaceName(vpcID)
	if obj.Deleted {
		runIP(namespace, vmCommands(vpcID, address, ""))
		return
	}
	if owner.GreAddress == "" {
		return
	}
	allowGREPeer(owner.GreAddress)
	if createVPCTunnel(namespace, nodeConfig.Network.GreAddress, vpcID) != nil {
		return
	}
	runIP(namespace, vmCommands(vpcID, address, owner.GreAddress))
}

// vmCommands points the addresses of one remote VM at the tunnel, or removes
// them when nodeAddress is empty. The route says which device to use, and the
// neighbour says which node to send to. A moved VM keeps its address, so these
// replace an entry instead of adding one.
//
// A bare address means a host route, and `ip -batch` reads the family from it.
func vmCommands(vpcID int, address, nodeAddress string) []string {
	device := greName(vpcID)
	var commands []string
	for _, ip := range []string{address, guestIPv6(address)} {
		switch {
		case ip == "":
		case nodeAddress == "":
			commands = append(commands,
				"route del "+ip,
				"neigh del "+ip+" dev "+device)
		default:
			commands = append(commands,
				"route replace "+ip+" dev "+device,
				"neigh replace "+ip+" lladdr "+nodeAddress+" dev "+device+" nud permanent")
		}
	}
	return commands
}

// runIP sends all commands to one `ip` process, because a large node programs
// many thousands of entries. An empty namespace means root. "-force" keeps
// going when a line fails, such as a delete of an entry that is already gone.
func runIP(namespace string, commands []string) {
	if len(commands) == 0 {
		return
	}
	args := []string{"-force", "-batch", "-"}
	if namespace != "" {
		args = append([]string{"-n", namespace}, args...)
	}
	runScript(exec.Command("ip", args...), strings.Join(commands, "\n")+"\n")
}

// createVPCTunnel makes the one tunnel "g<vpc>" that reaches every other node.
// It has no remote address, so a neighbour entry picks the node. The GRE key
// is the VPC id, which keeps two VPCs apart on the same addresses.
//
// The tunnel is built in root and then moved, because a GRE device keeps its
// original namespace for the outer packet. The outer packet therefore stays in
// root, where the local address exists and the uplink NAT leaves it alone.
func createVPCTunnel(namespace, localAddr string, vpcID int) error {
	name := greName(vpcID)
	if exec.Command("ip", "-n", namespace, "link", "show", name).Run() == nil {
		return nil
	}

	// Remove a device that a failed move left behind in root.
	exec.Command("ip", "link", "del", name).Run()

	if err := exec.Command("ip", "link", "add", name, "type", "gre",
		"local", localAddr, "key", fmt.Sprintf("%d", vpcID)).Run(); err != nil {
		return err
	}
	if err := exec.Command("ip", "link", "set", name, "netns", namespace).Run(); err != nil {
		exec.Command("ip", "link", "del", name).Run()
		return err
	}
	return exec.Command("ip", "-n", namespace, "link", "set", name, "up").Run()
}

// ensureGREFilter drops GRE from any address that is not a known node. The
// tunnels have no remote address, so without this the kernel accepts a packet
// from any sender that gives the right key.
func ensureGREFilter() {
	if exec.Command("nft", "list", "table", "inet", "atlas-gre").Run() == nil {
		return
	}
	runScript(exec.Command("nft", "-f", "-"), `
add table inet atlas-gre
add set inet atlas-gre node_set { type ipv4_addr ; }
add chain inet atlas-gre input { type filter hook input priority -10 ; }
add rule inet atlas-gre input meta l4proto gre ip saddr != @node_set drop
`)
}

// allowGREPeer lets GRE from one node through. The address stays after the
// node leaves a VPC, because other VPCs may still use that node.
func allowGREPeer(greAddress string) {
	exec.Command("nft", "add", "element", "inet", "atlas-gre", "node_set",
		"{ "+greAddress+" }").Run()
}

func greName(vpcID int) string {
	return fmt.Sprintf("g%d", vpcID)
}
