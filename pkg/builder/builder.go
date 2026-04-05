package builder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/chroot"
	packer_common_common "github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	packer_common_commonsteps "github.com/hashicorp/packer-plugin-sdk/multistep/commonsteps"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"github.com/mitchellh/mapstructure"
	"github.com/solo-io/packer-plugin-arm-image/pkg/image"
	"github.com/solo-io/packer-plugin-arm-image/pkg/image/arch"
	"github.com/solo-io/packer-plugin-arm-image/pkg/image/utils"

	getter "github.com/hashicorp/go-getter/v2"
)

const BuilderId = "yuval-k.arm-image"

var (
	knownTypes = map[utils.KnownImageType][]string{
		utils.RaspberryPi: {"/boot", "/"},
		utils.BeagleBone:  {"/"},
		utils.Kali:        {"/root", "/"},
		utils.Ubuntu:      {"/boot/firmware", "/"},
		utils.Armbian:     {"/"},
	}
	knownArgs = map[utils.KnownImageType][]string{
		utils.BeagleBone: {"-cpu", "cortex-a8"},
	}

	knownQemu = map[arch.KnownArchType]string{
		arch.Arm:     "qemu-arm-static",
		arch.ArmBE:   "qemu-armeb-static",
		arch.Arm64:   "qemu-aarch64-static",
		arch.Arm64BE: "qemu-aarch64_be-static",
	}

	defaultBase = [][]string{
		{"proc", "proc", "/proc"},
		{"sysfs", "sysfs", "/sys"},
		{"bind", "/dev", "/dev"},
		{"devpts", "devpts", "/dev/pts"},
		{"binfmt_misc", "binfmt_misc", "/proc/sys/fs/binfmt_misc"},
	}
	resolvConfBindMount = []string{"bind", "/etc/resolv.conf", "/etc/resolv.conf"}

	defaultChrootTypes = map[utils.KnownImageType][][]string{
		utils.Unknown: defaultBase,
	}
)

type ResolvConfBehavior string

const (
	Off      ResolvConfBehavior = "off"
	CopyHost ResolvConfBehavior = "copy-host"
	BindHost ResolvConfBehavior = "bind-host"
	Delete   ResolvConfBehavior = "delete"
)

const ChrootKey = "mount_path"

var generatedDataKeys = map[string]string{
	ChrootKey: "MountPath",
}

type Builder struct {
	config    Config
	runner    *multistep.BasicRunner
	usePodman bool
}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) autoDetectType() utils.KnownImageType {
	if len(b.config.ISOUrls) < 1 {
		return ""
	}
	url := b.config.ISOUrls[0]
	return utils.GuessImageType(url)
}

func (b *Builder) ConfigSpec() hcldec.ObjectSpec {
	return b.config.FlatMapstructure().HCL2Spec()
}

