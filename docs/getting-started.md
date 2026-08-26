# Getting Started

This guide installs Atlas-runtime on a Linux host and starts your first VM.

## Install

You need Linux with systemd, and root access.

```bash
git clone git@github.com:tanmoysrt/atlas-runtime.git
cd atlas-runtime
sudo ./scripts/setup.sh
```

The script does all of this:

1. It installs Go, Firecracker, and the network tools.
2. It builds `atlas-runtime` and puts it in `/usr/local/bin`.
3. It downloads a guest kernel to `/var/lib/atlas/kernels/`.
4. It installs the systemd units and the generator.
5. It makes the directories below `/var/lib/atlas/`.

Add `--with-image` to also build a guest image. This needs `debootstrap` and takes some minutes. Without the flag, build the image later:

```bash
cd /var/lib/atlas && /opt/atlas-runtime/scripts/build-ubuntu-image
```

The script uses the checkout that you are in. To clone a different repository instead, give its URL as an argument. You can run the script again at any time, because it keeps what is already correct.

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
egress_v4 = "host"
nameservers = ["1.1.1.1"]

[rootfs]
size = 8589934592   # bytes

[ssh]
authorized_keys = ["ssh-ed25519 AAAA..."]

[firewall]
[[firewall.ingress]]
protocol = "tcp"
port = 22
[[firewall.ingress]]
protocol = "icmp"
[[firewall.egress]]
protocol = "all"
```

Two notes on this file:

- Atlas reads `[boot]` only at the first start. After that, a change to `image`, `snapshot`, `kernel`, or `hostname` has no result. To use a new value, make a new machine directory.
- `network.address` is one address, and it needs no mask. Atlas makes the MAC of the guest from it.

### The firewall

Both directions are deny-by-default. With no `[firewall]` section, the guest cannot be reached and cannot reach out, except for DNS to the nameservers in `network.nameservers`. To reach the guest over SSH, add the `tcp/22` ingress rule above. `protocol = "all"` allows everything in that direction. Read [network.md](network.md) for the full rules.

## Start from a snapshot

`POST /snapshot` copies the rootfs of a running VM to `/var/lib/atlas/snapshots/<vm-id>/<snapshot-id>/`:

```bash
curl -X POST http://127.0.0.1:9101/snapshot
```

There is no restore endpoint. A snapshot is a boot source, so you start it as a new VM. Make a second machine directory, and use `snapshot` in place of `image`:

```toml
[boot]
snapshot = "vm-001/snap-k3f9x2mq7b"
kernel = "file:///var/lib/atlas/images/vmlinux"
cmdline = "console=ttyS0 reboot=k panic=1 pci=off"
hostname = "vm-002"
```

Give the new VM its own `listen` address and its own `network.address`, because the first VM keeps the ones that it has. `rootfs.size` must not be smaller than the rootfs of the snapshot. Read [disk.md](disk.md) for the snapshots.

A snapshot holds the disk only, and no memory. The new VM does a normal cold boot.

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

## The filesystem, optional

Atlas makes each rootfs and each snapshot as a copy-on-write clone. A clone is immediate, and it uses almost no more disk space. This needs XFS with `reflink=1`, or Btrfs. Mount such a disk at `/var/lib/atlas`:

```bash
dnf install -y xfsprogs   # or: apt-get install -y xfsprogs
mkfs.xfs /dev/sdb         # reflink=1 is the default
mkdir -p /var/lib/atlas
mount /dev/sdb /var/lib/atlas
```

On a filesystem that cannot do reflink, Atlas makes a normal copy instead. The VM still starts, but each new rootfs costs its own disk space, and a snapshot stops the VM for the length of the copy. Read [disk.md](disk.md).

## Next

- [state.md](state.md) tells you what Atlas keeps on disk.
- [network.md](network.md) explains the network.
- [api.md](api.md) lists the endpoints.
