package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	gogotemplates "github.com/Newton-School/gogo/internal/cli/templates"
	"github.com/Newton-School/gogo/internal/version"
)

var goIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewStartprojectCommand creates the project generator command.
func NewStartprojectCommand() Command {
	return startprojectCommand{}
}

type startprojectCommand struct{}

func (c startprojectCommand) Name() string {
	return "startproject"
}

func (c startprojectCommand) Summary() string {
	return "Create a new Gogo project"
}

func (c startprojectCommand) Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("startproject", flag.ContinueOnError)
	force := flags.Bool("force", false, "allow generation into a non-empty directory")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}

	remaining := flags.Args()
	if len(remaining) < 1 || len(remaining) > 2 {
		return fmt.Errorf("%w: usage startproject [--force] <name> [path]", ErrInvalidArguments)
	}

	projectName := remaining[0]
	if !goIdentifierPattern.MatchString(projectName) {
		return fmt.Errorf("%w: project name %q must be a valid Go identifier", ErrInvalidArguments, projectName)
	}

	target := projectName
	if len(remaining) == 2 {
		target = remaining[1]
	}

	if err := ensureProjectTarget(target, *force); err != nil {
		return err
	}

	projectData := gogotemplates.ProjectData{
		ProjectName:       projectName,
		ModulePath:        projectName,
		GogoModuleVersion: version.ModuleVersion(),
	}
	files, err := gogotemplates.ProjectFiles(projectData)
	if err != nil {
		return fmt.Errorf("%w: render project templates: %v", ErrCommandFailed, err)
	}
	for relativePath, contents := range files {
		path := filepath.Join(target, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("%w: create directory for %s: %v", ErrCommandFailed, relativePath, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return fmt.Errorf("%w: write %s: %v", ErrCommandFailed, relativePath, err)
		}
	}

	if projectData.GogoModuleVersion != "" {
		// Hydration improves first-run readiness for released CLIs, but scaffolding
		// must still succeed for local builds, unpublished test versions, and
		// temporarily unreachable module proxies.
		_ = hydrateProjectModule(ctx, target)
	}

	return nil
}

func hydrateProjectModule(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "go", "mod", "download", "all")
	command.Dir = target
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: hydrate generated module: go mod download all failed: %v\n%s", ErrCommandFailed, err, output)
	}
	return nil
}

func ensureProjectTarget(target string, force bool) error {
	entries, err := os.ReadDir(target)
	if err == nil {
		if len(entries) > 0 && !force {
			return fmt.Errorf("%w: target directory %s is not empty", ErrCommandFailed, target)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect target directory %s: %v", ErrCommandFailed, target, err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("%w: create target directory %s: %v", ErrCommandFailed, target, err)
	}
	return nil
}
