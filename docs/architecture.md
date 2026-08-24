# Architecture

Atlas-runtime is one Go process per VM. There is no controller, no database,
and no cluster manager.

```text
systemd
  └── atlas-runtime
        ├── HTTP API
        ├── Firecracker process
        ├── TAP device + nftables NAT, inside a per-VPC network namespace
        ├── tc bandwidth limits
        ├── serial console ring buffer + WebSocket broadcast
        └── MMDS metadata service
```

## Files

| File | Responsibility |
| --- | --- |
| `main.go` | CLI entry point. Handles the SIGUSR1 reload signal. |
| `config.go` | TOML structs. `LoadConfig` and `Validate`. |
| `metadata.go` | Atomic JSON read and write. |
| `runtime.go` | Orchestrator. Start, Stop, Reboot, Reload, Resize, SysRq, Snapshot, initialize. |
| `firecracker.go` | Firecracker lifecycle: process, API client, snapshot create/restore, MMDS. |
| `network.go` | Per-VM TAP setup, nftables NAT and forwarding, `tc` limits. |
| `vpc.go` | Per-VPC network namespace, veth-to-root link, shared uplink NAT. |
| `console.go` | 1 MiB ring buffer, serial FIFO reader, WebSocket broadcast. |
| `api.go` | HTTP handlers and the WebSocket console endpoint. |
| `image.go` | Image resolution: `file://`, `http://`, `https://`. |
| `disk.go` | Rootfs preparation with XFS reflink. Project quota setup. |

## Dependencies

Go standard library, plus:

- `github.com/BurntSushi/toml` for config parsing.
- `github.com/gorilla/websocket` for the console WebSocket.

External tools:

- `firecracker` for the microVM. Launched through `nsenter` into the VM's VPC
  network namespace.
- `ip` (including `ip netns`), `nft`, `tc` for networking.
- `xfs_quota` for disk quotas. Optional, XFS only.
- `cp --reflink=auto`, `truncate`, `resize2fs` for rootfs preparation.

## More docs

- [getting-started.md](getting-started.md): setup and first VM
- [state.md](state.md): VM state and lifecycle
- [network.md](network.md): VPC networking
- [disk.md](disk.md): rootfs and disk I/O
- [api.md](api.md): HTTP API