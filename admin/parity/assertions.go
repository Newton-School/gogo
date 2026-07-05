package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AssertContains fails when any expected normalized fragment is absent.
func AssertContains(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(body, fragment) {
			t.Fatalf("admin parity output missing %q\n\n%s", fragment, body)
		}
	}
}

// AssertGolden compares normalized admin output with a golden fixture.
func AssertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "parity", "golden", name)
	if os.Getenv("GOGO_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nRun GOGO_UPDATE_GOLDEN=1 go test ./admin -run Parity to create it.", name, err)
	}
	want := strings.TrimSpace(string(wantBytes))
	if got != want {
		t.Fatalf("golden mismatch for %s\nwant:\n%s\n\ngot:\n%s", name, want, got)
	}
}
