package main

import (
	"path/filepath"
	"strings"
	"testing"
)

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
