package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveTestDirectoryHandlesReadOnlyModulesWithoutFollowingLinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "owned")
	module := filepath.Join(root, "module-cache", "example.test", "dependency@v1.0.0", "nested")
	if err := os.MkdirAll(module, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "source.go"), []byte("package dependency\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(module, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(module), 0555); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "keep"), []byte("unchanged"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "external-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(external, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(external, 0700)
	if err := removeTestDirectory(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned artifacts remain", err)
	}
	info, err := os.Stat(external)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("cleanup changed an external directory", err)
	}
	data, err := os.ReadFile(filepath.Join(external, "keep"))
	if err != nil || string(data) != "unchanged" {
		t.Fatal("cleanup followed an external link", err)
	}
}

func TestRemoveTestDirectoryReportsCleanupFailure(t *testing.T) {
	if err := removeTestDirectory(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleanup failure was hidden", err)
	}
}

func TestIndependentModuleEnvironmentKeepsIsolationWithConfiguredDependencyProxy(t *testing.T) {
	for _, configured := range []string{"", "https://proxy.example.test,direct", "file:///dependency-cache,off", "off"} {
		t.Run(configured, func(t *testing.T) {
			t.Setenv("GOPROXY", configured)
			t.Setenv("GOWORK", "unrelated-workspace")
			t.Setenv("GOMODCACHE", "unrelated-module-cache")
			t.Setenv("GOGO_TEST_POSTGRES_DSN", "unrelated-database")
			t.Setenv("GOSUMDB", "sum.golang.org")
			proxy := filepath.Join(t.TempDir(), "proxy")
			values := map[string]string{}
			for _, entry := range testEnv(proxy) {
				key, value, found := strings.Cut(entry, "=")
				if !found {
					t.Fatal("invalid environment entry")
				}
				if _, duplicate := values[key]; duplicate {
					t.Fatalf("duplicate environment key %s", key)
				}
				values[key] = value
			}
			fallback := configured
			if fallback == "" {
				fallback = "https://proxy.golang.org"
			}
			if values["GOPROXY"] != "file://"+filepath.ToSlash(proxy)+","+fallback {
				t.Fatal("packaged modules must remain the first source")
			}
			if values["GOWORK"] != "off" || values["GOMODCACHE"] != filepath.Join(filepath.Dir(proxy), "module-cache") {
				t.Fatal("workspace or module cache isolation lost")
			}
			if _, exists := values["GOGO_TEST_POSTGRES_DSN"]; exists {
				t.Fatal("unrelated database escaped into independent module test")
			}
			if values["GOSUMDB"] != "sum.golang.org" || values["GONOSUMDB"] != modulePath+","+modulePath+"/*" {
				t.Fatal("dependency checksum verification changed")
			}
		})
	}
}
