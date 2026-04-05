# macOS + Podman Support

This document explains how the plugin builds ARM images on macOS using Podman,
and the specific problems that were solved to make it work.

## Background

Building ARM images requires Linux kernel features: `losetup` (loop devices),
`mount`, `chroot`, and optionally `binfmt_misc` (for QEMU cross-architecture
emulation). None of these exist on macOS.

The plugin solves this by running a privileged Podman container inside the
Podman VM. The container provides all the Linux tools, while the VM provides
the Linux kernel. The host macOS filesystem is shared into the container via
volume mounts so the image file is accessible.

## Architecture

```
macOS (host)
  └── Podman VM (Apple Hypervisor, Linux kernel)
        └── Privileged Container (ubuntu:26.04)
              ├── losetup     (creates loop devices)
              ├── kpartx      (creates partition device-mapper entries)
              ├── mount       (mounts partitions into chroot)
              ├── chroot      (enters the ARM filesystem)
              └── qemu-*      (only if cross-arch, skipped on arm64 Mac)
```

Packer runs on macOS. The plugin starts a Podman container and rewrites the
command wrapper so all subsequent shell commands execute inside the container
via `podman exec`.

## Problems and Solutions

### 1. Relative output paths don't resolve inside Podman

**Problem:** The `output_filename` in the Packer HCL (e.g. `output-arm-image/yavin.img`)
is a relative path. Packer plugins run as separate subprocesses, and `filepath.Abs`
correctly resolves this on the host. But the container has a different working
directory, so the resolved path must be absolute before any container commands
use it.

**Solution:** `builder.go` resolves `OutputFile` to an absolute path during
`Prepare()`. This happens early enough that all subsequent steps (copy, losetup,
mount) use the absolute path.

**File:** `pkg/builder/builder.go`

### 2. `losetup -P` doesn't create partition devices inside containers

**Problem:** On native Linux, `losetup --show -f -P <image>` creates a loop device
AND scans it for partitions, producing `/dev/loop0p1`, `/dev/loop0p2`, etc.
Inside a Podman container on the Apple Hypervisor VM, losetup creates the loop
device but the kernel does not populate partition device nodes in the container's
`/dev`. The build would wait 60 seconds scanning `/dev/` and never find partitions.

**Root cause:** The Podman VM kernel creates partition devices in the VM's devtmpfs,
but the container's `/dev` (even in privileged mode) doesn't dynamically reflect
new device nodes created after container start.

**Solution:** In Podman mode, the plugin uses `kpartx` instead of `losetup -P`.
`kpartx` creates device-mapper entries at `/dev/mapper/loop0p1`,
`/dev/mapper/loop0p2`, etc. These are visible inside the container because
device-mapper operates through `/dev/mapper/` which is updated by the device-mapper
subsystem, not by devtmpfs.

The flow in Podman mode is:
1. `losetup --show -f <image>` (no `-P`) — creates the loop device
2. `kpartx -av /dev/loop0` — creates `/dev/mapper/loop0p1`, `/dev/mapper/loop0p2`
3. `mount /dev/mapper/loop0p1 <mountpoint>` — works as expected

Cleanup reverses this:
1. `umount` all mountpoints
2. `kpartx -dv /dev/loop0` — removes device-mapper entries
3. `losetup -d /dev/loop0` — detaches the loop device

The native Linux path is unchanged — it still uses `losetup -P` and
`/dev/loopNpN` device nodes.

**Files:** `pkg/builder/step_map_image.go`, `pkg/builder/step_setup_podman.go`

### 3. `kpartx` not installed in container

**Problem:** The container base image (`ubuntu:26.04`) includes `losetup` but not
`kpartx`. The `InstallPackages` method had an optimization: skip installation if
`losetup` is already present. This caused `kpartx` to never be installed.

**Solution:** `InstallPackages` now checks for ALL required binaries (losetup,
resize2fs, kpartx, fuser) before deciding to skip. If any binary is missing,
it runs `apt-get install`.

**File:** `pkg/builder/podman.go`

### 4. `resolv.conf` is a dangling symlink in Ubuntu chroot

**Problem:** Ubuntu's `/etc/resolv.conf` is a symlink to
`/run/systemd/resolve/stub-resolv.conf`. In the chroot, systemd-resolved isn't
running, so this symlink dangles. The `copy-host` resolv-conf mode runs
`cp /etc/resolv.conf <chroot>/etc/resolv.conf`, and `cp` refuses to write
through a dangling symlink.

**Solution:** In Podman mode, `rm -f` the destination before `cp`. This removes
the dangling symlink so `cp` can create a regular file.

**File:** `pkg/builder/step_handle_resolv_conf.go`

### 5. Cleanup errors abort a successful build

**Problem:** The `run()` helper always calls `state.Put("error", err)` on
failure. During cleanup, operations like `umount` or `kpartx -dv` can fail
(e.g. `/sys` is busy). These cleanup failures set an error in state, which
causes the builder's `Run()` method to report the build as failed — even though
all provisioning completed successfully.

**Solution:** Added `runCleanup()`, a variant of `run()` that logs errors but
does not set them in state. All cleanup methods (`stepMountImage.Cleanup`,
`StepMountCleanup.Cleanup`, `stepMapImage.Cleanup`) now use `runCleanup()`.

Additionally, `stepMountImage.Cleanup` now uses `umount -l` (lazy unmount) so
busy mounts are detached asynchronously rather than failing.

**File:** `pkg/builder/util.go`, `pkg/builder/step_mount_image.go`,
`pkg/builder/step_mount_cleanup.go`, `pkg/builder/step_map_image.go`

### 6. Stale loop devices from failed builds

**Problem:** When a build fails partway through, the loop device cleanup may
not run (e.g. if partition scanning failed, the partitions were never stored in
state, so `Cleanup` had nothing to detach). Subsequent builds find all loop
devices occupied and fail.

**Solution:** Two fixes:
- `stepMapImage` stores the loop device path in state (`loop_device` key)
  immediately after `losetup` succeeds, before partition scanning. This ensures
  `Cleanup` can always detach it.
- `stepSetupPodman` runs `losetup -D` inside the container on startup to detach
  any stale loop devices from previous runs.

**Files:** `pkg/builder/step_map_image.go`, `pkg/builder/step_setup_podman.go`

## Known Limitations

- **`/sys` busy during cleanup:** `umount /sys` inside the chroot sometimes
  fails because kernel threads hold references. The lazy unmount (`-l`) works
  around this, but the warning still appears in the log. This is cosmetic.

- **Stale loop devices in the VM:** If the Podman container is destroyed before
  loop device cleanup (e.g. the process is killed), loop devices persist in the
  VM. Run `podman machine ssh podman-machine-default "sudo losetup -D"` to
  clean them up manually.

- **goreleaser and `go build`:** The plugin's `justfile` uses `go build`
  directly for `install-local` instead of goreleaser. goreleaser with `-s -w`
  ldflags was not picking up source changes reliably. The `build-release` target
  still uses goreleaser for releases.
