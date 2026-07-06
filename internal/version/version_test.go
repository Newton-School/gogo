package version

import (
	"runtime/debug"
	"testing"
)

func TestInfoUsesDefaultBuildMetadata(t *testing.T) {
	restore := setBuildMetadata("0.0.0-dev", "unknown", "unknown", nil)
	defer restore()

	got := Info()
	want := "gogo 0.0.0-dev (commit unknown, built unknown)"

	if got != want {
		t.Fatalf("Info() = %q, want %q", got, want)
	}
}

func TestInfoUsesOverriddenBuildMetadata(t *testing.T) {
	restore := setBuildMetadata("1.2.3", "abc123", "2026-06-27T00:00:00Z", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/Newton-School/gogo/cmd/gogo", Version: "v9.9.9"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "ignored"},
				{Key: "vcs.time", Value: "2026-06-28T00:00:00Z"},
			},
		}, true
	})
	defer restore()

	got := Info()
	want := "gogo 1.2.3 (commit abc123, built 2026-06-27T00:00:00Z)"

	if got != want {
		t.Fatalf("Info() = %q, want %q", got, want)
	}
}

func TestInfoUsesGoInstallModuleVersionWhenLdflagsAreAbsent(t *testing.T) {
	restore := setBuildMetadata("0.0.0-dev", "unknown", "unknown", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/Newton-School/gogo/cmd/gogo", Version: "v0.1.0"},
		}, true
	})
	defer restore()

	got := Info()
	want := "gogo 0.1.0 (commit unknown, built unknown)"

	if got != want {
		t.Fatalf("Info() = %q, want %q", got, want)
	}
}

func TestInfoUsesGoBuildVCSMetadataWhenLdflagsAreAbsent(t *testing.T) {
	restore := setBuildMetadata("0.0.0-dev", "unknown", "unknown", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/Newton-School/gogo/cmd/gogo", Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc123"},
				{Key: "vcs.time", Value: "2026-06-28T00:00:00Z"},
			},
		}, true
	})
	defer restore()

	got := Info()
	want := "gogo 0.0.0-dev (commit abc123, built 2026-06-28T00:00:00Z)"

	if got != want {
		t.Fatalf("Info() = %q, want %q", got, want)
	}
}

func setBuildMetadata(version, commit, buildDate string, buildInfo func() (*debug.BuildInfo, bool)) func() {
	oldVersion := Version
	oldCommit := Commit
	oldBuildDate := BuildDate
	oldReadBuildInfo := readBuildInfo

	Version = version
	Commit = commit
	BuildDate = buildDate
	if buildInfo == nil {
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return nil, false
		}
	} else {
		readBuildInfo = buildInfo
	}

	return func() {
		Version = oldVersion
		Commit = oldCommit
		BuildDate = oldBuildDate
		readBuildInfo = oldReadBuildInfo
	}
}
