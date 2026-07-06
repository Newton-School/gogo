package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const generatedPackagesPath = "docs/code/generated/public-packages.md"

type snippet struct {
	File  string
	Index int
	Line  int
	Lang  string
	Code  string
}

type publicPackage struct {
	ImportPath string
	Name       string
	Doc        string
}

func main() {
	command := "all"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	switch command {
	case "all":
		err = runAll(root)
	case "links":
		err = checkMarkdownLinks(root)
	case "examples":
		err = checkCodeExamples(root)
	case "generated":
		err = checkGeneratedDocs(root)
	case "update-generated":
		err = updateGeneratedDocs(root)
	case "tutorials":
		err = checkTutorials(root)
	default:
		err = fmt.Errorf("unknown docs verification command %q", command)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func runAll(root string) error {
	checks := []struct {
		name string
		run  func(string) error
	}{
		{name: "markdown links", run: checkMarkdownLinks},
		{name: "code examples", run: checkCodeExamples},
		{name: "generated docs", run: checkGeneratedDocs},
		{name: "tutorials", run: checkTutorials},
	}
	for _, check := range checks {
		if err := check.run(root); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	return nil
}

func checkMarkdownLinks(root string) error {
	files, err := markdownFiles(root)
	if err != nil {
		return err
	}
	linkPattern := regexp.MustCompile(`!?\[[^\]\n]+\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	var problems []string
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, match := range linkPattern.FindAllSubmatch(content, -1) {
			target := strings.TrimSpace(string(match[1]))
			target = strings.Trim(target, "<>")
			if skipLinkTarget(target) {
				continue
			}
			rawPath := strings.SplitN(target, "#", 2)[0]
			rootRelative := strings.HasPrefix(rawPath, "/")
			rawPath = strings.TrimPrefix(rawPath, "/")
			rawPath = strings.TrimPrefix(rawPath, "./")
			rawPath = strings.TrimSuffix(rawPath, "/")
			if rawPath == "" {
				continue
			}
			path := rawPath
			path, err = filepath.Localize(path)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s has invalid link %q", rel(root, file), target))
				continue
			}
			candidates := markdownLinkCandidates(root, file, path, rootRelative)
			if !markdownTargetExists(candidates) {
				problems = append(problems, fmt.Sprintf("%s has missing link target %q", rel(root, file), target))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	fmt.Println("markdown links ok")
	return nil
}

func checkCodeExamples(root string) error {
	files, err := markdownFiles(root)
	if err != nil {
		return err
	}
	var snippets []snippet
	for _, file := range files {
		extracted, err := extractSnippets(file)
		if err != nil {
			return err
		}
		for _, item := range extracted {
			if item.Lang == "go" && !strings.Contains(item.Code, "docverify:skip") {
				snippets = append(snippets, item)
			}
		}
	}
	if len(snippets) == 0 {
		fmt.Println("code examples ok")
		return nil
	}
	tmp, err := os.MkdirTemp("", "gogo-doc-examples-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := writeSnippetModule(tmp, root); err != nil {
		return err
	}
	if err := writeSnippetPackages(tmp, root, snippets); err != nil {
		return err
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmp
	if output, err := tidy.CombinedOutput(); err != nil {
		return fmt.Errorf("code example module setup failed:\n%s", output)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("code examples do not compile:\n%s", output)
	}
	fmt.Printf("code examples ok (%d snippets)\n", len(snippets))
	return nil
}

func checkGeneratedDocs(root string) error {
	expected, err := renderGeneratedPackages(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, generatedPackagesPath)
	actual, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s is missing or unreadable: %w", generatedPackagesPath, err)
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%s is stale; run `make docs-generated-update`", generatedPackagesPath)
	}
	fmt.Println("generated docs ok")
	return nil
}

func updateGeneratedDocs(root string) error {
	content, err := renderGeneratedPackages(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, generatedPackagesPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	fmt.Printf("updated %s\n", generatedPackagesPath)
	return nil
}

func checkTutorials(root string) error {
	tmp, err := os.MkdirTemp("", "gogo-tutorial-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	projectDir := filepath.Join(tmp, "mysite")
	if err := run(root, "go", "run", "./cmd/gogo", "startproject", "mysite", projectDir); err != nil {
		return err
	}
	appDir := filepath.Join(projectDir, "apps", "blog")
	if err := run(root, "go", "run", "./cmd/gogo", "startapp", "blog", appDir); err != nil {
		return err
	}
	replace := "github.com/Newton-School/gogo=" + filepath.ToSlash(root)
	if err := run(projectDir, "go", "mod", "edit", "-replace", replace); err != nil {
		return err
	}
	if err := run(projectDir, "go", "mod", "tidy"); err != nil {
		return err
	}
	if err := run(projectDir, "go", "test", "./..."); err != nil {
		return err
	}
	fmt.Println("tutorials ok")
	return nil
}

func markdownFiles(root string) ([]string, error) {
	var files []string
	readme := filepath.Join(root, "README.md")
	if _, err := os.Stat(readme); err == nil {
		files = append(files, readme)
	}
	docsDir := filepath.Join(root, "docs")
	if err := filepath.WalkDir(docsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipMarkdownDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isMarkdownPath(path) {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func markdownLinkCandidates(root string, file string, path string, rootRelative bool) []string {
	if rootRelative {
		candidates := []string{}
		if isPublicDocsContent(root, file) {
			candidates = append(candidates, filepath.Join(root, "docs", "public", "src", "content", "docs", path))
		}
		return append(candidates, filepath.Join(root, path))
	}
	return []string{filepath.Join(filepath.Dir(file), path)}
}

func markdownTargetExists(candidates []string) bool {
	for _, candidate := range candidates {
		if pathExists(candidate) {
			return true
		}
		trimmed := strings.TrimRight(candidate, string(os.PathSeparator))
		if pathExists(trimmed+".md") || pathExists(trimmed+".mdx") {
			return true
		}
		if pathExists(filepath.Join(candidate, "index.md")) || pathExists(filepath.Join(candidate, "index.mdx")) {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isPublicDocsContent(root string, file string) bool {
	relative := rel(root, file)
	return strings.HasPrefix(relative, "docs/public/src/content/docs/")
}

func isMarkdownPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".mdx"
}

func skipMarkdownDir(name string) bool {
	switch name {
	case "node_modules", "dist", ".astro":
		return true
	default:
		return false
	}
}

func skipLinkTarget(target string) bool {
	if target == "" || strings.HasPrefix(target, "#") {
		return true
	}
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "app://")
}

func extractSnippets(file string) ([]snippet, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(content), "\n")
	var snippets []snippet
	var builder strings.Builder
	inBlock := false
	lang := ""
	startLine := 0
	for index, line := range lines {
		lineNumber := index + 1
		if strings.HasPrefix(line, "```") {
			if inBlock {
				snippets = append(snippets, snippet{
					File:  file,
					Index: len(snippets) + 1,
					Line:  startLine,
					Lang:  lang,
					Code:  strings.TrimRight(builder.String(), "\n"),
				})
				builder.Reset()
				inBlock = false
				lang = ""
				continue
			}
			inBlock = true
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "```")))
			if len(fields) > 0 {
				lang = strings.ToLower(fields[0])
			}
			startLine = lineNumber
			continue
		}
		if inBlock {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	if inBlock {
		return nil, fmt.Errorf("%s has an unterminated code fence starting at line %d", file, startLine)
	}
	return snippets, nil
}

func writeSnippetModule(tmp string, root string) error {
	content := fmt.Sprintf(`module gogo-doc-snippets

go 1.26.4

toolchain go1.26.4

require github.com/Newton-School/gogo v0.0.0

replace github.com/Newton-School/gogo => %s
`, filepath.ToSlash(root))
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(content), 0o644); err != nil {
		return err
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(tmp, "go.sum"), sum, 0o644)
}

