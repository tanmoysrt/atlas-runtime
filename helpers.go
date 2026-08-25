package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
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
