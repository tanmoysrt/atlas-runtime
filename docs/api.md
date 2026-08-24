# API Reference

Base URL: `http://<listen>`, where `<listen>` is the `[runtime]` value from
`config.toml`. The default is `127.0.0.1:9101`.

All HTTP endpoints use JSON. Endpoints that change configuration write the
change to `config.toml` and apply it live.

On the WebSocket endpoint, the server sends the ring buffer as a binary
message, and client input is forwarded to the serial input.

## Lifecycle

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/info` | Current state, config, and metadata. |
| `GET` | `/health` | Always returns `200`. |
| `POST` | `/start` | Persist `desired_state=running` and start the VM. |
| `POST` | `/stop` | Persist `desired_state=stopped` and stop the VM. |
| `POST` | `/reboot` | Stop and start the VM without changing `desired_state`. |
| `POST` | `/reload` | Re-read `config.toml` and apply live changes. |
| `POST` | `/resize` | Change vCPU count and memory size. The VM must be stopped. |
| `POST` | `/sysrq` | Send a raw character to the serial console. |

### GET /info

Response:

```json
{
  "instance_id": "vm-001",
  "initialized": true,
  "desired_state": "running",
  "config": { "runtime": { "listen": "127.0.0.1:9101" }, ... }
}
```

### POST /resize

Request body:

```json
{ "cpus": 4, "memory": 8589934592 }
```

`memory` is in bytes. The VM must be stopped; the new values apply on the
next `/start`.

### POST /sysrq

Request body:

```json
{ "key": "b" }
```

## Console

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/console` | Create a one-time token for WebSocket access. |
| `GET` | `/console/attach?token=<token>` | WebSocket connection to the serial console. |

### Token flow

1. `POST /console` returns `{ "token": "..." }`.
2. The token is valid for **60 seconds** and can be used **once**.
3. Open `ws://<host>/console/attach?token=...`.
4. The server sends the current **1 MiB ring buffer** immediately.
5. All future serial output is broadcast to open WebSocket connections.
6. Client messages are written to the serial input FIFO.

## SSH keys

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/ssh-keys` | List authorized keys. |
| `POST` | `/ssh-keys` | Add one key. |
| `DELETE` | `/ssh-keys/{id}` | Remove a key by index. |

### POST /ssh-keys

Request body:

```json
{ "key": "ssh-ed25519 AAAA..." }
```

The key is pushed to MMDS if the VM is running.

## Network

| Method | Path | Purpose |
| --- | --- | --- |
| `PUT` | `/network/bandwidth` | Change ingress and egress bandwidth limits. |
| `PUT` | `/network/public-ip` | Assign or clear the VM's public IPv4/IPv6 (1:1 NAT). |

### PUT /network/bandwidth

Request body:

```json
{ "ingress_bandwidth": 104857600, "egress_bandwidth": 52428800 }
```

Values are in **bits per second**. `0` means unlimited. The `tc` limits apply
live.

### PUT /network/public-ip

Request body:

```json
{ "public_ipv4": "203.0.113.5", "public_ipv6": "2400:6180:100:d0:0:1:7fd5:b000" }
```

An empty string clears that address. The NAT rules swap live, so no VM restart
is needed: the guest only ever sees its private address.

## Snapshots

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/snapshot` | Pause the VM, save state + memory + rootfs, then resume. |

### POST /snapshot

Request body:

```json
{ "id": "snap-001" }
```

The snapshot is written to `/var/lib/atlas/snapshots/<id>/`:

```text
state
memory
rootfs
metadata.json
```

Boot a new VM from a snapshot by setting `boot.snapshot = "snap-001"` in its
`config.toml`.