func writeSnippetPackages(tmp string, root string, snippets []snippet) error {
	packageDirs := map[string]string{}
	for _, item := range snippets {
		trimmed := strings.TrimSpace(item.Code)
		if strings.HasPrefix(trimmed, "package ") {
			packageName := packageName(trimmed)
			if packageName == "" {
				return fmt.Errorf("%s:%d missing package name", rel(root, item.File), item.Line)
			}
			key := rel(root, item.File) + ":" + packageName
			dir, ok := packageDirs[key]
			if !ok {
				dir = filepath.Join(tmp, safeName(key))
				packageDirs[key] = dir
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("snippet_%03d.go", item.Index)), []byte(item.Code+"\n"), 0o644); err != nil {
				return err
			}
			continue
		}
		dir := filepath.Join(tmp, safeName(rel(root, item.File)), fmt.Sprintf("snippet_%03d", item.Index))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		source := wrappedSnippet(item.Code)
		if err := os.WriteFile(filepath.Join(dir, "snippet_test.go"), []byte(source), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func wrappedSnippet(code string) string {
	return fmt.Sprintf(`package snippet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/api"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/email"
	"github.com/Newton-School/gogo/forms"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/canvas"
)

var (
	_ = context.Background
	_ = http.MethodGet
	_ = httptest.NewRequest
	_ = testing.T{}
	_ = time.Second
	_ = admin.ModelAdmin{}
	_ = api.NewRouter
	_ = auth.User{}
	_ = conf.DefaultSettings
	_ = email.NewMemoryBackend
	_ = forms.NewForm
	_ = migrations.InitialMigrationName
	_ = operations.RunSQL{}
	_ = models.Metadata{}
	_ = orm.NewQuery
	_ = postgres.New
	_ = queue.NewApp
	_ = canvas.NewChain
)

func TestSnippetCompiles(t *testing.T) {
%s
}
`, indent(code, "\t"))
}

func packageName(source string) string {
	pattern := regexp.MustCompile(`(?m)^package\s+([A-Za-z_][A-Za-z0-9_]*)`)
	match := pattern.FindStringSubmatch(source)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func renderGeneratedPackages(root string) ([]byte, error) {
	packages, err := listPublicPackages(root)
	if err != nil {
		return nil, err
	}
	var builder strings.Builder
	builder.WriteString("# Generated Public Packages\n\n")
	builder.WriteString("> Generated by `make docs-generated-update`; do not edit by hand.\n\n")
	builder.WriteString("| Import Path | Package | Doc |\n")
	builder.WriteString("| --- | --- | --- |\n")
	for _, pkg := range packages {
		fmt.Fprintf(&builder, "| `%s` | `%s` | %s |\n", escapeTable(pkg.ImportPath), escapeTable(pkg.Name), escapeTable(pkg.Doc))
	}
	return []byte(builder.String()), nil
}

func listPublicPackages(root string) ([]publicPackage, error) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{.Name}}\t{{.Doc}}", "./...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("go list failed:\n%s", exitErr.Stderr)
		}
		return nil, err
	}
	var packages []publicPackage
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("unexpected go list row %q", line)
		}
		doc := ""
		if len(parts) == 3 {
			doc = strings.TrimSpace(parts[2])
		}
		importPath := parts[0]
		if skipPublicPackage(importPath) {
			continue
		}
		packages = append(packages, publicPackage{ImportPath: importPath, Name: parts[1], Doc: doc})
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].ImportPath < packages[j].ImportPath
	})
	return packages, nil
}

func skipPublicPackage(importPath string) bool {
	return strings.Contains(importPath, "/internal/") ||
		strings.Contains(importPath, "/cmd/") ||
		strings.Contains(importPath, "/examples/") ||
		strings.HasSuffix(importPath, "/scripts")
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed in %s:\n%s", name, strings.Join(args, " "), dir, output)
	}
	return nil
}

func safeName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
			continue
		}
		builder.WriteByte('_')
	}
	return strings.Trim(builder.String(), "_")
}

func indent(value string, prefix string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	if value == "" {
		return "-"
	}
	return value
}

func rel(root string, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(value)
}
