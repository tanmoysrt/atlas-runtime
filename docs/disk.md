# Disk

Each VM has a `rootfs` file in its machine directory. Atlas makes this file when the VM starts for the first time. It is a copy of the boot image.

## How Atlas copies the image

Atlas uses `cp --reflink=auto`. The result is different on each type of filesystem:

- On XFS and Btrfs, the copy is a copy-on-write clone. The clone is immediate, and it uses almost no more disk space.
- On other filesystems, the command makes a normal sparse copy.

A snapshot copies the rootfs with the same command. A snapshot is therefore also immediate.

## Size

Set the size of the rootfs with `rootfs.size` in `config.toml`. The value is in bytes.

On XFS, Atlas also makes a project quota. The quota holds the file to that size at the filesystem level. On other filesystems, Atlas does not make a quota. The VM still starts, but nothing holds the file to the size.

You can make the rootfs larger later with `POST /rootfs`. You cannot make it smaller. If the VM runs, the guest grows its own filesystem, because a udev rule in the image watches for the change of capacity.

## Speed limits

Firecracker can limit the speed of the disk. Set the limits with `rootfs.bandwidth` and `rootfs.iops` in `config.toml`, or with `POST /rootfs`.

A value of `0` or less means no limit.
