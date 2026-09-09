// Command test-modules packages development modules into a temporary local Go
// proxy, then verifies them without workspace or local replace directives.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/Newton-School/gogo/internal/version"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const modulePath = "github.com/Newton-School/gogo"

var modules = []string{"", "admin", "async", "connectors/postgres", "connectors/redis", "async/redis"}

func main() {
	race := flag.Bool("race", true, "enable race detector")
	flag.Parse()
	if err := run(*race); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(race bool) (resultErr error) {
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(repo, ".agentflow")); err != nil {
		return errors.New("run from the Gogo repository root")
	}
	temp, err := os.MkdirTemp("", "gogo-module-tests-")
	if err != nil {
		return err
	}
	defer func() {
		if err := removeTestDirectory(temp); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove isolated module test artifacts: %w", err))
		}
	}()
	proxy := filepath.Join(temp, "proxy")
	for _, module := range modules {
		if err = packageModule(repo, proxy, module); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for _, module := range modules {
		name := modulePath
		if module != "" {
			name += "/" + module
		}
		fmt.Println("Verify independent module:", name)
		copyDir := filepath.Join(temp, "source", filepath.FromSlash(module))
		if module == "" {
			copyDir = filepath.Join(temp, "source", "root")
		}
		if err = os.MkdirAll(copyDir, 0755); err != nil {
			return err
		}
		zipFile := filepath.Join(proxy, escape(name), "@v", version.Module+".zip")
		if err = extract(zipFile, copyDir, name+"@"+version.Module+"/"); err != nil {
			return err
		}
		args := []string{"test", "-mod=mod"}
		if race {
			args = append(args, "-race")
		}
		args = append(args, "./...")
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = copyDir
		cmd.Env = testEnv(proxy)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("independent module %s: %w", name, err)
		}
	}
	return testConsumer(ctx, temp, proxy)
}

// Downloaded modules contain read-only directories. os.RemoveAll alone cannot
// remove their children on Unix, even though this runner created the cache.
// Only restore owner permissions inside our freshly allocated temporary tree;
// WalkDir does not follow symlinks into any external directory.
func removeTestDirectory(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func testConsumer(ctx context.Context, temp, proxy string) error {
	fmt.Println("Verify generated consumer outside the workspace")
	consumer := filepath.Join(temp, "consumer")
	command := func(dir string, args ...string) error {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = testEnv(proxy)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if err := command(filepath.Join(temp, "source", "root"), "run", "./cmd/gogo", "startproject", consumer, "--module", "example.com/storefront"); err != nil {
		return err
	}
	if err := command(consumer, "mod", "tidy"); err != nil {
		return err
	}
	if err := command(consumer, "run", "manage.go", "startapp", "catalog"); err != nil {
		return err
	}
	if err := command(consumer, "run", "manage.go", "generate", "--check"); err != nil {
		return fmt.Errorf("fresh app descriptor consistency: %w", err)
	}
	model := `package catalog
import "github.com/Newton-School/gogo/core/models"
type Product struct { models.Base; ID int64; Name string; Price string; Stock int64 }
func(*Product) Schema()models.Schema{return models.Schema{AppLabel:"catalog",Name:"Product",Fields:[]models.Field{
models.BigAutoField("id",models.WithStructField("ID")),
models.CharField("name",models.WithStructField("Name"),models.WithMaxLength(200)),
models.DecimalField("price",10,2,models.WithStructField("Price")),
models.IntegerField("stock",models.WithStructField("Stock")),
}}}
`
	if err := os.WriteFile(filepath.Join(consumer, "apps", "catalog", "models.go"), []byte(model), 0644); err != nil {
		return err
	}
	for _, args := range [][]string{{"run", "manage.go", "generate"}, {"run", "manage.go", "generate", "--check"}, {"run", "manage.go", "runserver", "--help"}, {"run", "manage.go", "makemigrations", "catalog"}, {"run", "manage.go", "makemigrations", "catalog", "--check"}, {"test", "./..."}, {"build", "-o", filepath.Join(temp, "manage"), "manage.go"}} {
		if err := command(consumer, args...); err != nil {
			return fmt.Errorf("generated consumer %v: %w", args, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(consumer, "apps", "catalog", "zz_gogo.gen.go"))
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte("Name orm.Ref[string]")) && !bytes.Contains(data, []byte("Name  orm.Ref[string]")) {
		if !bytes.Contains(data, []byte("orm.Field[string](\"name\")")) {
			return errors.New("typed model field references not generated")
		}
	}
	return nil
}
func testEnv(proxy string) []string {
	dependencyProxy := os.Getenv("GOPROXY")
	if dependencyProxy == "" {
		dependencyProxy = "https://proxy.golang.org"
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GOWORK" || key == "GOPROXY" || key == "GONOSUMDB" || key == "GOMODCACHE" || key == "GOGO_TEST_POSTGRES_DSN" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GOWORK=off", "GOPROXY=file://"+filepath.ToSlash(proxy)+","+dependencyProxy, "GONOSUMDB="+modulePath+","+modulePath+"/*", "GOMODCACHE="+filepath.Join(filepath.Dir(proxy), "module-cache"))
}
func escape(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func packageModule(repo, proxy, module string) error {
	name := modulePath
	if module != "" {
		name += "/" + module
	}
	source := filepath.Join(repo, filepath.FromSlash(module))
	mod, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		return err
	}
	if bytes.Contains(mod, []byte("replace ")) || bytes.Contains(mod, []byte("replace(")) {
		return fmt.Errorf("release module contains a replace directive: %s", name)
	}
	dest := filepath.Join(proxy, escape(name), "@v")
	if err = os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	for file, value := range map[string][]byte{version.Module + ".mod": mod, "list": []byte(version.Module + "\n"), version.Module + ".info": []byte(`{"Version":"` + version.Module + `","Time":"2000-01-01T00:00:00Z"}`)} {
		if err = os.WriteFile(filepath.Join(dest, file), value, 0644); err != nil {
			return err
		}
	}
	file, err := os.Create(filepath.Join(dest, version.Module+".zip"))
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if _, e := os.Stat(filepath.Join(path, "go.mod")); e == nil {
				return filepath.SkipDir
			}
			if module == "" && !strings.Contains(relative, "/") && relative != "core" && relative != "cmd" && relative != "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "go.work" || entry.Name() == "go.work.sum" {
			return nil
		}
		if module == "" && !strings.Contains(relative, "/") && entry.Name() != "go.mod" && entry.Name() != "go.sum" && entry.Name() != "gogo.go" && entry.Name() != "README.md" && entry.Name() != "LICENSE" {
			return nil
		}
		content, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		out, e := writer.Create(name + "@" + version.Module + "/" + relative)
		if e != nil {
			return e
		}
		_, e = out.Write(content)
		return e
	})
	return errors.Join(err, writer.Close(), file.Close())
}
func extract(source, dest, prefix string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if !strings.HasPrefix(file.Name, prefix) {
			return errors.New("invalid module archive prefix")
		}
		relative := strings.TrimPrefix(file.Name, prefix)
		if !fs.ValidPath(relative) {
			return errors.New("unsafe module archive path")
		}
		target := filepath.Join(dest, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		input, e := file.Open()
		if e != nil {
			return e
		}
		output, e := os.Create(target)
		if e != nil {
			input.Close()
			return e
		}
		_, e = io.Copy(output, input)
		err = errors.Join(e, input.Close(), output.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
