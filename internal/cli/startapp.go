package cli

import (
	"context"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	gogotemplates "github.com/Newton-School/gogo/internal/cli/templates"
)

// NewStartappCommand creates the app generator command.
func NewStartappCommand() Command {
	return startappCommand{}
}

type startappCommand struct{}

func (c startappCommand) Name() string {
	return "startapp"
}

func (c startappCommand) Summary() string {
	return "Create a new Gogo app"
}

func (c startappCommand) Run(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("startapp", flag.ContinueOnError)
	force := flags.Bool("force", false, "allow generation into a non-empty directory")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}

	remaining := flags.Args()
	if len(remaining) < 1 || len(remaining) > 2 {
		return fmt.Errorf("%w: usage startapp [--force] <name> [path]", ErrInvalidArguments)
	}

	appName := remaining[0]
	if !goIdentifierPattern.MatchString(appName) {
		return fmt.Errorf("%w: app name %q must be a valid Go identifier", ErrInvalidArguments, appName)
	}

	target := appName
	if len(remaining) == 2 {
		target = remaining[1]
	}

	if err := ensureProjectTarget(target, *force); err != nil {
		return err
	}

	files, err := gogotemplates.AppFiles(gogotemplates.AppData{AppName: appName, AppLabel: appName, ModulePath: modulePathForGeneratedApp(target)})
	if err != nil {
		return fmt.Errorf("%w: render app templates: %v", ErrCommandFailed, err)
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

	if err := autoInstallGeneratedApp(target, appName); err != nil {
		return err
	}

	return nil
}

func modulePathForGeneratedApp(target string) string {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return ""
	}
	appsDir := filepath.Dir(targetAbs)
	if filepath.Base(appsDir) != "apps" {
		return ""
	}
	modulePath, err := readModulePath(filepath.Join(filepath.Dir(appsDir), "go.mod"))
	if err != nil {
		return ""
	}
	return modulePath
}

func autoInstallGeneratedApp(target, appName string) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("%w: resolve app target: %v", ErrCommandFailed, err)
	}
	appsDir := filepath.Dir(targetAbs)
	if filepath.Base(appsDir) != "apps" {
		return nil
	}
	projectRoot := filepath.Dir(appsDir)
	goModPath := filepath.Join(projectRoot, "go.mod")
	modulePath, err := readModulePath(goModPath)
	if err != nil {
		return nil
	}
	projectName := filepath.Base(projectRoot)
	projectDir := filepath.Join(projectRoot, projectName)
	if _, err := os.Stat(projectDir); err != nil {
		return nil
	}
	appImportPath := modulePath + "/apps/" + appName

	if err := installAppInSettings(filepath.Join(projectDir, "settings", "base.go"), appName); err != nil {
		return err
	}
	if err := appendInstalledAppEnv(filepath.Join(projectRoot, ".env.example"), appName); err != nil {
		return err
	}
	if err := appendInstalledAppEnv(filepath.Join(projectRoot, ".env"), appName); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "urls.go"), appImportPath, "// gogo:startapp-routes", fmt.Sprintf("\tif err := %s.RegisterRoutes(router); err != nil {\n\t\treturn err\n\t}\n\t// gogo:startapp-routes", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "urls.go"), appImportPath, "// gogo:startapp-api-routes", fmt.Sprintf("\tif err := %s.RegisterAPI(router); err != nil {\n\t\treturn err\n\t}\n\t// gogo:startapp-api-routes", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "admin.go"), appImportPath, "// gogo:startapp-admin", fmt.Sprintf("\tif err := %s.RegisterAdmin(registry); err != nil {\n\t\tpanic(err)\n\t}\n\t// gogo:startapp-admin", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "queue.go"), appImportPath, "// gogo:startapp-tasks", fmt.Sprintf("\tif err := %s.RegisterTasks(app); err != nil {\n\t\tpanic(err)\n\t}\n\t// gogo:startapp-tasks", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "app.go"), appImportPath, "// gogo:startapp-configs", fmt.Sprintf("\t\t%s.NewConfig(),\n\t\t// gogo:startapp-configs", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "app.go"), appImportPath, "// gogo:startapp-model-metadata", fmt.Sprintf("\tmetadata = append(metadata, %s.ModelMetadata()...)\n\t// gogo:startapp-model-metadata", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "app.go"), appImportPath+"/migrations", "// gogo:startapp-migrations", fmt.Sprintf("\tregistered = append(registered, %smigrations.Migrations()...)\n\t// gogo:startapp-migrations", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "app.go"), appImportPath, "// gogo:startapp-commands", fmt.Sprintf("\tcommands = append(commands, %s.Commands()...)\n\t// gogo:startapp-commands", appName)); err != nil {
		return err
	}
	if err := installAppInGoFile(filepath.Join(projectDir, "app.go"), appImportPath, "// gogo:startapp-checks", fmt.Sprintf("\tregistered = append(registered, %s.Checks()...)\n\t// gogo:startapp-checks", appName)); err != nil {
		return err
	}
	return nil
}

