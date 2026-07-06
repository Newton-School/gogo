package migrations

import (
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Writer writes deterministic Go migration files.
type Writer struct {
	Dir string
}

// NewWriter creates a migration writer.
func NewWriter(dir string) Writer {
	return Writer{Dir: dir}
}

// Write writes a Go migration file and returns its path.
func (w Writer) Write(migration Migration) (string, error) {
	var builder strings.Builder
	packageName := w.packageName()
	builder.WriteString("package " + packageName + "\n\n")
	builder.WriteString("import gogomigrations \"github.com/Newton-School/gogo/migrations\"\n\n")
	variableName := generatedMigrationVariableName(migration.Name)
	builder.WriteString("// " + variableName + " describes this generated migration.\n")
	builder.WriteString("var " + variableName + " = gogomigrations.Migration{\n")
	builder.WriteString(fmt.Sprintf("\tAppLabel: %q,\n", migration.AppLabel))
	builder.WriteString(fmt.Sprintf("\tName: %q,\n", migration.Name))
	builder.WriteString(fmt.Sprintf("\tAtomic: %t,\n", migration.Atomic))
	builder.WriteString("\tDependencies: []gogomigrations.Dependency{\n")
	for _, dependency := range migration.Dependencies {
		builder.WriteString(fmt.Sprintf("\t\t{AppLabel: %q, Name: %q},\n", dependency.AppLabel, dependency.Name))
	}
	builder.WriteString("\t},\n")
	builder.WriteString("\tReplaces: []gogomigrations.Dependency{\n")
	for _, dependency := range migration.Replaces {
		builder.WriteString(fmt.Sprintf("\t\t{AppLabel: %q, Name: %q},\n", dependency.AppLabel, dependency.Name))
	}
	builder.WriteString("\t},\n")
	builder.WriteString("\tRunBefore: []gogomigrations.Dependency{\n")
	for _, dependency := range migration.RunBefore {
		builder.WriteString(fmt.Sprintf("\t\t{AppLabel: %q, Name: %q},\n", dependency.AppLabel, dependency.Name))
	}
	builder.WriteString("\t},\n")
	builder.WriteString("\tOperations: []gogomigrations.Operation{\n")
	for _, operation := range migration.Operations {
		specJSON, err := OperationSpecFor(operation).CanonicalJSON()
		if err != nil {
			return "", err
		}
		builder.WriteString(fmt.Sprintf("\t\tgogomigrations.ManifestOperation{SpecJSON: %q},\n", specJSON))
	}
	builder.WriteString("\t},\n")
	builder.WriteString("}\n")
	content := []byte(builder.String())
	formatted, err := format.Source(content)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(w.Dir, migration.Name+".go")
	return path, os.WriteFile(path, formatted, 0o644)
}

func (w Writer) packageName() string {
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return "migrations"
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		names = append(names, filepath.Join(w.Dir, entry.Name()))
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.PackageClauseOnly)
		if err != nil || file == nil || file.Name == nil || file.Name.Name == "" {
			continue
		}
		return file.Name.Name
	}
	return "migrations"
}

func generatedMigrationVariableName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r < '0' || (r > '9' && r < 'A') || (r > 'Z' && r < 'a') || r > 'z'
	})
	var builder strings.Builder
	builder.WriteString("GeneratedMigration")
	for _, part := range parts {
		if part == "" {
			continue
		}
		builder.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			builder.WriteString(part[1:])
		}
	}
	return builder.String()
}
