# Atlas-runtime

Atlas-runtime runs Firecracker microVMs on a Linux host. There is one Go process for each VM, and systemd starts and stops them. There is no controller, no database, and no cluster manager.

## Documents

- [Getting started](docs/getting-started.md) tells you how to install the runtime and start your first VM.
- [Architecture](docs/architecture.md) shows the design and the file layout.
- [State](docs/state.md) tells you what Atlas keeps on disk, and how a VM starts and stops.
- [Network](docs/network.md) explains VPCs, addresses, and traffic between nodes.
- [Disk](docs/disk.md) explains the rootfs and the disk limits.
- [API](docs/api.md) lists the HTTP endpoints.

## Install

You need Linux with systemd, and root access.

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/tanmoysrt/atlas-runtime/main/scripts/setup.sh)
```

Read [Getting started](docs/getting-started.md) for the full procedure.

## License

[GNU Affero General Public License v3.0](LICENSE)
