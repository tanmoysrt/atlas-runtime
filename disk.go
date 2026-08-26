package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// PrepareRootfs clones a source image or snapshot to the destination, then
// expands it to the given size.
func PrepareRootfs(source, destination string, size int64) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if info.Size() > size {
		return fmt.Errorf("rootfs.size %d is smaller than the source %d", size, info.Size())
	}

	if err := cloneFile(source, destination); err != nil {
		if err := copyFile(source, destination); err != nil {
			return fmt.Errorf("copy rootfs: %w", err)
		}
	}
	return GrowRootfs(destination, size, true)
}

// GrowRootfs expands a rootfs file, and the filesystem inside it when
// resizeFilesystem is set. Only an offline disk can be resized from the host.
func GrowRootfs(path string, size int64, resizeFilesystem bool) error {
	// A missing file is the pre-boot case: initialize builds it at the new size.
	if fileInfo, err := os.Stat(path); err != nil || size <= fileInfo.Size() {
		return nil
	}

	if err := exec.Command("truncate", "-s", fmt.Sprintf("%d", size), path).Run(); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	if !resizeFilesystem {
		return nil
	}
	// e2fsck fixes unclean state before resize. Non-zero exit on corrected errors is safe to ignore.
	exec.Command("e2fsck", "-f", "-y", path).Run()
	if err := exec.Command("resize2fs", "-f", path).Run(); err != nil {
		return fmt.Errorf("resize2fs: %w", err)
	}
	return nil
}

// ficlone is FICLONE from linux/fs.h, that is _IOW(0x94, 9, int). The value
// is the same on amd64 and on arm64.
const ficlone = 0x40049409

// cloneFile makes a copy-on-write clone of source at destination. The clone is
// immediate, and it uses almost no more disk space. It returns an error if the
// filesystem cannot do reflink. The destination must not exist.
func cloneFile(source, destination string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, destinationFile.Fd(), ficlone, sourceFile.Fd())
	if errno != 0 {
		os.Remove(destination)
		return errno
	}
	return destinationFile.Sync()
}

// cloneUnsupported reports whether a clone failed because the filesystem
// cannot do reflink, and not because of a real error such as a full disk.
func cloneUnsupported(err error) bool {
	return errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTTY) ||
		errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EINVAL)
}

// copyFile copies source to destination, keeps the holes of a sparse file, and
// makes the result durable. The destination must not exist.
func copyFile(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("destination exists: %s", destination)
	}

	// cp finds the holes of the source exactly. A rootfs is mostly empty
	// space, so a copy that writes the zeros costs the full size on disk.
	if output, err := exec.Command("cp", "--sparse=always", source, destination).CombinedOutput(); err != nil {
		os.Remove(destination)
		return fmt.Errorf("cp: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return syncFile(destination)
}

// syncFile makes the contents of a file durable.
func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
