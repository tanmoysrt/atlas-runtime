package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PrepareRootfs reflink-clones a source image to the destination, then expands it to the given size.
// If the filesystem does not support reflink, it falls back to a regular sparse copy.
func PrepareRootfs(source, destination string, size int64) error {
	if err := reflinkCopy(source, destination); err != nil {
		return fmt.Errorf("reflink copy: %w", err)
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

// SetupProjectQuota initializes an XFS project quota for a VM directory.
// It assigns a deterministic project ID derived from the instance ID and sets a hard byte limit.
// If the underlying filesystem is not XFS, or xfs_quota is unavailable, this returns nil
// so that the VM can still start without quota enforcement.
func SetupProjectQuota(machineDirectory string, instanceID string, sizeBytes int64) error {
	mountPoint, ok := xfsMountPoint(machineDirectory)
	if !ok {
		return nil
	}

	projectID := projectIDFromInstance(instanceID)
	exec.Command("xfs_quota", "-x",
		"-c", fmt.Sprintf("project -s -p %s %d", machineDirectory, projectID),
		"-c", fmt.Sprintf("limit -p bhard=%d %d", sizeBytes, projectID),
		mountPoint,
	).Run()
	return nil
}

// RemoveProjectQuota clears the XFS project quota for a VM directory.
// If the underlying filesystem is not XFS, or xfs_quota is unavailable, this returns nil.
func RemoveProjectQuota(machineDirectory string, instanceID string) error {
	mountPoint, ok := xfsMountPoint(machineDirectory)
	if !ok {
		return nil
	}

	projectID := projectIDFromInstance(instanceID)
	exec.Command("xfs_quota", "-x",
		"-c", fmt.Sprintf("limit -p bhard=0 %d", projectID),
		mountPoint,
	).Run()
	return nil
}

// ReflinkSnapshot creates an instant, copy-on-write snapshot of a rootfs file.
// It uses XFS reflink; if unavailable, it falls back to a regular sparse copy.
func ReflinkSnapshot(source, destination string) error {
	if err := reflinkCopy(source, destination); err != nil {
		return fmt.Errorf("reflink snapshot: %w", err)
	}
	return nil
}

// reflinkCopy performs a copy-on-write clone via cp --reflink, falling back
// to a regular sparse copy if the filesystem does not support reflink.
func reflinkCopy(source, destination string) error {
	return exec.Command("cp", "--reflink=auto", "--sparse=always", source, destination).Run()
}

// projectIDFromInstance derives a deterministic numeric project ID from an instance ID string.
func projectIDFromInstance(instanceID string) uint32 {
	var hash uint32 = 5381
	for _, character := range instanceID {
		hash = ((hash << 5) + hash) + uint32(character)
	}
	if hash == 0 {
		hash = 1
	}
	return hash
}

// xfsMountPoint returns the mount point for path if it is on an XFS filesystem.
// ok is false if the mount point cannot be determined or is not XFS.
func xfsMountPoint(path string) (mountPoint string, ok bool) {
	output, err := exec.Command("findmnt", "-n", "-o", "TARGET,FSTYPE", "--target", path).Output()
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != "xfs" {
		return "", false
	}
	return fields[0], true
}
