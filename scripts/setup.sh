#!/bin/bash
# setup.sh installs Atlas-runtime and its dependencies.
# Usage: setup.sh [repo-url]
# With a repo-url it clones the source first; run from a checkout to skip.

set -eu

REPO_URL="${1:-https://github.com/tanmoysrt/atlas-runtime}"
INSTALL_DIR=/opt/atlas-runtime

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root" >&2
	exit 1
fi

install_packages() {
	if command -v dnf >/dev/null 2>&1; then
		dnf install -y git curl tar iproute2 nftables util-linux coreutils e2fsprogs
	elif command -v apt-get >/dev/null 2>&1; then
		apt-get update
		apt-get install -y git curl tar iproute2 nftables util-linux coreutils e2fsprogs
	else
		echo "no supported package manager" >&2
		exit 1
	fi
}

get_source() {
	if [ -n "$REPO_URL" ]; then
		rm -rf "$INSTALL_DIR"
		git clone "$REPO_URL" "$INSTALL_DIR"
	elif [ -f ./go.mod ]; then
		INSTALL_DIR=$(pwd)
	else
		echo "usage: setup.sh [repo-url]" >&2
		exit 1
	fi
}

install_go() {
	latest=$(curl -fsSL "https://go.dev/VERSION?m=text" | head -n1)
	if command -v go >/dev/null 2>&1 && [ "$(go version | awk '{print $3}')" = "$latest" ]; then
		return
	fi

	arch=$(uname -m)
	case "$arch" in
		x86_64) arch=amd64 ;;
		aarch64) arch=arm64 ;;
		*) echo "unsupported arch: $arch" >&2; exit 1 ;;
	esac

	curl -fsSL "https://go.dev/dl/${latest}.linux-${arch}.tar.gz" -o /tmp/go.tgz
	rm -rf /usr/local/go
	tar -C /usr/local -xzf /tmp/go.tgz
	ln -sf /usr/local/go/bin/go /usr/local/bin/go
	ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
	echo "installed go $latest"
}

install_firecracker() {
	if command -v firecracker >/dev/null 2>&1; then
		return
	fi

	arch=$(uname -m)
	case "$arch" in
		x86_64) arch=x86_64 ;;
		aarch64) arch=aarch64 ;;
		*) echo "unsupported arch: $arch" >&2; exit 1 ;;
	esac

	asset_url=$(curl -fsSL "https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest" \
		| grep -Po '"browser_download_url":\s*"\K[^"]+' \
		| grep -- "-${arch}.tgz$")
	version=$(basename "$asset_url" | sed -E "s/^firecracker-v([^-]+)-${arch}\.tgz$/\1/")
	curl -fsSL "$asset_url" -o /tmp/fc.tgz
	tar -C /tmp -xzf /tmp/fc.tgz "release-v${version}-${arch}/firecracker-v${version}-${arch}"
	install -m 0755 "/tmp/release-v${version}-${arch}/firecracker-v${version}-${arch}" /usr/local/bin/firecracker
	rm -rf "/tmp/release-v${version}-${arch}"
	echo "installed firecracker v${version}"
}

build_install() {
	cd "$INSTALL_DIR"
	export PATH=/usr/local/go/bin:$PATH
	export CGO_ENABLED=0
	go build -trimpath -ldflags="-s -w" -o /usr/local/bin/atlas-runtime .
	echo "installed /usr/local/bin/atlas-runtime"
}

install_systemd() {
	install -m 0644 systemd/atlas-vm@.service systemd/atlas-vms.target /usr/lib/systemd/system/
	install -m 0755 systemd/atlas-generator /usr/lib/systemd/atlas-generator
	systemctl daemon-reload
	systemctl enable atlas-vms.target
	echo "installed systemd units"
}

install_packages
get_source
install_go
install_firecracker
build_install
install_systemd

mkdir -p /var/lib/atlas/machines /var/lib/atlas/images
echo "done"