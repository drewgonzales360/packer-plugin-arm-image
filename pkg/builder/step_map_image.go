package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
)

var (
	loopRe = regexp.MustCompile("/dev/loop[0-9]+")
)

type stepMapImage struct {
	ImageKey  string
	ResultKey string
}

func (s *stepMapImage) Run(_ context.Context, state multistep.StateBag) multistep.StepAction {
	// Read our value and assert that it is the type we want
	image := state.Get(s.ImageKey).(string)
	ui := state.Get("ui").(packer.Ui)

	// Resolve to absolute path so the image is found inside Podman containers
	// (which may have a different working directory).
	if absImage, err := filepath.Abs(image); err == nil {
		image = absImage
	}

	ui.Message(fmt.Sprintf("mapping %s", image))

	podman := getPodmanEnv(state)

	// Create loopback device (without -P; we use kpartx for partitions in Podman mode).
	var out []byte
	var err error

	if podman != nil {
		ui.Say(fmt.Sprintf("losetup --show -f %s", image))
		out, err = podman.Exec("losetup", "--show", "-f", image)
	} else {
		ui.Say(fmt.Sprintf("losetup --show -f -P %s", image))
		out, err = exec.Command("losetup", "--show", "-f", "-P", image).CombinedOutput()
	}
	if err != nil {
		ui.Error(fmt.Sprintf("error losetup: %v: %s", err, string(out)))
		s.Cleanup(state)
		return multistep.ActionHalt
	}
	loopDev := strings.TrimSpace(string(out))
	loop := strings.Split(loopDev, "/")[2] // e.g. "loop0"
	state.Put("loop_device", loopDev)

	var partitions []string

	if podman != nil {
		// Use kpartx to create device-mapper partition mappings. The Podman VM
		// kernel doesn't create /dev/loopNpM nodes from containers, but kpartx
		// creates /dev/mapper/loopNpM entries via device-mapper.
		ui.Say(fmt.Sprintf("kpartx -av %s", loopDev))
		out, err = podman.Exec("kpartx", "-av", loopDev)
		if err != nil {
			ui.Error(fmt.Sprintf("error kpartx: %v: %s", err, string(out)))
			s.Cleanup(state)
			return multistep.ActionHalt
		}
		state.Put("used_kpartx", true)

		// Parse kpartx output: "add map loop0p1 (252:0): 0 1048576 linear 7:0 2048"
		prefix := loop + "p"
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "add map "+prefix) {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					partitions = append(partitions, "/dev/mapper/"+fields[2])
				}
			}
		}
	} else {
		time.Sleep(2 * time.Second)
		prefix := loop + "p"
		partitions, err = s.findPartitionsNative(prefix, ui)
		if err != nil {
			ui.Error(err.Error())
			s.Cleanup(state)
			return multistep.ActionHalt
		}
	}

	if len(partitions) == 0 {
		ui.Error("no partitions found")
		s.Cleanup(state)
		return multistep.ActionHalt
	}

	// Sort partitions by number.
	sort.Slice(partitions, func(i, j int) bool {
		numI := extractPartNum(partitions[i])
		numJ := extractPartNum(partitions[j])
		return numI < numJ
	})

	ui.Say(fmt.Sprintf("partitions: %v", partitions))
	state.Put(s.ResultKey, partitions)

	return multistep.ActionContinue
}

// extractPartNum extracts the partition number from paths like /dev/loop0p2 or /dev/mapper/loop0p2.
func extractPartNum(path string) int {
	re := regexp.MustCompile(`p(\d+)$`)
	m := re.FindStringSubmatch(path)
	if len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// findPartitionsNative scans /dev/ on the host for partition devices.
func (s *stepMapImage) findPartitionsNative(prefix string, ui packer.Ui) ([]string, error) {
	var partitions []string
	cPartitions := make(chan []string)
	cErr := make(chan error)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				files, err := os.ReadDir("/dev/")
				if err != nil {
					cErr <- fmt.Errorf("couldn't list devices in /dev/")
					return
				}

				var found []string
				for _, file := range files {
					if strings.HasPrefix(file.Name(), prefix) {
						found = append(found, "/dev/"+file.Name())
					}
				}

				if len(found) > 0 {
					cPartitions <- found
					return
				}
			}
		}
	}()

	select {
	case err := <-cErr:
		return nil, err
	case partitions = <-cPartitions:
		return partitions, nil
	case <-ctx.Done():
		return nil, nil
	}
}

func (s *stepMapImage) Cleanup(state multistep.StateBag) {
	loopDev := ""
	if ld, ok := state.GetOk("loop_device"); ok {
		loopDev = ld.(string)
	}
	if loopDev == "" {
		// Fall back to extracting from partitions.
		switch partitions := state.Get(s.ResultKey).(type) {
		case []string:
			if len(partitions) > 0 {
				if match := loopRe.Find([]byte(partitions[0])); match != nil {
					loopDev = string(match)
				}
			}
		}
	}
	if loopDev == "" {
		return
	}

	// Remove kpartx mappings before detaching the loop device.
	if _, ok := state.GetOk("used_kpartx"); ok {
		runCleanup(context.TODO(), state, fmt.Sprintf("kpartx -dv %s", loopDev))
	}
	runCleanup(context.TODO(), state, fmt.Sprintf("losetup -d %s", loopDev))
}
