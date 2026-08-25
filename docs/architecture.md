# Architecture

Atlas-runtime is one Go process for each VM. There is no controller, no database, and no cluster manager. systemd starts one process for each machine directory.

```text
systemd
  └── atlas-runtime (one process for each VM)
        ├── HTTP API
        ├── Firecracker process
        ├── TAP device and nftables NAT, inside the VPC network namespace
        ├── tc bandwidth limits
        ├── serial console: ring buffer and WebSocket
        └── MMDS metadata service
```

## Files

Each file has one job. There are no packages below the root.

| File | What it does |
| --- | --- |
| `main.go` | Reads the command line. Handles the SIGUSR1 reload signal. |
| `config.go` | The TOML structures, `LoadConfig`, and `Validate`. |
| `metadata.go` | Reads and writes `metadata.json` atomically. |
| `runtime.go` | Start, Stop, Reboot, Reload, Resize, SysRq, and snapshots. |
| `firecracker.go` | The Firecracker process, its API, and MMDS. |
| `snapshot.go` | The record of a snapshot: make the identifier, list, and delete. |
| `network.go` | The TAP device of one VM, its nftables rules, and its `tc` limits. |
| `vpc.go` | The VPC namespace, the veth to root, and the shared uplink NAT. |
| `vpc_remote.go` | The other nodes: the beacon watch, the GRE tunnel, and the routes. |
| `vpc_members.go` | Caches the VPC members of the other nodes in the machine directory. |
| `beacon.go` | The client for beacon, over HTTP and over WebSocket. |
| `nodeconfig.go` | The identity of the host: `node_id`, `gre_address`, `beacon_endpoint`. |
| `helpers.go` | Small shared parts: the file lock, and the addresses of a guest. |
| `console.go` | A ring buffer of 1 MiB, the serial reader, and the WebSocket. |
| `api.go` | The HTTP handlers and the console WebSocket. |
| `image.go` | Finds an image from `file://`, `http://`, or `https://`. |
| `disk.go` | Clones or copies the rootfs. Grows it. Sets the project quota. |

## Dependencies

Atlas uses the Go standard library and two packages:

- `github.com/BurntSushi/toml` reads `config.toml`.
- `github.com/gorilla/websocket` serves the console and watches beacon.

Atlas also calls these programs on the host:

- `firecracker` runs the microVM. Atlas starts it with `nsenter`, inside the VPC namespace, because Firecracker opens the TAP device by name.
- `ip`, `ip netns`, `nft`, and `tc` build the network.
- `truncate`, `e2fsck`, and `resize2fs` grow the rootfs. Atlas asks for the copy-on-write clone itself, with the `FICLONE` ioctl, and uses `cp --sparse=always` when the filesystem cannot do reflink.
- `xfs_quota` and `findmnt` set the disk quota. These are optional, and XFS only.

## More documents

- [getting-started.md](getting-started.md) tells you how to install and start a VM.
- [state.md](state.md) tells you what Atlas keeps on disk.
- [network.md](network.md) explains the network.
- [disk.md](disk.md) explains the rootfs.
- [api.md](api.md) lists the endpoints.
