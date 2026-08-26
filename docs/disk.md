# Disk

Each VM has a `rootfs` file in its machine directory. Atlas makes this file when the VM starts for the first time. It is a copy of the boot image.

## How Atlas copies the image

Atlas asks the filesystem for a copy-on-write clone with the `FICLONE` ioctl.

- On XFS with `reflink=1`, and on Btrfs, the clone is immediate, and it uses almost no more disk space. The two files share their extents until one of them changes.
- On a filesystem that cannot do this, Atlas makes a normal copy instead. The copy keeps the holes of the source, because a rootfs is mostly empty space. A 2 GiB rootfs that holds 325 MB of data therefore costs 325 MB.

Atlas takes a snapshot of a VM with the same clone. On a filesystem that can do reflink, the snapshot is immediate and the VM keeps running. Without reflink, Atlas stops the VM for the length of the copy, then starts it again. The `live` field in the reply of `POST /snapshot` tells you which one happened.

The rootfs of the VM and the snapshots directory must be on the same filesystem, because a clone cannot cross a filesystem.

Do not change the rootfs file of a snapshot. A VM that starts from a snapshot makes its own clone of it.

## Size

Set the size of the rootfs with `rootfs.size` in `config.toml`. The value is in bytes.

The file has a fixed length, so the guest cannot use more space than this value.

You can make the rootfs larger later with `POST /rootfs`. You cannot make it smaller. If the VM runs, the guest grows its own filesystem, because a udev rule in the image watches for the change of capacity.

## Speed limits

Firecracker can limit the speed of the disk. Set the limits with `rootfs.bandwidth` and `rootfs.iops` in `config.toml`, or with `POST /rootfs`.

A value of `0` or less means no limit.
