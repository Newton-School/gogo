package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/internal/version"
)

func TestReleaseModulePins(t *testing.T) {
	repo := filepath.Join("..", "..")
	for _, module := range append(append([]string{}, modules...), "tests/integration") {
		data, err := os.ReadFile(filepath.Join(repo, module, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			for i, word := range fields {
				if (word == modulePath || strings.HasPrefix(word, modulePath+"/")) && i+1 < len(fields) && fields[i+1] != version.Module {
					t.Errorf("%s: dependency pin %s must match %s", module, fields[i+1], version.Module)
				}
			}
		}
	}
}

func TestPackageUsesReleaseVersion(t *testing.T) {
	repo, proxy := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"go.mod": "module " + modulePath + "\n", "gogo.go": "package gogo\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := packageModule(repo, proxy, ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proxy, escape(modulePath), "@v")
	for _, suffix := range []string{".mod", ".info", ".zip"} {
		if _, err := os.Stat(filepath.Join(dir, version.Module+suffix)); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := zip.OpenReader(filepath.Join(dir, version.Module+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, modulePath+"@"+version.Module+"/") {
			t.Fatal(file.Name)
		}
	}
}
