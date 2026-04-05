# Packer plugin for ARM images

This plugin lets you take an existing ARM image and modify it on your x86 machine.
It is optimized for the Raspberry Pi use case — MBR partition table, with the filesystem partition
being the last partition.

With this plugin, you can:

- Provision new ARM images from existing ones
- Use ARM binaries for provisioning (`apt-get install` for example)
- Resize the last partition in case you need more space than the default

## How it works

The plugin runs provisioners in a chroot environment. Binary execution is done using
`qemu-arm-static`, via `binfmt_misc`.

### Dependencies

The following must be installed on the host machine:

- `qemu-user-static` — executes ARM binaries in the chroot
- `losetup` — mounts the image (pre-installed on most Linux distributions)

```shell
# Debian/Ubuntu
sudo apt install qemu-user-static

# Fedora
sudo dnf install qemu-user-static

# Arch Linux
pacman -S qemu-arm-static
```

Other commands used (should already be installed): `mount`, `umount`, `cp`, `ls`, `chroot`.

To resize the filesystem:
- `e2fsck`
- `resize2fs`

To provide custom arguments to `qemu-arm-static` via `qemu_args`, `gcc` is required.

This plugin requires kernel support for `/proc/sys/fs/binfmt_misc`.

## Configuration

Provide an existing ARM image via `iso_url`. Zip-compressed images are supported (you can
point directly at official Raspberry Pi image downloads).

See [config.go](pkg/builder/config.go) for all configuration options, and the
[builder doc](docs/builders/arm-image.mdx) for the full reference.

*Note:* For arm64 images, set `qemu_binary` to `qemu-aarch64-static`.

## Building and Installing

```bash
just build          # build binary via goreleaser
just install-local  # build + install to local packer plugin directory
just test           # run unit tests
```

Requires [just](https://github.com/casey/just) and [goreleaser](https://goreleaser.com).

## Running

```shell
packer init .
PACKER_CONFIG_DIR=$HOME sudo -E $(which packer) build your-config.pkr.hcl
```

## Flashing

### Golang flasher

```shell
go build cmd/flasher/main.go
```

It will auto-detect most things and guide you with questions.

### dd

```shell
# find the identifier of the device you want to flash
diskutil list

# un-mount the disk
diskutil unmountDisk /dev/disk2

# flash the image
sudo dd bs=4m if=output-arm-image/image of=/dev/disk2

# eject the disk
diskutil eject /dev/disk2
```

## Cookbook

### Raspberry Pi Provisioners

#### Enable ssh

```json
{
  "type": "shell",
  "inline": ["touch /boot/ssh"]
}
```

#### Set WiFi password

```json
{
  "type": "shell",
  "inline": [
    "echo 'network={' >> /etc/wpa_supplicant/wpa_supplicant.conf",
    "echo '    ssid=\"{{user `wifi_name`}}\"' >> /etc/wpa_supplicant/wpa_supplicant.conf",
    "echo '    psk=\"{{user `wifi_password`}}\"' >> /etc/wpa_supplicant/wpa_supplicant.conf",
    "echo '}' >> /etc/wpa_supplicant/wpa_supplicant.conf"
  ]
}
```

#### Add ssh key to authorized keys, enable ssh, disable password login

```json
{
  "variables": {
    "ssh_key_src": "{{env `HOME`}}/.ssh/id_rsa.pub",
    "image_home_dir": "/home/pi"
  },
  "builders": [
    {
      "type": "arm-image",
      "iso_url": "https://downloads.raspberrypi.org/raspbian_lite/images/raspbian_lite-2017-12-01/2017-11-29-raspbian-stretch-lite.zip",
      "iso_checksum": "sha256:e942b70072f2e83c446b9de6f202eb8f9692c06e7d92c343361340cc016e0c9f"
    }
  ],
  "provisioners": [
    {
      "type": "shell",
      "inline": ["mkdir {{user `image_home_dir`}}/.ssh"]
    },
    {
      "type": "file",
      "source": "{{user `ssh_key_src`}}",
      "destination": "{{user `image_home_dir`}}/.ssh/authorized_keys"
    },
    {
      "type": "shell",
      "inline": ["touch /boot/ssh"]
    },
    {
      "type": "shell",
      "inline": [
        "sed '/PasswordAuthentication/d' -i /etc/ssh/sshd_config",
        "echo >> /etc/ssh/sshd_config",
        "echo 'PasswordAuthentication no' >> /etc/ssh/sshd_config"
      ]
    }
  ]
}
```
