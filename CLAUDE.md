# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A HashiCorp Packer plugin that modifies existing ARM images (e.g. Raspberry Pi) on x86 machines. It mounts images via loopback devices, enters a chroot environment, and uses `qemu-user-static` + `binfmt_misc` to execute ARM binaries for provisioning. Requires root/privileged access for low-level device operations.

`qemu-user-static` must be installed on the host — there is no embedded fallback.

## Build & Test Commands

```bash
just build              # build via goreleaser (single target, current platform) → ./packer-plugin-arm-image
just install-local      # build + install via packer plugins install
just test               # go test -race ./... -timeout=3m
just testacc            # acceptance tests (PACKER_ACC=1, 120m timeout)
just testacc-sudo       # acceptance tests with sudo
just plugin-check       # validates plugin with packer-sdc
just check-generated    # verifies go generate + tidy are up to date
just release-snapshot   # local goreleaser snapshot (no publish)
```

Run a single test: `go test -race -count 1 ./pkg/utils/ -timeout=3m -run TestName`

After changing `config.go` or `flash.go` structs, run: `go generate ./...` to regenerate HCL2 spec files (`*_hcl2spec.go`). Note: `go generate` requires a compatible version of `packer-sdc` — if it fails, edit the `*_hcl2spec.go` files manually to match the struct changes.

## Architecture

**Plugin entry point** (`main.go`): Registers one builder (`arm-image`) and one post-processor (`arm-image` flasher) with the Packer plugin SDK.

**Builder** (`pkg/builder/`): Uses Packer's multistep pattern. The build pipeline:
1. `StepDownload` (SDK) - downloads the source image (reuses Packer's ISO downloader)
2. `stepCopyImage` - copies/decompresses image to output location
3. `stepResizeLastPart` - optionally resizes the last MBR partition
4. `stepMapImage` - maps image partitions via `losetup`
5. `stepResizeFs` - optionally resizes the filesystem (`e2fsck` + `resize2fs`)
6. `stepMountImage` - sorts partitions so root (`/`) mounts first, then `mkdir -p`s nested mount points (e.g. `/boot/firmware`) via the command wrapper before mounting sub-partitions
7. `StepMountExtra` (SDK) - mounts proc/sys/dev/devpts/binfmt_misc
8. `StepMountCleanup` - cleanup handler
9. `stepHandleResolvConf` - optionally copies/binds/deletes resolv.conf
10. `stepQemuUserStatic` - copies qemu binary into chroot
11. `stepRegisterBinFmt` - registers ARM binary format with kernel
12. `StepChrootProvision` (SDK) - runs Packer provisioners inside the chroot

**Config** (`pkg/builder/config.go`): Builder configuration struct with `mapstructure` tags. Uses `//go:generate` directives to produce `config.hcl2spec.go` via `packer-sdc`.

**Image handling** (`pkg/image/`): Detects and decompresses source images (zip, xz, gzip, bzip2). Uses native CLI tools (xzcat, zcat, bzcat) as a fast path with Go stdlib fallback. Known image types (RaspberryPi, BeagleBone, Kali, Ubuntu, Armbian) determine default mount points and qemu args.

**Post-processor** (`pkg/postprocessor/`): Flashes built images to SD cards. Standalone flasher CLI also available at `cmd/flasher/`.

**Utilities** (`pkg/utils/`): Device detection (finds removable block devices for flashing) and file copy helpers.

## Build System

`justfile` is the build system. goreleaser (`goreleaser build`) is the single place where Go build flags (ldflags, trimpath) are defined — `just build` and `just install-local` both use goreleaser to produce the binary.

Version is injected by goreleaser at build time via ldflags (`-X .../version.Version={{.Version}}`). The defaults in `version/version.go` are only a fallback for unbuilt binaries.

## Code Generation

Files ending in `_hcl2spec.go` are generated — do not edit manually under normal circumstances. After modifying a struct with a `//go:generate` directive, run `go generate ./...` then `go mod tidy`. If `go generate` fails due to `packer-sdc` incompatibility with the current Go version, edit the `_hcl2spec.go` file manually to match the struct change.
