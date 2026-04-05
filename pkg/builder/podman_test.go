package builder

import "testing"

func TestExtractPartNum(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{"/dev/loop0p1", 1},
		{"/dev/loop0p2", 2},
		{"/dev/mapper/loop0p1", 1},
		{"/dev/mapper/loop0p12", 12},
		{"/dev/loop0", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := extractPartNum(tt.path); got != tt.want {
			t.Errorf("extractPartNum(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestBuildRegisterString(t *testing.T) {
	tests := []struct {
		name string
		qemu string
	}{
		{"test-arm", "/usr/bin/qemu-arm-static"},
		{"test-aarch64", "/usr/bin/qemu-aarch64-static"},
	}
	for _, tt := range tests {
		result := buildRegisterString(tt.name, tt.qemu)
		if len(result) == 0 {
			t.Errorf("buildRegisterString(%q, %q) returned empty", tt.name, tt.qemu)
		}
		// Should start with :<name>:
		if result[0] != ':' {
			t.Errorf("buildRegisterString should start with ':', got %q", result[0])
		}
		// Should end with :<qemu>:
		suffix := []byte(":" + tt.qemu + ":")
		got := result[len(result)-len(suffix):]
		if string(got) != string(suffix) {
			t.Errorf("buildRegisterString should end with %q, got %q", string(suffix), string(got))
		}
	}

	// 64-bit vs 32-bit should produce different magic bytes
	arm := buildRegisterString("test", "/usr/bin/qemu-arm-static")
	arm64 := buildRegisterString("test", "/usr/bin/qemu-aarch64-static")
	if string(arm) == string(arm64) {
		t.Error("arm and arm64 register strings should differ")
	}
}
