package builder

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestMountOrderingSortsRootFirst(t *testing.T) {
	tests := []struct {
		name     string
		mounts   []string
		expected []string
	}{
		{
			name:     "ubuntu: /boot/firmware before /",
			mounts:   []string{"/boot/firmware", "/"},
			expected: []string{"/", "/boot/firmware"},
		},
		{
			name:     "raspberrypi: /boot before /",
			mounts:   []string{"/boot", "/"},
			expected: []string{"/", "/boot"},
		},
		{
			name:     "already correct order",
			mounts:   []string{"/", "/boot"},
			expected: []string{"/", "/boot"},
		},
		{
			name:     "single root",
			mounts:   []string{"/"},
			expected: []string{"/"},
		},
		{
			name:     "three partitions",
			mounts:   []string{"/boot/firmware", "/home", "/"},
			expected: []string{"/", "/boot/firmware", "/home"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted := make([]string, len(tt.mounts))
			copy(sorted, tt.mounts)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

			if len(sorted) != len(tt.expected) {
				t.Fatalf("length mismatch: got %v, want %v", sorted, tt.expected)
			}
			for i := range sorted {
				if sorted[i] != tt.expected[i] {
					t.Errorf("index %d: got %q, want %q (full result: %v)", i, sorted[i], tt.expected[i], sorted)
				}
			}
		})
	}
}

func TestMkdirAllCreatesMountPoints(t *testing.T) {
	// Simulate the scenario: root is "mounted" (represented by a temp dir),
	// and we need to create /boot/firmware inside it before mounting.
	tmpDir := t.TempDir()

	// The mount ordering after sort: ["/", "/boot/firmware"]
	mounts := []string{"/", "/boot/firmware"}

	for _, mnt := range mounts {
		mntpnt := filepath.Join(tmpDir, mnt)
		if err := os.MkdirAll(mntpnt, os.ModePerm); err != nil {
			t.Fatalf("MkdirAll(%q) failed: %v", mntpnt, err)
		}
	}

	// Verify /boot/firmware was created inside the "root"
	bootFirmware := filepath.Join(tmpDir, "boot", "firmware")
	info, err := os.Stat(bootFirmware)
	if err != nil {
		t.Fatalf("expected %q to exist: %v", bootFirmware, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", bootFirmware)
	}
}
