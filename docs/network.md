# Network

Each VM belongs to a VPC. Set it with `network.vpc`. Each VPC has its own Linux network namespace, so two VPCs stay apart. Two VPCs can use the same addresses, and even the same address for two different VMs.

```text
VM (eth0)
  │
TAP (atap-<instance-id>)                    inside the namespace atlas-vpc-<id>
  │
nftables NAT and forwarding, one table for each VM
  │
veth (vp<id>n) ─────── veth (vp<id>r)
                            │
                      nftables NAT, the shared atlas-uplink table    root namespace
                            │
                        the uplink of the host
```

## The namespace

`ensureVPCNetwork` in `vpc.go` makes the namespace and the veth pair when the first VM of a VPC starts. A file lock guards this step, because many VMs can start at the same time.

Each VM writes a marker file in `/run/atlas-vpc-<id>/vms/`. The namespace stays while that directory holds files, because the other VMs of the VPC still need it. The last VM to stop removes the namespace. This also removes the veth pair and the tunnels inside it.

Atlas starts Firecracker inside the namespace with `nsenter`, because Firecracker opens the TAP device by name.

## Addresses

`network.address` holds one address, and that is all a VM gets. It needs no mask. The guest carries the address as a `/32`, and the VPC routes each address on its own. A VPC is therefore one routed domain and not a set of subnets. Two VMs of one VPC need no relation between their addresses.

Atlas makes the MAC of the guest from the address. The address `10.10.1.30` gives the MAC `06:00:0a:0a:01:1e`. The guest reads its own address back out of the MAC, so `config.toml` holds no `mac` key.

The link to the guest is a point-to-point pair:

- Each end has a bare `/32` address, or a `/128` address for IPv6.
- Each end has one route to the other end. There is no shared subnet.
- The host side of the TAP is always `169.254.1.1` and `fd00::1`. It is the same in each VPC, because this address never leaves the namespace.

The link from the veth to root is different. Root has one routing table for all the VPCs, so each VPC needs its own address there. Atlas gives each VPC a `/31` from `169.254.128.0/17`, and a `/127` from `fd01::`.

The veth uses only the top half of `169.254.0.0/16`. This keeps it away from `169.254.1.1`, which is present in each namespace. The half range gives one `/31` to each VPC id from 1 to 16384. This is also the limit that `config.go` accepts.

## Egress to the internet

Set `network.egress = "host"`. Traffic that leaves through the uplink crosses two NAT points:

1. In the VPC namespace, `MASQUERADE` changes the private address of the guest to the address of the veth.
2. In root, the shared `atlas-uplink` table changes the address of the veth to the real address of the host.

Two steps are necessary. One masquerade to the physical card is not enough. If two VPCs use the same private address, the reverse NAT in root finds that address before root selects a route.

## Public addresses

Set `network.public_ipv4` and `network.public_ipv6`. A public address maps 1:1 to the private address of the guest:

```text
public address     --DNAT-->   private address    (traffic that comes in)
private address    --SNAT-->   public address     (traffic that goes out)
```

The guest never sees the public address. The NAT rules live in the VPC namespace of that VM, where the private address is never ambiguous. Root holds only one route for the public address, through the veth of the VPC. Public addresses are unique, so this route is safe.

To change a public address, use `PUT /network/public-ip`, or edit `config.toml` and send `POST /reload`. The VM does not stop.

## More than one node

A VPC can hold VMs on more than one node. Each node needs the file `/var/lib/atlas/config.json`. The file gives the node three values:

- `node_id`, the number of this node.
- `gre_address`, the address of this node on the underlay network.
- `beacon_endpoint`, the URL of beacon.

Without this file, the node runs alone. Only the VMs on that node can then speak to each other.

The runtime reads the file one time, when a VM starts. Write the file before `atlas-vms.target` starts. If you write it later, the VMs of that boot stay alone until you start them again.

### How the nodes find each other

The nodes use beacon, a small key-value service. Each VM writes one object for itself:

```text
key      vpc-member/<vpc-id>/<vm-id>
value    {"gre_address":"10.1.0.11"}
labels   vpc=<vpc-id>  node=<node-id>  ip=<vm-address>  type=vpc-membership
```

The address and the node are labels, and not part of the value. Beacon keeps the labels on a tombstone, so a watcher still knows which route to remove after the object is gone.

Each VM opens a WebSocket to beacon and subscribes by label. Beacon therefore sends only the objects of that VPC. The watcher writes its own object again at each connection, so a node that started while beacon was down becomes visible again when beacon answers. A VM deletes its own object when it stops.

The routes are for one VM, and not for a subnet. A VM therefore keeps its address when it moves to another node. The key stays the same, the `node` label changes, and the other nodes move the route.

