# API Reference

The base URL is `http://<listen>`. The `<listen>` value comes from the `[runtime]` section of `config.toml`. The default is `127.0.0.1:9101`.

Some notes that apply to all the endpoints:

- The requests and the responses use JSON.
- An endpoint that changes the configuration writes the change to `config.toml`, and applies it immediately.
- The runtime serves one VM. There is no VM name in a path.

## Lifecycle

| Method | Path | What it does |
| --- | --- | --- |
| `GET` | `/health` | Always answers `200`. |
| `GET` | `/info` | Gives the state, the configuration, and the metadata. |
| `POST` | `/start` | Writes `desired_state=running`, then starts the VM. |
| `POST` | `/stop` | Writes `desired_state=stopped`, then stops the VM. |
| `POST` | `/reboot` | Stops and starts the VM. `desired_state` does not change. |
| `POST` | `/reload` | Reads `config.toml` again and applies the live parts. |
| `POST` | `/resize` | Changes the number of CPUs and the memory. |
| `POST` | `/rootfs` | Makes the disk larger and sets its speed limits. |
| `POST` | `/sysrq` | Sends one character to the serial console. |

### GET /info

```json
{
  "instance_id": "vm-001",
  "initialized": true,
  "desired_state": "running",
  "config": { "runtime": { "listen": "127.0.0.1:9101" } }
}
```

### POST /resize

```json
{ "cpus": 4, "memory": 8589934592 }
```

`memory` is in bytes. The VM must be stopped. The new values apply at the next `POST /start`.

### POST /rootfs

```json
{ "size": 17179869184, "bandwidth": 104857600, "iops": 2000 }
```

- `size` is in bytes. You can only make the disk larger. A smaller value keeps the size that the disk has now.
- `bandwidth` is in bytes for each second. `iops` is in operations for each second.
- A `bandwidth` or an `iops` of `0` or less means no limit.

If the VM runs, the guest grows its own filesystem.

### POST /sysrq

```json
{ "key": "b" }
```

## Console

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/console` | Makes a token for one WebSocket connection. |
| `GET` | `/console/attach?token=<token>` | Connects to the serial console. |

The token flow has these steps:

1. `POST /console` gives you `{ "token": "..." }`.
2. The token is good for 60 seconds, and for one connection only.
3. Open `ws://<host>/console/attach?token=...`.
4. The server sends the ring buffer of 1 MiB immediately, as one binary message.
5. The server then sends all new serial output to each open connection.
6. The server writes each message from a client to the serial input.

## SSH keys

| Method | Path | What it does |
| --- | --- | --- |
| `GET` | `/ssh-keys` | Lists the keys. |
| `POST` | `/ssh-keys` | Adds one key. |
| `DELETE` | `/ssh-keys/{id}` | Removes one key by its position in the list. |

### POST /ssh-keys

```json
{ "key": "ssh-ed25519 AAAA..." }
```

If the VM runs, Atlas sends the key to MMDS immediately.

## Network

| Method | Path | What it does |
| --- | --- | --- |
| `PUT` | `/network/bandwidth` | Changes the speed limits of the network. |
| `PUT` | `/network/public-ip` | Sets or clears the public addresses. |
| `GET` | `/firewall` | Lists the firewall rules. |
| `PUT` | `/firewall` | Replaces all the firewall rules. |

### PUT /network/bandwidth

```json
{ "ingress_bandwidth": 104857600, "egress_bandwidth": 52428800 }
```

The values are in bits for each second. A value of `0` means no limit. The `tc` limits change immediately.

### PUT /network/public-ip

```json
{ "public_ipv4": "203.0.113.5", "public_ipv6": "2400:6180:100:d0:0:1:7fd5:b000" }
```

An empty string clears that address. The NAT rules change immediately, and the VM does not stop. The guest always sees only its private address.

### GET /firewall

```json
{ "ingress": [{ "protocol": "tcp", "port": 22 }], "egress": [{ "protocol": "all" }] }
```

Both lists are always present. They are empty when no `[firewall]` section exists in `config.toml`.

### PUT /firewall

```json
{
  "ingress": [
    { "protocol": "tcp", "port": 22, "source": "203.0.113.0/24" },
    { "protocol": "icmp" }
  ],
  "egress": [
    { "protocol": "tcp", "port": 443 }
  ]
}
```

This replaces all the rules. An empty list denies all traffic in that direction. A rule has these fields:

- `protocol` is `tcp`, `udp`, `icmp`, or `all`.
- `port` is one port, or the start of a range. `port_end` ends the range.
- `source` and `destination` are an optional IP or CIDR.

There are no defaults. Without rules, the VM denies all traffic both ways, except that the guest may always send `udp/53` to `network.nameservers`. The change applies immediately, and the VM does not stop.

## Snapshots

A snapshot is a copy of the rootfs. It holds no memory and no CPU state.

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/snapshot` | Copies the rootfs into a new snapshot. |
| `GET` | `/snapshots` | Lists the snapshots of this VM. |
| `DELETE` | `/snapshots/{id}` | Removes one snapshot of this VM. |

### POST /snapshot

The request has no body. Atlas makes the identifier and returns the record:

```json
{
  "id": "snap-k3f9x2mq7b",
  "instance_id": "vm-001",
  "created_at": "2026-08-25T18:20:03Z",
  "size": 2147483648,
  "live": true
}
```

`live` tells you if the VM kept running. On a filesystem that can do reflink, such as XFS with `reflink=1` or Btrfs, the copy is immediate and `live` is `true`. Without reflink, Atlas stops the VM for the length of the copy, starts it again, and `live` is `false`. `desired_state` does not change.

`size` is the logical size of the rootfs file. The disk space that the snapshot uses is smaller while it shares extents with the rootfs of the VM.

A snapshot is crash-consistent, not application-consistent. It is the same as a power loss: the guest replays its journal when it mounts the filesystem. Stop the VM first, or make the application flush its data first, if you need more.

Atlas writes the snapshot to `/var/lib/atlas/snapshots/<vm-id>/<snapshot-id>/`:

```text
rootfs
metadata.json
```

### GET /snapshots

Returns a JSON array of the records above. The array is empty if this VM has no snapshot.

### DELETE /snapshots/{id}

Removes the directory of the snapshot. The filesystem frees only the extents that no other clone of the rootfs uses, so a VM that started from this snapshot is not affected.

### Start a new VM from a snapshot

Set `boot.snapshot` in the `config.toml` of a new machine directory to `"<vm-id>/<snapshot-id>"`:

```toml
[boot]
snapshot = "vm-001/snap-k3f9x2mq7b"
```

The new VM makes its own clone of the snapshot rootfs, then does a normal cold boot. `rootfs.size` must not be smaller than the rootfs of the snapshot.

## Dashboard

If you start the runtime with `-enable-dashboard`, it serves a web page at `GET /`. The page uses the endpoints above. Without the flag, `GET /` answers `404`.
