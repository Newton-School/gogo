package release

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestValidateTagAcceptsSemanticReleaseTags(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "v1.2.3", "v1.2.3-rc.1"} {
		if err := ValidateTag(tag); err != nil {
			t.Fatalf("ValidateTag(%q) error = %v", tag, err)
		}
	}
}

func TestValidateTagRejectsUnsafeTags(t *testing.T) {
	for _, tag := range []string{"1.2.3", "v1", "v1.2", "v1.2.3 dirty", "release"} {
		err := ValidateTag(tag)
		if !errors.Is(err, ErrInvalidRelease) {
			t.Fatalf("ValidateTag(%q) error = %v, want ErrInvalidRelease", tag, err)
		}
	}
}

func TestNewPlanBuildsSupportedArtifactsAndLinkerFlags(t *testing.T) {
	plan, err := NewPlan("v0.1.0", "abc123", "2026-06-28T00:00:00Z")
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Version != "0.1.0" {
		t.Fatalf("Version = %q, want 0.1.0", plan.Version)
	}
	if len(plan.Artifacts) != len(SupportedPlatforms()) {
		t.Fatalf("Artifacts = %d, want %d", len(plan.Artifacts), len(SupportedPlatforms()))
	}
	if plan.Artifacts[0].Filename != "gogo_0.1.0_linux_amd64" {
		t.Fatalf("first artifact = %q", plan.Artifacts[0].Filename)
	}
	if got := plan.Artifacts[len(plan.Artifacts)-1].Filename; got != "gogo_0.1.0_windows_arm64.exe" {
		t.Fatalf("last artifact = %q", got)
	}
	flags := strings.Join(plan.LinkerFlags, " ")
	for _, want := range []string{
		"github.com/Newton-School/gogo/internal/version.Version=0.1.0",
		"github.com/Newton-School/gogo/internal/version.Commit=abc123",
		"github.com/Newton-School/gogo/internal/version.BuildDate=2026-06-28T00:00:00Z",
	} {
		if !strings.Contains(flags, want) {
			t.Fatalf("LinkerFlags = %q, want %q", flags, want)
		}
	}
}

func TestChangelogEntryExtractsTagSection(t *testing.T) {
	markdown := `# Changelog

## Unreleased

- Future work.

## v0.1.0 - TBD

- Initial release.
- Admin, ORM, auth, and queue support.

## v0.0.1

- Older release.
`

	notes, err := ChangelogEntry(markdown, "v0.1.0")
	if err != nil {
		t.Fatalf("ChangelogEntry() error = %v", err)
	}
	if strings.Contains(notes, "Older release") || !strings.Contains(notes, "Initial release") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestSHA256AndDryRun(t *testing.T) {
	sum, err := SHA256(strings.NewReader("gogo"))
	if err != nil {
		t.Fatalf("SHA256() error = %v", err)
	}
	if sum != "16af0577252ea2fc2b73260d8fe6a4e73155e9f83bb234588b561ab01c9bca6b" {
		t.Fatalf("SHA256() = %q", sum)
	}

	plan, err := NewPlan("v0.1.0", "abc123", "2026-06-28T00:00:00Z")
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	var output bytes.Buffer
	if err := WriteDryRun(&output, plan, "- Initial release."); err != nil {
		t.Fatalf("WriteDryRun() error = %v", err)
	}
	for _, want := range []string{"release v0.1.0", "gogo_0.1.0_linux_amd64", "release notes bytes"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("dry-run output = %q, want %q", output.String(), want)
		}
	}
}
