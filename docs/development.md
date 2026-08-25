# Local Development

`scripts/atlas-dev.sh` runs one VM for tests. It does not install systemd units, and it does not use `/var/lib/atlas`. Everything stays in `dev-vm/` in the checkout.

## Before you start

You need:

- Linux with `/dev/kvm`.
- Root access. The network setup needs `ip`, `ip netns`, and `nft`.

## Start a VM

```bash
scripts/atlas-dev.sh
```

The first run does all of this:

1. It downloads the newest Firecracker release and a kernel.
2. It builds a test rootfs.
3. It writes a default `config.toml` for a VM with the name `vm-001`.
4. It builds the runtime and starts it in the foreground.

The script starts the VM when the API answers. The serial output then takes over the terminal.

If you run the script again, it continues with the same VM. It never replaces a kernel, an image, or a machine directory that exists. To make a second VM, give a name:

```bash
scripts/atlas-dev.sh vm-002
```

`dev-vm/` has the same layout as `/var/lib/atlas` on a production host: `dist/`, `kernels/`, and `machines/<name>/`.

## Control the VM

Ctrl-C stops the `atlas-runtime` process. It does not stop the VM, and it does not remove the network. Use the API from a second terminal:

```bash
curl -X POST http://127.0.0.1:9101/stop
curl -X POST http://127.0.0.1:9101/start
```

The serial output also goes to `dev-vm/machines/<name>/console.log`. Read [api.md](api.md) for all the endpoints.
