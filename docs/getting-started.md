# Getting Started

This guide installs Atlas-runtime on a Linux host and starts your first VM.

## Install

You need Linux with systemd, and root access.

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/tanmoysrt/atlas-runtime/main/scripts/setup.sh)
```

The script does all of this:

1. It installs Go, the network tools, and Firecracker.
2. It clones the source and builds the binary.
3. It installs the systemd units.

The script clones `https://github.com/tanmoysrt/atlas-runtime`. To use a different repository, give the URL as an argument. If you already have a checkout, run `sudo ./scripts/setup.sh` inside it, and the script does not clone.

## The machine directory

Each VM has its own directory below `/var/lib/atlas/machines/`:

```text
/var/lib/atlas/machines/
└── vm-001/
    ├── config.toml
    ├── metadata.json
    ├── rootfs
    ├── console.log
    └── runtime/
```

The runtime reads `config.toml` from this directory. All the state that must stay is here. Only `runtime/` is temporary.

## Make a VM

Make the directory:

```bash
mkdir -p /var/lib/atlas/machines/vm-001
```

Then write `config.toml` in it:

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
address = "10.0.0.10"
egress = "host"
nameservers = ["1.1.1.1"]

[rootfs]
size = 8589934592   # bytes

[ssh]
authorized_keys = ["ssh-ed25519 AAAA..."]
```

Two notes on this file:

- Atlas reads `[boot]` only at the first start. After that, a change to `image`, `snapshot`, `kernel`, or `hostname` has no result. To use a new value, make a new machine directory.
- `network.address` is one address, and it needs no mask. Atlas makes the MAC of the guest from it.

## Run a script in the guest

Add a `[cloud_init]` section to give the guest a script. Atlas sends the text through MMDS, and the guest runs it at each start:

```toml
[cloud_init]
user_data = '''
#!/bin/bash
echo 'root:toor' | chpasswd
'''
```

The text must start with `#!`. If it does not, the guest reads it and does nothing. Use the `'''` quotes of TOML, because they keep a `\n` as two characters for the shell.

## Start the VM

At boot, the systemd generator reads each `/var/lib/atlas/machines/*/config.toml`. It makes one `atlas-vm@<name>.service` for each machine. Start this one:

```bash
systemctl enable --now atlas-vm@vm-001.service
```

The unit starts the runtime, but not the VM. Send `POST /start` to start the VM. Atlas then remembers this, and the VM starts again after a reboot of the host.

## Use the API

The API listens on the `[runtime] listen` address, `127.0.0.1:9101` here:

```bash
curl http://127.0.0.1:9101/health
curl -X POST http://127.0.0.1:9101/start
```

At the first `POST /start`, Atlas makes the rootfs, builds the network, and starts Firecracker. `POST /stop` stops the VM. Read [api.md](api.md) for all the endpoints.

## Disk quotas, optional

A quota needs the machine directory on an XFS filesystem. Mount a disk at `/var/lib/atlas` and install `xfsprogs`:

```bash
mkfs.xfs /dev/sdb
mkdir -p /var/lib/atlas
mount /dev/sdb /var/lib/atlas
dnf install -y xfsprogs   # or: apt-get install -y xfsprogs
```

Without XFS, Atlas does not make a quota, and the VM still starts.

## Next

- [state.md](state.md) tells you what Atlas keeps on disk.
- [network.md](network.md) explains the network.
- [api.md](api.md) lists the endpoints.
