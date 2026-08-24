# Local Development

This runs one throwaway VM for testing code changes, with no systemd
install and no `/var/lib/atlas`.

## Prerequisites

- Linux with `/dev/kvm`
- Root access (Firecracker networking needs `ip`, `nft`, and `ip netns`)

## Run a VM

From a checkout:

```bash
scripts/atlas-dev.sh
```

On first run this downloads the latest Firecracker release and a kernel, and
builds a test rootfs, all under `dev-vm/`, then creates a VM named `vm-001`
with a default `config.toml` and runs the runtime in the foreground.

Running it again resumes the same VM: it never overwrites an existing
kernel, image, or machine directory. Pass a name to create another VM:

```bash
scripts/atlas-dev.sh vm-002
```

`dev-vm/` uses the same layout as `/var/lib/atlas` in production:
`dist/`, `kernels/`, and `machines/<name>/`.

## Use the VM

The script starts the VM automatically once the API is up. Ctrl-C stops the
`atlas-runtime` process, not the VM or its network. In another shell:

```bash
curl -X POST http://127.0.0.1:9101/stop
curl -X POST http://127.0.0.1:9101/start
```

Serial output goes to `dev-vm/machines/<name>/console.log`. See
[api.md](api.md) for all endpoints.
