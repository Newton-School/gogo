package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const (
	defaultVersion = "0.0.0-dev"
	unknown        = "unknown"
)

var (
	// Version is the framework version. Release builds override it with ldflags.
	Version = defaultVersion

	// Commit is the source revision. Release builds override it with ldflags.
	Commit = unknown

	// BuildDate is the build timestamp. Release builds override it with ldflags.
	BuildDate = unknown

	readBuildInfo = debug.ReadBuildInfo
)

// Info returns stable human-readable version metadata.
func Info() string {
	info := effectiveInfo()
	return fmt.Sprintf("gogo %s (commit %s, built %s)", info.version, info.commit, info.buildDate)
}

// ModuleVersion returns the Go module version suitable for require directives.
func ModuleVersion() string {
	value := strings.TrimSpace(effectiveInfo().version)
	if value == "" || value == defaultVersion || value == unknown || strings.Contains(value, "dev") {
		return ""
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if len(value) < 2 || value[1] < '0' || value[1] > '9' {
		return ""
	}
	return value
}

type buildMetadata struct {
	version   string
	commit    string
	buildDate string
}

func effectiveInfo() buildMetadata {
	metadata := buildMetadata{
		version:   Version,
		commit:    Commit,
		buildDate: BuildDate,
	}

	buildInfo, ok := readBuildInfo()
	if !ok || buildInfo == nil {
		return metadata
	}

	if metadata.version == defaultVersion {
		if version := moduleVersion(buildInfo); version != "" {
			metadata.version = version
		}
	}
	if metadata.commit == unknown {
		if commit := buildSetting(buildInfo, "vcs.revision"); commit != "" {
			metadata.commit = commit
		}
	}
	if metadata.buildDate == unknown {
		if buildDate := buildSetting(buildInfo, "vcs.time"); buildDate != "" {
			metadata.buildDate = buildDate
		}
	}

	return metadata
}

func moduleVersion(info *debug.BuildInfo) string {
	if version := normalizeModuleVersion(info.Main.Version); version != "" {
		return version
	}
	for _, dependency := range info.Deps {
		if dependency == nil {
			continue
		}
		if dependency.Path == "github.com/Newton-School/gogo" {
			if version := normalizeModuleVersion(dependency.Version); version != "" {
				return version
			}
		}
	}
	return ""
}

func normalizeModuleVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || version == "(devel)" {
		return ""
	}
	if strings.HasPrefix(version, "v") && len(version) > 1 && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}

func buildSetting(info *debug.BuildInfo, key string) string {
	for _, setting := range info.Settings {
		if setting.Key == key {
			return strings.TrimSpace(setting.Value)
		}
	}
	return ""
}
