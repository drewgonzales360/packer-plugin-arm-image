# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A HashiCorp Packer plugin that modifies existing ARM images (e.g. Raspberry Pi) on x86 machines. It mounts images via loopback devices, enters a chroot environment, and uses `qemu-user-static` + `binfmt_misc` to execute ARM binaries for provisioning. Requires root/privileged access for low-level device operations.

## Build & Test Commands

```bash
make build              # go generate + go build (produces packer-plugin-arm-image binary)
make test               # go test -race ./... -timeout=3m
make testacc            # acceptance tests (PACKER_ACC=1, 120m timeout)
make testacc-sudo       # acceptance tests with sudo (compiles test binary, runs as root)
make plugin-check       # validates plugin with packer-sdc
make install-local      # builds and installs to ~/.packer.d/plugins/
make check-generated    # verifies go generate + vendor are up to date
```

Run a single test: `go test -race -count 1 ./pkg/utils/ -timeout=3m -run TestName`

After changing `config.go` or `flash.go` structs, run: `go generate ./...` to regenerate HCL2 spec files (`*_hcl2spec.go`).

## Architecture

**Plugin entry point** (`main.go`): Registers one builder (`arm-image`) and one post-processor (`arm-image` flasher) with the Packer plugin SDK.

**Builder** (`pkg/builder/`): Uses Packer's multistep pattern. The build pipeline is a sequence of `multistep.Step` implementations:
1. `StepDownload` (SDK) - downloads the source image (reuses Packer's ISO downloader)
2. `stepCopyImage` - copies/decompresses image to output location
3. `stepResizeLastPart` - optionally resizes the last MBR partition
4. `stepMapImage` - maps image partitions via `losetup`
5. `stepResizeFs` - optionally resizes the filesystem (`e2fsck` + `resize2fs`)
6. `stepMountImage` - mounts partitions into chroot path
7. `StepMountExtra` (SDK) - mounts proc/sys/dev/devpts/binfmt_misc
8. `StepMountCleanup` - cleanup handler
9. `stepHandleResolvConf` - optionally copies/binds/deletes resolv.conf
10. `stepQemuUserStatic` - copies qemu binary into chroot
11. `stepRegisterBinFmt` - registers ARM binary format with kernel
12. `StepChrootProvision` (SDK) - runs Packer provisioners inside the chroot

**Config** (`pkg/builder/config.go`): Builder configuration struct with `mapstructure` tags. Uses `//go:generate` directives to produce `config.hcl2spec.go` via `packer-sdc`.

**Image handling** (`pkg/image/`): Detects and decompresses source images (zip, xz, gzip, bzip2). Uses native CLI tools (xzcat, zcat, bzcat) as a fast path with Go stdlib fallback. Known image types (RaspberryPi, BeagleBone, Kali, Ubuntu, Armbian) determine default mount points and qemu args.

**Embedded qemu** (`pkg/builder/embed/`): Ships embedded `qemu-arm-static` and `qemu-aarch64-static` binaries so users don't need them pre-installed. Falls back to system-installed versions if available.

**Post-processor** (`pkg/postprocessor/`): Flashes built images to SD cards. Standalone flasher CLI also available at `cmd/flasher/`.

**Utilities** (`pkg/utils/`): Device detection (finds removable block devices for flashing) and file copy helpers.

## Code Generation

Files ending in `_hcl2spec.go` are generated - do not edit manually. After modifying any struct with a `//go:generate` directive, run `go generate ./...` then `go mod vendor && go mod tidy` to keep everything in sync.
