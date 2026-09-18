package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepositoryRoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  string
		content string
		remove  bool
		wantErr bool
	}{
		{name: "public modules only"},
		{name: "missing root module", change: "go.mod", remove: true, wantErr: true},
		{name: "unrelated root module", change: "go.mod", content: "module example.com/other\n", wantErr: true},
		{name: "missing module declaration", change: "go.mod", content: "go 1.26.8\n", wantErr: true},
		{name: "missing optional module checkout", change: "admin/go.mod", remove: true, wantErr: true},
		{name: "wrong nested module", change: "async/redis/go.mod", content: "module example.com/other\n", wantErr: true},
		{name: "quoted module with comments", change: "go.mod", content: "// Module declaration\nmodule \"" + modulePath + "\" // root\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			for _, module := range modules {
				name := modulePath
				if module != "" {
					name += "/" + module
				}
				dir := filepath.Join(repo, filepath.FromSlash(module))
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+name+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.change != "" {
				path := filepath.Join(repo, filepath.FromSlash(tc.change))
				var err error
				if tc.remove {
					err = os.Remove(path)
				} else {
					err = os.WriteFile(path, []byte(tc.content), 0644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validateRepositoryRoot(repo); (err != nil) != tc.wantErr {
				t.Fatalf("validateRepositoryRoot() = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
	if err := validateRepositoryRoot(t.TempDir()); err == nil {
		t.Fatal("accepted an empty directory")
	}
}

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