func readModulePath(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modulePath := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if modulePath != "" {
				return modulePath, nil
			}
		}
	}
	return "", fmt.Errorf("module path not found")
}

func installAppInSettings(path, appName string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(contents)
	quoted := `"` + appName + `"`
	if strings.Contains(text, quoted) {
		return nil
	}
	start := strings.Index(text, "settings.InstalledApps = []string{")
	if start < 0 {
		return nil
	}
	end := strings.Index(text[start:], "\n\t}")
	if end < 0 {
		return nil
	}
	insertAt := start + end
	text = text[:insertAt] + "\n\t\t" + quoted + "," + text[insertAt:]
	return writeGoFile(path, text)
}

func appendInstalledAppEnv(path, appName string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(contents), "\n")
	for index, line := range lines {
		if !strings.HasPrefix(line, "GOGO_INSTALLED_APPS=") {
			continue
		}
		if strings.Contains(","+line+",", ","+appName+",") {
			return nil
		}
		if strings.TrimSpace(line) == "GOGO_INSTALLED_APPS=" {
			lines[index] = "GOGO_INSTALLED_APPS=" + appName
		} else {
			lines[index] = line + "," + appName
		}
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil
}

func installAppInGoFile(path, importPath, marker, replacement string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(contents)
	if !strings.Contains(text, marker) || replacementAlreadyInstalled(text, marker, replacement) {
		return nil
	}
	text = addGoImport(text, importPath)
	text = strings.Replace(text, marker, replacement, 1)
	return writeGoFile(path, text)
}

func replacementAlreadyInstalled(text, marker, replacement string) bool {
	probe := strings.Replace(replacement, marker, "", 1)
	for _, line := range strings.Split(probe, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		return strings.Contains(text, line)
	}
	return false
}

func addGoImport(source, importPath string) string {
	quoted := `"` + importPath + `"`
	if strings.Contains(source, quoted) {
		return source
	}
	if strings.Contains(source, "import (\n") {
		return strings.Replace(source, "import (\n", "import (\n\t"+quoted+"\n", 1)
	}
	start := strings.Index(source, "import ")
	if start < 0 {
		return source
	}
	lineEnd := strings.Index(source[start:], "\n")
	if lineEnd < 0 {
		return source
	}
	importLine := source[start : start+lineEnd]
	existing := strings.TrimSpace(strings.TrimPrefix(importLine, "import "))
	block := "import (\n\t" + quoted + "\n\t" + existing + "\n)"
	return source[:start] + block + source[start+lineEnd:]
}

func writeGoFile(path, source string) error {
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return fmt.Errorf("%w: format %s: %v", ErrCommandFailed, path, err)
	}
	if err := os.WriteFile(path, formatted, 0o644); err != nil {
		return fmt.Errorf("%w: write %s: %v", ErrCommandFailed, path, err)
	}
	return nil
}