func (b *Builder) Prepare(cfgs ...interface{}) ([]string, []string, error) {
	var md mapstructure.Metadata
	err := config.Decode(&b.config, &config.DecodeOpts{
		Metadata:           &md,
		PluginType:         BuilderId,
		Interpolate:        true,
		InterpolateContext: &b.config.ctx,
		InterpolateFilter:  &interpolate.RenderFilter{},
	}, cfgs...)
	if err != nil {
		return nil, nil, err
	}
	var errs *packer.MultiError
	var warnings []string
	isoWarnings, isoErrs := b.config.ISOConfig.Prepare(&b.config.ctx)
	warnings = append(warnings, isoWarnings...)
	errs = packer.MultiErrorAppend(errs, isoErrs...)

	if b.config.OutputFile == "" {
		if b.config.OutputDir != "" {
			warnings = append(warnings, "output_directory is deprecated, use output_filename instead.")
			b.config.OutputFile = filepath.Join(b.config.OutputDir, "image")
		} else {
			b.config.OutputFile = fmt.Sprintf("output-%s/image", b.config.PackerConfig.PackerBuildName)
		}
	}

	// Resolve OutputFile to an absolute path so that it works correctly
	// inside Podman containers which have a different working directory.
	if cwd, err := os.Getwd(); err == nil {
		log.Printf("[DEBUG] plugin CWD: %s, OutputFile before resolve: %s", cwd, b.config.OutputFile)
	}
	if abs, err := filepath.Abs(b.config.OutputFile); err == nil {
		log.Printf("[DEBUG] OutputFile resolved to: %s", abs)
		b.config.OutputFile = abs
	} else {
		log.Printf("[DEBUG] filepath.Abs failed: %v", err)
	}

	if b.config.LastPartitionExtraSize > 0 {
		warnings = append(warnings, "last_partition_extra_size is deprecated, use target_image_size to grow your image")
	}

	if b.config.ChrootMounts == nil {
		b.config.ChrootMounts = make([][]string, 0)
	}

	if len(b.config.ChrootMounts) == 0 {
		b.config.ChrootMounts = defaultChrootTypes[utils.Unknown]
		if imageDefaults, ok := defaultChrootTypes[b.config.ImageType]; ok {
			b.config.ChrootMounts = imageDefaults
		}
	}

	if len(b.config.AdditionalChrootMounts) > 0 {
		b.config.ChrootMounts = append(b.config.ChrootMounts, b.config.AdditionalChrootMounts...)
	}

	if b.config.ResolvConf == BindHost {
		b.config.ChrootMounts = append(b.config.ChrootMounts, resolvConfBindMount)
	}

	if b.config.CommandWrapper == "" {
		b.config.CommandWrapper = "{{.Command}}"
	}

	if b.config.ImageType == "" {
		// defaults...
		b.config.ImageType = b.autoDetectType()
	} else {
		if _, ok := knownTypes[b.config.ImageType]; !ok {

			var validvalues []utils.KnownImageType
			for k := range knownTypes {
				validvalues = append(validvalues, k)
			}
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("unknown image_type. must be one of: %v", validvalues))
			b.config.ImageType = ""
		}
	}
	if b.config.ImageType != "" {
		if len(b.config.ImageMounts) == 0 {
			b.config.ImageMounts = knownTypes[b.config.ImageType]
		}
		if len(b.config.QemuArgs) == 0 {
			b.config.QemuArgs = knownArgs[b.config.ImageType]
		}
		if len(b.config.QemuArgs) > 0 {
			// If the image requires custom qemu args or the user provided some, make sure we use qemu
			b.config.QemuRequired = true
		}
	}

	if len(b.config.ImageMounts) == 0 {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("no image mounts provided. Please set the image mounts or image type."))
	}

	if b.config.ImageArch == arch.Unknown {
		b.config.ImageArch = arch.Arm
	} else if !b.config.ImageArch.Valid() {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("unknown image_arch. must be one of: %v", arch.Values()))
		b.config.ImageArch = arch.Arm
	}

	// Detect whether we need a Podman container for the build environment.
	// On non-Linux hosts, Linux kernel features (losetup, mount, chroot,
	// binfmt_misc) are unavailable, so we run them inside a privileged container.
	// On Linux, Podman is not used even if podman_image is set -- native operations
	// are always preferred.
	b.usePodman = NeedsPodman()

	if b.config.PodmanImage != "" && !b.usePodman {
		warnings = append(warnings, "podman_image is set but ignored on Linux where native operations are used")
	}

	if b.usePodman {
		if _, err := exec.LookPath("podman"); err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf(
				"podman is required on non-Linux hosts: install podman, then run: podman machine init --rootful && podman machine start"))
		}
	}

	if b.config.QemuBinary == "" {
		b.config.QemuBinary = knownQemu[b.config.ImageArch]
	} else if b.config.QemuBinary != knownQemu[b.config.ImageArch] {
		// If the user provided a non-default qemu, make sure we use it
		b.config.QemuRequired = true
	}

	// Only validate the qemu binary on the host when we'll actually use it.
	// When using Podman, qemu is installed inside the container instead.
	// When the image arch is native, qemu is skipped entirely.
	needsQemu := !b.config.ImageArch.IsNative() || b.config.QemuRequired
	if needsQemu && !b.usePodman {
		path, err := exec.LookPath(b.config.QemuBinary)
		if err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("qemu binary %q not found in PATH: install qemu-user-static (e.g. apt install qemu-user-static)", b.config.QemuBinary))
		} else {
			if !strings.Contains(path, "qemu-") {
				warnings = append(warnings, "binary doesn't look like qemu-user")
			}
			b.config.QemuBinary = path
		}
	} else if needsQemu && b.usePodman {
		// When using Podman, use the standard path inside the container.
		b.config.QemuBinary = "/usr/bin/" + knownQemu[b.config.ImageArch]
	}

	log.Println("qemu path", b.config.QemuBinary)
	if errs != nil && len(errs.Errors) > 0 {
		return nil, warnings, errs
	}

	generatedData := make([]string, 0, len(generatedDataKeys))
	for _, v := range generatedDataKeys {
		generatedData = append(generatedData, v)
	}

	return generatedData, warnings, nil
}

type wrappedCommandTemplate struct {
	Command string
}

func init() {
	// HACK: go-getter automatically decompress, which hurts caching.
	// additionally, we use native binaries to decompress which is faster anyway.
	// disable decompressors:
	getter.Decompressors = map[string]getter.Decompressor{}
}

