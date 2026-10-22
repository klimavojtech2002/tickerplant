package version

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	withRev := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef0123"}}}
	cases := []struct {
		name     string
		injected string
		bi       *debug.BuildInfo
		ok       bool
		want     string
	}{
		{"injected wins", "v1.2.3", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true, "v1.2.3"},
		{"no build info", "dev", nil, false, "dev"},
		{"module version", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}, true, "v0.4.0"},
		{"devel module falls to the revision", "dev", withRev, true, "dev+0123456789ab"},
		{"nothing recorded", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "dev"},
		{"short revision kept whole", "dev", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}}, true, "dev+abc"},
	}
	for _, c := range cases {
		if got := resolve(c.injected, c.bi, c.ok); got != c.want {
			t.Errorf("%s: resolve = %q, want %q", c.name, got, c.want)
		}
	}
}

// Under "go test" nothing is injected, so String must be non-empty and never panic.
func TestStringNotEmpty(t *testing.T) {
	if String() == "" {
		t.Fatal("String() is empty")
	}
}
