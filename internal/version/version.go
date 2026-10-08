// Package version exposes build metadata for the dirhop binary.
//
// Values are injected at build time with -ldflags "-X" (see the Makefile and
// the release workflow). When they are not set - for example with
// `go install github.com/m1r3dk/dirhop/cmd/dirhop@latest` - they are recovered
// from the module's build information so the binary still reports a meaningful
// version, commit, and build date.
package version

import (
	"runtime"
	"runtime/debug"
)

// These are overridden via -ldflags at build time, e.g.
//
//	go build -ldflags "-X github.com/m1r3dk/dirhop/internal/version.version=v1.2.3"
var (
	version = ""
	commit  = ""
	date    = ""
)

// Info is a snapshot of the binary's build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the resolved build metadata, falling back to the embedded module
// build info when ldflags were not supplied.
func Get() Info {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" && info.Main.Version != "" {
			v = info.Main.Version
		}
		var vcsRevision, vcsTime string
		var vcsModified bool
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRevision = setting.Value
			case "vcs.time":
				vcsTime = setting.Value
			case "vcs.modified":
				vcsModified = setting.Value == "true"
			}
		}
		if c == "" && vcsRevision != "" {
			c = shorten(vcsRevision)
			if vcsModified {
				c += "-dirty"
			}
		}
		if d == "" && vcsTime != "" {
			d = vcsTime
		}
	}
	if v == "" {
		v = "dev"
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return Info{
		Version:   v,
		Commit:    c,
		Date:      d,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String returns the short, human-friendly version string.
func (i Info) String() string {
	return "dirhop " + i.Version + " (" + i.Commit + ", " + i.Date + ")"
}

func shorten(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
