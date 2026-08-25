#!/bin/bash
# atlas-dev.sh runs one throwaway VM for local development, under dev-vm/
# in the repo root: dist/, kernels/, and machines/<name>/, the same layout
# atlas-runtime uses in production under /var/lib/atlas.
#
# Usage: scripts/atlas-dev.sh [vm-name]
#
# On first run it downloads the latest Firecracker release and a Firecracker
# kernel, and builds a test Ubuntu rootfs. It never overwrites an existing
# machine directory, kernel, or image: running it again resumes. A name that
# has not been used before creates a new VM with the default config.
set -eu

VM_NAME="${1:-vm-001}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEV_DIR="$REPO_ROOT/dev-vm"
MACHINE_DIR="$DEV_DIR/machines/$VM_NAME"
IMAGE="$DEV_DIR/dist/ubuntu-24.04-atlas.ext4"
KERNEL="$DEV_DIR/kernels/vmlinux-5.10"
BINARY="$DEV_DIR/atlas-runtime"
FIRECRACKER_BIN="$DEV_DIR/firecracker/firecracker"

mkdir -p "$DEV_DIR/dist" "$DEV_DIR/kernels" "$MACHINE_DIR"

echo "Building atlas-runtime ..."
(cd "$REPO_ROOT" && go build -o "$BINARY" .)

download_firecracker() {
	if [ -f "$FIRECRACKER_BIN" ]; then
		return
	fi
	echo "No firecracker binary at $FIRECRACKER_BIN. Downloading latest release ..."
	arch=$(uname -m)
	case "$arch" in
		x86_64) fc_arch=x86_64 ;;
		aarch64) fc_arch=aarch64 ;;
		*) echo "unsupported arch: $arch" >&2; exit 1 ;;
	esac
	mkdir -p "$DEV_DIR/firecracker"
	asset_url=$(curl -fsSL "https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest" \
		| grep -Po '"browser_download_url":\s*"\K[^"]+' \
		| grep -- "-${fc_arch}.tgz$")
	version=$(basename "$asset_url" | sed -E "s/^firecracker-v([^-]+)-${fc_arch}\.tgz$/\1/")
	curl -fsSL "$asset_url" -o /tmp/fc-dev.tgz
	tar -C "$DEV_DIR/firecracker" -xzf /tmp/fc-dev.tgz "release-v${version}-${fc_arch}/firecracker-v${version}-${fc_arch}"
	mv "$DEV_DIR/firecracker/release-v${version}-${fc_arch}/firecracker-v${version}-${fc_arch}" "$FIRECRACKER_BIN"
	rm -rf "$DEV_DIR/firecracker/release-v${version}-${fc_arch}"
	echo "Downloaded firecracker v${version}"
}
download_firecracker

if [ ! -f "$KERNEL" ]; then
	echo "No kernel at $KERNEL. Downloading ..."
	arch=$(uname -m)
	case "$arch" in
		x86_64) kernel_arch=x86_64 ;;
		aarch64) kernel_arch=aarch64 ;;
		*) echo "unsupported arch: $arch" >&2; exit 1 ;;
	esac
	curl -fsSL -o "$KERNEL" \
		"https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/${kernel_arch}/vmlinux-5.10.223"
fi

if [ ! -f "$IMAGE" ]; then
	echo "No test image at $IMAGE. Building one (needs root) ..."
	(cd "$DEV_DIR" && sudo "$REPO_ROOT/scripts/build-image")
fi

if [ -f "$MACHINE_DIR/config.toml" ]; then
	echo "Resuming $VM_NAME from $MACHINE_DIR/config.toml"
else
	cat > "$MACHINE_DIR/config.toml" <<EOF
[runtime]
listen = "127.0.0.1:9101"

[resources]
cpus = 1
memory = 268435456

[boot]
image = "file://$IMAGE"
kernel = "file://$KERNEL"
cmdline = "console=ttyS0 reboot=k panic=1 pci=off"
hostname = "$VM_NAME"

[network]
vpc = 1
address = "10.10.1.20"
egress = "host"

[rootfs]
size = 2147483648

[cloud_init]
user_data = """
#!/bin/bash
echo 'root:toor' | chpasswd
"""
EOF
	echo "Created $VM_NAME at $MACHINE_DIR/config.toml"
fi

# The runtime rewrites config.toml with the keys indented under their table.
LISTEN=$(grep -Po '^\s*listen\s*=\s*"\K[^"]+' "$MACHINE_DIR/config.toml" || true)
if [ -z "$LISTEN" ]; then
	echo "no [runtime] listen address in $MACHINE_DIR/config.toml" >&2
	exit 1
fi
BASE_URL="http://$LISTEN"

echo
echo "  Dashboard and API: $BASE_URL"
echo
echo "Note it down: serial output takes over this terminal."
echo "Needs root. Ctrl-C stops the API, not the VM."
echo
printf 'Press Enter to start %s ... ' "$VM_NAME"
read -r _ || true
echo

sudo env PATH="$DEV_DIR/firecracker:$PATH" "$BINARY" --enable-dashboard "$MACHINE_DIR/config.toml" &
RUNTIME_PID=$!
trap 'kill "$RUNTIME_PID" 2>/dev/null' INT TERM

echo "Waiting for the API ..."
for _ in $(seq 1 50); do
	if ! kill -0 "$RUNTIME_PID" 2>/dev/null; then
		echo "atlas-runtime exited" >&2
		exit 1
	fi
	if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
		break
	fi
	sleep 0.2
done

echo "Starting $VM_NAME ..."
curl -fsS -X POST "$BASE_URL/start" >/dev/null || echo "POST /start failed" >&2

echo "Serial output (Ctrl-C stops):"
tail -f --retry "$MACHINE_DIR/console.log" &
TAIL_PID=$!
trap 'kill "$RUNTIME_PID" "$TAIL_PID" 2>/dev/null' INT TERM

wait "$RUNTIME_PID"
kill "$TAIL_PID" 2>/dev/null
