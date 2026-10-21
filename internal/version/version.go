// Package version reports which build is running.
package version

import "runtime/debug"

// Version is set at link time (-ldflags "-X .../internal/version.Version=v1.2.3"). A plain
// "go build" leaves it as "dev".
var Version = "dev"

// String returns the injected version, or else what the Go toolchain recorded about the
// build (a module version, or the VCS revision), or else "dev".
func String() string {
	bi, ok := debug.ReadBuildInfo()
	return resolve(Version, bi, ok)
}

func resolve(injected string, bi *debug.BuildInfo, ok bool) string {
	if injected != "dev" {
		return injected
	}
	if !ok {
		return injected
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return "dev+" + rev
		}
	}
	return injected
}
