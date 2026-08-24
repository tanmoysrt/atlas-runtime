# Atlas-runtime

Runs Firecracker microVMs on a Linux host. One Go process per VM, managed by
systemd. No controller, no database, no cluster manager.

## Docs

- [Getting started](docs/getting-started.md): setup and first VM
- [Architecture](docs/architecture.md): design and file layout
- [State](docs/state.md): VM state and lifecycle
- [Network](docs/network.md): VPC networking
- [Disk](docs/disk.md): rootfs and disk I/O
- [API](docs/api.md): HTTP API reference

## Install

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/tanmoysrt/atlas-runtime/main/scripts/setup.sh)
```

See [Getting started](docs/getting-started.md) for the full guide.

## License

[GNU Affero General Public License v3.0](LICENSE)