Each VM also caches the members it learns in `vpc-members.json`, in the machine directory. The cache holds the members and the highest beacon timestamp it has seen, the revision. When the host starts again, Atlas rebuilds the routes from this cache before it reaches beacon. A reboot during a beacon outage therefore does not cut off cross-node traffic. When beacon returns, the watcher subscribes with `since=<revision>`, so it downloads only the changes that came after the cache, instead of every member again.

### The tunnel

Each VPC has one tunnel `g<vpc-id>` on a node, and that tunnel reaches every other node. The tunnel has no remote address. The neighbour entry of each remote VM says which node to send to.

```text
node 1                                   node 2
  atlas-vpc-1                              atlas-vpc-1
    10.10.1.30 dev atap-vm-a1  (local)       10.10.2.30 dev atap-vm-b1  (local)
    10.10.2.30 dev g1          (remote)      10.10.1.30 dev g1          (remote)
    neigh 10.10.2.30 -> node 2               neigh 10.10.1.30 -> node 1
         │                                        │
         └────────────── g1, key 1 ───────────────┘
```

The IPv6 address of a VM gets the same route and the same neighbour entry. On a GRE device the `lladdr` of a neighbour is an IPv4 address, so both families point to the node.

One tunnel for each VPC, and not one for each pair of nodes, keeps the number of devices small. A VPC with VMs on 100 nodes needs 1 tunnel on each node, and not 99.

Three more points about the tunnel:

- Atlas builds the tunnel in root, then moves it into the namespace. A GRE device keeps its first namespace for the outer packet. The outer packet therefore stays in root, where `gre_address` exists, and where the uplink NAT does not touch it. The inner packet belongs to the VPC.
- The GRE key is the VPC id. Two VPCs between the same two nodes therefore stay apart, even when they use the same addresses.
- A tunnel with no remote address accepts a packet from any sender that gives the correct key. The `atlas-gre` table in root therefore drops GRE from each address that is not a known node.

## Packet size

A GRE tunnel adds 24 bytes to each packet. The tunnel therefore carries 1472 bytes, and not 1500. The guest keeps an MTU of 1500, and knows nothing about the tunnel.

The forward chain of each VM sets the maximum segment size of TCP to the MTU of the route that the packet takes:

```text
tcp flags syn tcp option maxseg size set rt mtu
```

This rule is the first rule in the chain, because an `accept` rule ends the chain. Traffic that stays on the node keeps the full segment size, because the MTU of that route is still 1500.

The rule is for TCP only. A guest that sends UDP datagrams of more than 1472 bytes to another node must keep them small itself. If it does not, the tunnel divides them.

## Bandwidth

The speed limits use `tc` on the TAP device, inside the VPC namespace. Set them with `network.ingress_bandwidth` and `network.egress_bandwidth`, or with `PUT /network/bandwidth`.

## Firewall

Each VM has a firewall, and both directions are deny-by-default. The rules live in the VM's nftables table, in a `fw` chain that ends with a `drop` rule. The `forward` chain jumps to it only for traffic of this VM, so other VMs in the same namespace are not affected.

Set the rules in `config.toml`:

```toml
[firewall]
[[firewall.ingress]]
protocol = "tcp"
port = 22
source = "203.0.113.0/24"
[[firewall.ingress]]
protocol = "icmp"
[[firewall.egress]]
protocol = "tcp"
port = 443
destination = "10.0.0.0/8"
```

A rule has these fields:

- `protocol` is `tcp`, `udp`, `icmp`, or `all`.
- `port` is one port, or the start of a range. `port_end` ends the range. A `port_end` of `0` or equal to `port` means one port. `icmp` and `all` take no port.
- `source` and `destination` are an IP or CIDR, and are optional. Empty means anywhere. On an ingress rule, `source` is the caller and `destination` is the guest. On an egress rule they are the opposite.

There are no defaults. With no `[firewall]` section, the VM denies all traffic in both directions. One rule is always present and cannot be changed or removed through the API: the guest may send `udp/53` to the nameservers in `network.nameservers`. This is how the guest resolves names.

The MMDS address `169.254.169.254` and the gateway `169.254.1.1` never cross the `forward` chain. Firecracker answers MMDS inside the VM, and the gateway is a local address of the namespace. The firewall therefore never blocks them.

One caveat: DNAT and SNAT run around the `forward` hook. So on an ingress rule, `destination` matches the private address of the guest, never its public address. On an egress rule, `source` matches the private address.

To change the rules while the VM runs, use `PUT /firewall`, or edit `config.toml` and send `POST /reload`. The `fw` chain is rebuilt atomically, and the VM does not stop.
