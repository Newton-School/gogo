package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	tagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

	// ErrInvalidRelease is returned when release metadata is incomplete or unsafe.
	ErrInvalidRelease = errors.New("invalid release")
)

// Platform describes one supported release target for the gogo CLI.
type Platform struct {
	GOOS   string
	GOARCH string
}

// Artifact describes one release artifact produced by a release plan.
type Artifact struct {
	Platform Platform
	Filename string
}

// Plan is the release metadata used by the GitHub release workflow.
type Plan struct {
	Tag         string
	Version     string
	Commit      string
	BuildDate   string
	Artifacts   []Artifact
	LinkerFlags []string
}

// SupportedPlatforms returns the deterministic CLI build matrix.
func SupportedPlatforms() []Platform {
	return []Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "arm64"},
	}
}

// ValidateTag rejects tags that are not safe semantic-version release tags.
func ValidateTag(tag string) error {
	tag = strings.TrimSpace(tag)
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("%w: release tag %q must look like v1.2.3", ErrInvalidRelease, tag)
	}
	return nil
}

// NewPlan builds a release plan for the given tag, commit, and build date.
func NewPlan(tag, commit, buildDate string) (Plan, error) {
	tag = strings.TrimSpace(tag)
	commit = strings.TrimSpace(commit)
	buildDate = strings.TrimSpace(buildDate)
	if err := ValidateTag(tag); err != nil {
		return Plan{}, err
	}
	if commit == "" {
		return Plan{}, fmt.Errorf("%w: commit is required", ErrInvalidRelease)
	}
	if _, err := time.Parse(time.RFC3339, buildDate); err != nil {
		return Plan{}, fmt.Errorf("%w: build date must be RFC3339: %v", ErrInvalidRelease, err)
	}

	version := strings.TrimPrefix(tag, "v")
	plan := Plan{
		Tag:       tag,
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		LinkerFlags: []string{
			"-s",
			"-w",
			"-X", "github.com/Newton-School/gogo/internal/version.Version=" + version,
			"-X", "github.com/Newton-School/gogo/internal/version.Commit=" + commit,
			"-X", "github.com/Newton-School/gogo/internal/version.BuildDate=" + buildDate,
		},
	}
	for _, platform := range SupportedPlatforms() {
		plan.Artifacts = append(plan.Artifacts, Artifact{Platform: platform, Filename: artifactFilename(version, platform)})
	}
	return plan, nil
}

// ChangelogEntry extracts release notes for a tag from CHANGELOG.md content.
func ChangelogEntry(markdown, tag string) (string, error) {
	if err := ValidateTag(tag); err != nil {
		return "", err
	}
	lines := strings.Split(markdown, "\n")
	heading := "## " + tag
	start := -1
	for index, line := range lines {
		if line == heading || strings.HasPrefix(line, heading+" ") {
			start = index + 1
			break
		}
	}
	if start == -1 {
		return "", fmt.Errorf("%w: changelog section for %s is required", ErrInvalidRelease, tag)
	}

	end := len(lines)
	for index := start; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "## ") {
			end = index
			break
		}
	}

	notes := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if notes == "" {
		return "", fmt.Errorf("%w: changelog section for %s is empty", ErrInvalidRelease, tag)
	}
	return notes, nil
}

// SHA256 returns a hex-encoded checksum for release artifacts.
func SHA256(reader io.Reader) (string, error) {
	sum := sha256.New()
	if _, err := io.Copy(sum, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// WriteDryRun writes a human-readable release plan without building or publishing.
func WriteDryRun(writer io.Writer, plan Plan, notes string) error {
	if strings.TrimSpace(notes) == "" {
		return fmt.Errorf("%w: release notes are required", ErrInvalidRelease)
	}
	if _, err := fmt.Fprintf(writer, "release %s\n", plan.Tag); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "version %s\ncommit %s\nbuilt %s\n", plan.Version, plan.Commit, plan.BuildDate); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "artifacts"); err != nil {
		return err
	}
	for _, artifact := range plan.Artifacts {
		if _, err := fmt.Fprintf(writer, "- %s (%s/%s)\n", artifact.Filename, artifact.Platform.GOOS, artifact.Platform.GOARCH); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(writer, "release notes bytes %d\n", len([]byte(notes)))
	return err
}

func artifactFilename(version string, platform Platform) string {
	name := "gogo_" + version + "_" + platform.GOOS + "_" + platform.GOARCH
	if platform.GOOS == "windows" {
		name += ".exe"
	}
	return name
}
