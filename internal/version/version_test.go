package version

import (
	"runtime"
	"strings"
	"testing"
)

// Get must always return a usable Info, even with no ldflags and no VCS data,
// so `--version` never prints empty fields.
func TestGetFillsDefaults(t *testing.T) {
	info := Get()
	if info.Version == "" || info.Commit == "" || info.Date == "" {
		t.Fatalf("empty field in %+v", info)
	}
	if info.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	want := runtime.GOOS + "/" + runtime.GOARCH
	if info.Platform != want {
		t.Errorf("Platform = %q, want %q", info.Platform, want)
	}
}

// String is the single-line form shown by `dirhop --version`.
func TestInfoString(t *testing.T) {
	s := Info{Version: "v1.2.3", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"}.String()
	for _, want := range []string{"dirhop", "v1.2.3", "abc1234", "2026-01-02T03:04:05Z"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

// Explicit ldflags-style values must win over build-info fallback.
func TestGetPrefersInjectedValues(t *testing.T) {
	version, commit, date = "v9.9.9", "deadbee", "2020-01-01T00:00:00Z"
	t.Cleanup(func() { version, commit, date = "", "", "" })
	info := Get()
	if info.Version != "v9.9.9" || info.Commit != "deadbee" || info.Date != "2020-01-01T00:00:00Z" {
		t.Fatalf("injected values not used: %+v", info)
	}
}