func (b *Builder) Run(ctx context.Context, ui packer.Ui, hook packer.Hook) (packer.Artifact, error) {
	ui.Say(fmt.Sprintf("Image type: %s", b.config.ImageType))

	wrappedCommand := func(command string) (string, error) {
		b.config.ctx.Data = &wrappedCommandTemplate{Command: command}
		return interpolate.Render(b.config.CommandWrapper, &b.config.ctx)
	}

	state := new(multistep.BasicStateBag)
	state.Put("config", &b.config)
	state.Put("debug", b.config.PackerDebug)
	state.Put("hook", hook)
	state.Put("ui", ui)
	state.Put("wrappedCommand", packer_common_common.CommandWrapper(wrappedCommand))

	steps := []multistep.Step{
		&packer_common_commonsteps.StepDownload{
			Checksum:    b.config.ISOChecksum,
			Description: "Image",
			ResultKey:   "iso_path",
			Url:         b.config.ISOUrls,
			Extension:   b.config.TargetExtension,
			TargetPath:  b.config.TargetPath,
		},
		&stepCopyImage{FromKey: "iso_path", ResultKey: "imagefile", ImageOpener: image.NewImageOpener(ui)},
	}

	// stepResizeLastPart runs BEFORE Podman because it only does host-level
	// file I/O (os.Stat, os.Truncate, MBR table editing) on the image file,
	// which lives on the host filesystem. stepResizeFs runs AFTER Podman
	// because it needs Linux tools (e2fsck, resize2fs) that are only
	// available inside the container on non-Linux hosts.
	if b.config.LastPartitionExtraSize > 0 || b.config.TargetImageSize > 0 {
		steps = append(steps,
			&stepResizeLastPart{FromKey: "imagefile"},
		)
	}

	// On non-Linux hosts, start a Podman container before any Linux-specific
	// operations. The container provides losetup, mount, chroot, etc.
	if b.usePodman {
		steps = append(steps, &stepSetupPodman{})
	}

	steps = append(steps,
		&stepMapImage{ImageKey: "imagefile", ResultKey: "partitions"},
	)
	if b.config.LastPartitionExtraSize > 0 || b.config.TargetImageSize > 0 {
		steps = append(steps,
			&stepResizeFs{PartitionsKey: "partitions"},
		)
	}

	steps = append(steps,
		&stepMountImage{
			PartitionsKey:    "partitions",
			ResultKey:        ChrootKey,
			MountPath:        b.config.MountPath,
			GeneratedDataKey: generatedDataKeys[ChrootKey],
		},
		&chroot.StepMountExtra{
			ChrootMounts: b.config.ChrootMounts,
		},
		&StepMountCleanup{},
	)

	if b.config.ResolvConf == CopyHost || b.config.ResolvConf == Delete {
		steps = append(steps,
			&stepHandleResolvConf{ChrootKey: ChrootKey, Delete: b.config.ResolvConf == Delete})
	}

	if !b.config.ImageArch.IsNative() || b.config.QemuRequired {
		steps = append(steps,
			&stepQemuUserStatic{ChrootKey: ChrootKey, PathToQemuInChrootKey: "qemuInChroot", Args: Args{Args: b.config.QemuArgs}},
			&stepRegisterBinFmt{QemuPathKey: "qemuInChroot", BinfmtName: "binfmt_name"},
		)
	}

	steps = append(steps,
		&chroot.StepChrootProvision{},
	)

	b.runner = &multistep.BasicRunner{Steps: steps}

	// Executes the steps
	b.runner.Run(ctx, state)

	if rawErr, ok := state.GetOk("error"); ok {
		return nil, rawErr.(error)
	}
	// check if it is ok
	_, canceled := state.GetOk(multistep.StateCancelled)
	_, halted := state.GetOk(multistep.StateHalted)
	if canceled || halted {
		return nil, errors.New("step canceled or halted")
	}

	return &Artifact{
		image:     state.Get("imagefile").(string),
		StateData: map[string]interface{}{"generated_data": state.Get("generated_data")},
	}, nil
}

type Artifact struct {
	image     string
	StateData map[string]interface{}
}

func (a *Artifact) BuilderId() string {
	return BuilderId
}

func (a *Artifact) Files() []string {
	return []string{a.image}
}

func (a *Artifact) Id() string {
	return ""
}

func (a *Artifact) String() string {
	return a.image
}

func (a *Artifact) State(name string) interface{} {
	return a.StateData[name]
}

func (a *Artifact) Destroy() error {
	return os.Remove(a.image)
}
