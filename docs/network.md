# Network

Every VM belongs to a VPC, set with `network.vpc`. Two VPCs may use the same
CIDR or even the same private IP. To keep them apart, each VPC has its own
Linux network namespace.

```text
VM (eth0)
  │
TAP (atap-<instance_id>)                      inside netns atlas-vpc-<id>
  │
nftables NAT and forwarding (per-VM table)         (vpc.go, network.go)
  │
veth (vp<id>n) ──────── veth (vp<id>r)
                              │
                        nftables NAT (shared atlas-uplink table)   root netns
                              │
                          host uplink
```

`ensureVPCNetwork` (vpc.go) creates the namespace and the veth pair when the
first VM of a VPC starts. The step is idempotent and guarded by a file lock,
because many VMs can start at the same time at boot.

The namespace and veth stay up after a VM stops; another VM in the same VPC
may still need them. Nothing tracks whether a VPC has any VMs left.

Firecracker runs inside the VPC namespace via `nsenter --net=...`, because it
opens the TAP device by name.

## Addressing

The guest link is a point-to-point pair. Each end carries a bare `/32` (or
`/128`) address and an explicit route to the other end; there is no shared
subnet. The host side of the TAP is always `169.254.1.1` / `fd00::1`
(network.go), the same on every TAP in every VPC. This address never leaves
the VPC namespace, so forwarding only cares about the interface. Any two VMs
can use any two private IPs.

The veth-to-root link is a `/31` (or `/127`) subnet, unique per VPC and
derived from the VPC id (`169.254.0.0/16` / `fd01::`, vpc.go). Root's routing
table spans every VPC, so each VPC needs its own address there.

## Egress

Set `network.egress = "host"`. Guest traffic leaving through the uplink
crosses two NAT points:

1. Inside the VPC namespace, `MASQUERADE` rewrites the guest's private IP to
   the veth's address.
2. In root, the shared `atlas-uplink` table masquerades the veth's address to
   the host's real address.

Two hops are required. A single masquerade to the physical NIC would break
when two VPCs share a private IP: root's reverse NAT would resolve to that
address before root picks a route.

## Public IPs

Set `network.public_ipv4` and `network.public_ipv6`. A public address NATs 1:1
to the guest's private address:

```text
public IP  --DNAT-->  guest private IP   (inbound)
guest private IP  --SNAT-->  public IP   (outbound)
```

The guest never sees the public address. The NAT rules live in the VM's own
VPC namespace, where the private IP is never ambiguous. Root only holds one
route: `<public IP> via <veth-ns-addr> dev <veth-root>`. Public IPs are
unique, so this route is safe.

Public IPs change live via `PUT /network/public-ip`, or by editing
`config.toml` and calling `/reload`. No VM restart is needed.

Bandwidth limits use `tc` on the TAP device, inside the VPC namespace.