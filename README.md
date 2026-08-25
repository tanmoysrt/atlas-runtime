# Atlas-runtime

Atlas-runtime runs Firecracker microVMs on a Linux host. There is one Go process for each VM, and systemd starts and stops them. There is no controller, no database, and no cluster manager.

Each VM gets one private address in a VPC. VMs of one VPC can speak to each other on the same host, or across hosts through a GRE tunnel. VMs of different VPCs cannot, even when they use the same addresses.

## Install

You need a Linux host with systemd, `/dev/kvm`, and root access.

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

Add `--with-image` to also build a guest image. This step needs `debootstrap` and takes some minutes:

```bash
sudo ./scripts/setup.sh --with-image
```

You can run the script again at any time. It keeps what is already correct.

## Start your first VM

Make a directory for the VM and write `config.toml` in it:

```bash
sudo mkdir -p /var/lib/atlas/machines/vm-001
```

```toml
[runtime]
listen = "127.0.0.1:9101"

[resources]
cpus = 2
memory = 2147483648

[boot]
image = "file:///var/lib/atlas/dist/ubuntu-24.04-atlas.ext4"
kernel = "file:///var/lib/atlas/kernels/vmlinux-5.10"
cmdline = "console=ttyS0 reboot=k panic=1 pci=off"
hostname = "vm-001"

[network]
vpc = 1
address = "10.0.0.10"
egress = "host"
nameservers = ["1.1.1.1"]

[rootfs]
size = 4294967296
```

Then start the runtime and the VM:

```bash
sudo systemctl enable --now atlas-vm@vm-001
curl -X POST http://127.0.0.1:9101/start
```

The systemd generator finds each new machine directory at boot, so a VM starts again after the host starts.

## Try it without an install

`scripts/atlas-dev.sh` runs one VM from the checkout. It uses `dev-vm/` and does not touch `/var/lib/atlas`:

```bash
sudo scripts/atlas-dev.sh
```

## Documents

- [Getting started](docs/getting-started.md) tells you how to install the runtime and start your first VM.
- [Architecture](docs/architecture.md) shows the design and the file layout.
- [State](docs/state.md) tells you what Atlas keeps on disk, and how a VM starts and stops.
- [Network](docs/network.md) explains VPCs, addresses, and traffic between nodes.
- [Disk](docs/disk.md) explains the rootfs and the disk limits.
- [API](docs/api.md) lists the HTTP endpoints.

## License

[GNU Affero General Public License v3.0](LICENSE)
