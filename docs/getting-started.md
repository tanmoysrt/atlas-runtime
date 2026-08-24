# Getting Started

This guide sets up Atlas-runtime on a Linux host and boots your first VM.

## Install

Requires Linux with systemd and root access.

The setup script installs Go, the network tools, and Firecracker. It then
clones the source, builds the binary, and installs the systemd units.

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/tanmoysrt/atlas-runtime/main/scripts/setup.sh)
```

The script clones `https://github.com/tanmoysrt/atlas-runtime` by default.
Pass another URL as an argument to use a different repository. Run
`sudo ./scripts/setup.sh` inside an existing checkout to skip the clone.

## Machine directory

Each VM lives in its own directory under `/var/lib/atlas/machines/`:

```text
/var/lib/atlas/machines/
└── vm-001/
    ├── config.toml
    ├── metadata.json
    ├── rootfs
    ├── console.log
    └── runtime/
```

The runtime reads `config.toml` from this directory. All persistent state
stays here; `runtime/` is disposable.

## Create a VM

Create the machine directory and write a `config.toml`:

```bash
mkdir -p /var/lib/atlas/machines/vm-001
```

```toml
[runtime]
listen = "127.0.0.1:9101"

[resources]
cpus = 2
memory = 2147483648   # bytes

[boot]
image = "file:///var/lib/atlas/images/alpine.rootfs"
kernel = "file:///var/lib/atlas/images/vmlinux"
cmdline = "console=ttyS0 reboot=k panic=1 pci=off"
hostname = "vm-001"

[network]
vpc = 1
cidr = "10.0.0.0/24"
address = "10.0.0.10/24"
mac = "06:00:ac:10:00:01"
egress = "host"
nameservers = ["1.1.1.1"]

[rootfs]
size = 8589934592   # bytes

[ssh]
authorized_keys = ["ssh-ed25519 AAAA..."]
```

`[boot]` is consumed only on the first start. After the VM is initialized,
changes to `image`, `snapshot`, `kernel`, or `hostname` have no effect. To
change them, create a new machine directory.

## Start the VM

The systemd generator scans `/var/lib/atlas/machines/*/config.toml` at boot
and creates one `atlas-vm@<name>.service` per machine. Start this VM:

```bash
systemctl enable --now atlas-vm@vm-001.service
```

systemd restarts `atlas-runtime` if it dies. On startup, the runtime reads
`metadata.json` and starts every VM whose `desired_state` is `running`.

## Use the API

The API listens on `127.0.0.1:9101` (the `[runtime] listen` value):

```bash
curl http://127.0.0.1:9101/health
curl -X POST http://127.0.0.1:9101/start
```

`POST /start` prepares the rootfs on first boot, creates the network, and
boots Firecracker. `POST /stop` shuts the VM down. See [api.md](api.md) for
all endpoints.

## Disk quotas (optional)

Quotas need the machine directory on an XFS filesystem. Mount a disk at
`/var/lib/atlas` and install `xfsprogs`:

```bash
mkfs.xfs /dev/sdb
mkdir -p /var/lib/atlas
mount /dev/sdb /var/lib/atlas
dnf install -y xfsprogs   # or: apt-get install -y xfsprogs
```

Without XFS, quota setup is skipped and the VM still starts.

## Next steps

- [state.md](state.md): VM state and lifecycle
- [network.md](network.md): VPC networking
- [api.md](api.md): HTTP API