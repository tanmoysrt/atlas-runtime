package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// runScript feeds a script to a command on stdin, the way `nft -f -` and
// `ip -batch -` both want it.
func runScript(cmd *exec.Cmd, script string) {
	cmd.Stdin = strings.NewReader(script)
	cmd.Run()
}

// guestIP returns the single IPv4 address of a VM. A VM always has one
// address. A mask is accepted and ignored, so that older config files
// still load.
func guestIP(address string) net.IP {
	if ip, _, err := net.ParseCIDR(address); err == nil {
		return ip.To4()
	}
	return net.ParseIP(address).To4()
}

// guestIPv6 maps the VM address into the fd00:: range the guest uses.
// 10.10.1.30 becomes fd00::a0a:11e.
func guestIPv6(address string) string {
	ip4 := guestIP(address)
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("fd00::%02x%02x:%02x%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

// guestMAC builds the guest MAC from the VM address. The guest reads its own
// IPv4 back out of the MAC, so the two must always agree. Deriving the MAC
// keeps them in step.
func guestMAC(address string) string {
	ip4 := guestIP(address)
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

// fileLock is an advisory lock on a file path. It is used to coordinate
// between multiple atlas-runtime processes that may start VMs in the same
// VPC at the same time.
type fileLock struct {
	path string
	file *os.File
}

func newFileLock(path string) *fileLock {
	return &fileLock{path: path}
}

func (lock *fileLock) lock() error {
	file, err := os.OpenFile(lock.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	lock.file = file
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func (lock *fileLock) unlock() {
	if lock.file != nil {
		syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
		lock.file.Close()
	}
}

// writeFileAtomic writes data through a temporary file, then renames it over
// path. It keeps the mode of the file that it replaces.
func writeFileAtomic(path string, data []byte) error {
	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())

	// os.CreateTemp makes a 0600 file, so restore the mode being replaced.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := tempFile.Chmod(mode); err != nil {
		tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return os.Rename(tempFile.Name(), path)
}

// fsyncDir makes a directory entry durable, so that a new file or a rename
// stays after a power loss.
func fsyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
