# Disk

The rootfs is a clone of the image. `PrepareRootfs` uses
`cp --reflink=auto`, which gives an instant copy-on-write clone on filesystems
that support it, such as XFS and Btrfs. If reflink is unavailable, it falls
back to a regular sparse copy.

On XFS, a project quota enforces `rootfs.size` at the filesystem level. On
other filesystems, quota setup is skipped silently and the VM still starts.

Snapshots copy the rootfs the same way, so they are instant and
space-efficient.

The Firecracker drive API can rate-limit bandwidth and IOPS. Set these with
`[rootfs]` in `config.toml`.