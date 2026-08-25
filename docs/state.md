# VM State

Atlas keeps as little state as possible. `config.toml` is what you set. `metadata.json` is what Atlas remembers.

## Files in the machine directory

| File | What it holds |
| --- | --- |
| `config.toml` | Your configuration. This file is the authority. |
| `metadata.json` | The state of the machine: `instance_id`, `initialized`, `desired_state`, and `private_ip`. |
| `rootfs` | The disk of the VM. |
| `console.log` | The history of the serial console. Atlas only adds to this file. |
| `runtime/` | Temporary files: the Firecracker socket and the serial FIFOs. You can delete this directory at any time. |

## Files outside the machine directory

| Path | What it holds |
| --- | --- |
| `/var/lib/atlas/config.json` | The identity of the host, not of a VM: `node_id`, `gre_address`, and `beacon_endpoint`. Atlas reads it one time at start. The file is optional. Without it, the node runs alone. |
| `/run/atlas-vpc-<id>/` | Temporary VPC state: one marker file for each VM that runs, and the lock for the namespace. The last VM removes the directory. |

## What metadata.json answers

Atlas reads these answers from the file, and never from memory:

- Did this VM start for the first time already?
- Must this VM run after the host starts again?
- Which address and which hostname does it use?

## First start

The `[boot]` section is creation-time configuration. Atlas uses it one time:

```text
metadata.initialized = false
        │
        ▼
  read [boot]: an image or a snapshot
        │
        ▼
  make the rootfs, then set initialized = true
        │
        ▼
  each later start uses the rootfs that exists
```

After `initialized` becomes `true`, a change to `image`, `snapshot`, `kernel`, `rootfs.size`, or `hostname` has no result. To use a new value, make a new machine directory.

Atlas keeps the hostname at the first start. It uses `boot.hostname`, or the machine ID if `boot.hostname` is empty. No endpoint can change the hostname later.

## Desired state

`desired_state` is `running` or `stopped`. Atlas writes this value before it acts on it. A crash during a shutdown can therefore not cause a loop of restarts.

- The host starts again and `desired_state` is `running`: the VM starts.
- The host starts again and `desired_state` is `stopped`: the VM stays stopped.
- A reboot of the VM does not change `desired_state`.

## Recovery

systemd starts `atlas-runtime` again if the process stops. At start, the runtime reads `metadata.json`, and it starts the VM if `desired_state` is `running`.

The runtime does not watch Firecracker while it runs. If Firecracker stops, send `POST /start` to get the VM back.
