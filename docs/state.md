# VM State

## Files

Persistent state lives in the machine directory only:

| File | Purpose |
| --- | --- |
| `config.toml` | User configuration. The persistent authority. |
| `metadata.json` | Machine state: `instance_id`, `initialized`, `desired_state`, `private_ip`. |
| `rootfs` | VM disk image. |
| `console.log` | Serial console history. Append-only. |
| `runtime/` | Disposable runtime state: Firecracker socket, serial FIFOs. |

`metadata.json` answers these questions, never in-memory state:

- Was this VM initialized?
- Should it run after a host reboot?
- What IP and hostname does it use?

## Boot

`[boot]` is creation-time configuration. It is consumed once:

```text
metadata.initialized = false
        │
        ▼
  consume [boot] (image or snapshot)
        │
        ▼
   create rootfs, set initialized=true
        │
        ▼
  normal start/stop/reboot uses existing rootfs
```

After `initialized=true`, changes to `image`, `snapshot`, `kernel`,
`rootfs.size`, or `hostname` have no effect. To apply them, create a new
machine directory.

The hostname is captured on the first run: `boot.hostname`, or the machine ID
if it is empty. No API endpoint can change it later.

## Desired state

`desired_state` is either `running` or `stopped`. The runtime saves it before
acting on it, so a crash during shutdown cannot cause a restart loop.

- Host reboot with `desired_state=running`: the VM starts again.
- Host reboot with `desired_state=stopped`: the VM stays stopped.
- A VM reboot does not change `desired_state`.

## Process recovery

systemd restarts `atlas-runtime` if it dies. On startup, the runtime reads
`metadata.json` and starts every VM whose `desired_state` is `running`.

The runtime does not watch Firecracker while it runs. If Firecracker dies,
call `POST /start` to bring the VM back.