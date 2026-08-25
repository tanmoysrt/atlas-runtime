#!/bin/bash
# setup.sh installs Atlas-runtime on a bare metal host.
#
# Usage: setup.sh [--with-image] [repo-url]
#
#   --with-image   Also build the guest rootfs. This needs debootstrap and
#                  takes some minutes.
#   repo-url       Clone this repository. Give a URL that this host can read.
#                  If you run the script inside a checkout, it uses that
#                  checkout and does not clone.
#
# Run the script again at any time. It keeps what is already correct.

set -eu

REPO_URL=""
WITH_IMAGE=0
INSTALL_DIR=/opt/atlas-runtime
BIN=/usr/local/bin/atlas-runtime

# Firecracker 1.7.0 boots the 5.10 kernel with "pci=off". Newer releases
# change the virtio transport and the guest then finds no root device.
FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-1.7.0}"
KERNEL_URL_BASE="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10"

for arg in "$@"; do
	case "$arg" in
		--with-image) WITH_IMAGE=1 ;;
		-*) echo "unknown option: $arg" >&2; exit 1 ;;
		*) REPO_URL="$arg" ;;
	esac
done

case "$(uname -m)" in
	x86_64) ARCH=x86_64; GOARCH=amd64 ;;
	aarch64) ARCH=aarch64; GOARCH=arm64 ;;
	*) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

check_host() {
	if [ "$(id -u)" -ne 0 ]; then
		echo "run as root" >&2
		exit 1
	fi
	if [ ! -d /run/systemd/system ]; then
		echo "this host does not use systemd" >&2
		exit 1
	fi
	if [ ! -e /dev/kvm ]; then
		echo "no /dev/kvm. Turn on virtualization in the firmware." >&2
		exit 1
	fi
}

install_packages() {
	if command -v dnf >/dev/null 2>&1; then
		dnf install -y git curl tar iproute nftables util-linux e2fsprogs
		[ "$WITH_IMAGE" -eq 1 ] && dnf install -y debootstrap
	elif command -v apt-get >/dev/null 2>&1; then
		apt-get update
		apt-get install -y git curl tar iproute2 nftables util-linux e2fsprogs
		[ "$WITH_IMAGE" -eq 1 ] && apt-get install -y debootstrap
	else
		echo "no dnf and no apt-get" >&2
		exit 1
	fi
	return 0
}

get_source() {
	# A checkout in the current directory wins, unless you give a URL.
	if [ -z "$REPO_URL" ] && [ -f ./go.mod ] && [ -d ./systemd ]; then
		INSTALL_DIR=$(pwd)
		echo "source: $INSTALL_DIR"
		return
	fi
	if [ -z "$REPO_URL" ]; then
		echo "no source: run this inside a checkout, or give a repo URL" >&2
		exit 1
	fi
	if [ -d "$INSTALL_DIR/.git" ]; then
		git -C "$INSTALL_DIR" pull --ff-only
	else
		rm -rf "$INSTALL_DIR"
		git clone "$REPO_URL" "$INSTALL_DIR"
	fi
	echo "source: $INSTALL_DIR"
}

install_go() {
	latest=$(curl -fsSL "https://go.dev/VERSION?m=text" | head -n1)
	if command -v go >/dev/null 2>&1 && [ "$(go version | awk '{print $3}')" = "$latest" ]; then
		return
	fi
	curl -fsSL "https://go.dev/dl/${latest}.linux-${GOARCH}.tar.gz" -o /tmp/go.tgz
	rm -rf /usr/local/go
	tar -C /usr/local -xzf /tmp/go.tgz
	rm -f /tmp/go.tgz
	ln -sf /usr/local/go/bin/go /usr/local/bin/go
	ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
	echo "go: $latest"
}

install_firecracker() {
	want="v${FIRECRACKER_VERSION}"
	if command -v firecracker >/dev/null 2>&1 && firecracker --version | head -n1 | grep -q "$want"; then
		echo "firecracker: $want"
		return
	fi
	url="https://github.com/firecracker-microvm/firecracker/releases/download/${want}/firecracker-${want}-${ARCH}.tgz"
	curl -fsSL "$url" -o /tmp/fc.tgz
	tar -C /tmp -xzf /tmp/fc.tgz "release-${want}-${ARCH}/firecracker-${want}-${ARCH}"
	install -m 0755 "/tmp/release-${want}-${ARCH}/firecracker-${want}-${ARCH}" /usr/local/bin/firecracker
	rm -rf "/tmp/release-${want}-${ARCH}" /tmp/fc.tgz
	echo "firecracker: $want"
}

install_kernel() {
	kernel=/var/lib/atlas/kernels/vmlinux-5.10
	if [ -f "$kernel" ]; then
		return
	fi
	curl -fsSL "${KERNEL_URL_BASE}/${ARCH}/vmlinux-5.10.223" -o "$kernel"
	echo "kernel: $kernel"
}

build_runtime() {
	cd "$INSTALL_DIR"
	export PATH=/usr/local/go/bin:$PATH
	export CGO_ENABLED=0
	go build -trimpath -ldflags="-s -w" -o "$BIN" .
	echo "runtime: $BIN"
}

install_systemd() {
	install -m 0644 "$INSTALL_DIR"/systemd/atlas-vm@.service /usr/lib/systemd/system/
	install -m 0644 "$INSTALL_DIR"/systemd/atlas-vms.target /usr/lib/systemd/system/
	install -m 0755 "$INSTALL_DIR"/systemd/atlas-generator /usr/lib/systemd/system-generators/atlas-generator
	systemctl daemon-reload
	systemctl enable atlas-vms.target >/dev/null
	echo "systemd: atlas-vm@.service, atlas-vms.target"
}

build_image() {
	image=/var/lib/atlas/dist/ubuntu-24.04-atlas.ext4
	if [ -f "$image" ]; then
		echo "image: $image"
		return
	fi
	(cd /var/lib/atlas && "$INSTALL_DIR/scripts/build-ubuntu-image")
	echo "image: $image"
}

check_host
install_packages
get_source
mkdir -p /var/lib/atlas/machines /var/lib/atlas/images /var/lib/atlas/kernels \
	/var/lib/atlas/snapshots /var/lib/atlas/dist
install_go
install_firecracker
install_kernel
build_runtime
install_systemd
[ "$WITH_IMAGE" -eq 1 ] && build_image

echo
echo "Atlas-runtime is ready."
echo
if [ "$WITH_IMAGE" -eq 0 ]; then
	echo "Build a guest image:"
	echo "  cd /var/lib/atlas && $INSTALL_DIR/scripts/build-ubuntu-image"
	echo
fi
echo "Then make a VM:"
echo "  mkdir -p /var/lib/atlas/machines/vm-001"
echo "  \$EDITOR /var/lib/atlas/machines/vm-001/config.toml"
echo "  systemctl enable --now atlas-vm@vm-001"
echo "  curl -X POST http://127.0.0.1:9101/start"
