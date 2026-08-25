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

## Snapshots

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/snapshot` | Stops the VM for a moment, saves it, then starts it again. |

### POST /snapshot

```json
{ "id": "snap-001" }
```

Atlas writes the snapshot to `/var/lib/atlas/snapshots/<id>/`:

```text
state
memory
rootfs
metadata.json
```

To start a new VM from this snapshot, set `boot.snapshot = "snap-001"` in the `config.toml` of the new machine directory.

## Dashboard

If you start the runtime with `-enable-dashboard`, it serves a web page at `GET /`. The page uses the endpoints above. Without the flag, `GET /` answers `404`.
