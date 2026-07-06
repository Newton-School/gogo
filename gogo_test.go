package gogo

import "testing"

func TestPackageMetadata(t *testing.T) {
	if Name != "Gogo" {
		t.Fatalf("Name = %q, want %q", Name, "Gogo")
	}

	if ModulePath != "github.com/Newton-School/gogo" {
		t.Fatalf("ModulePath = %q, want %q", ModulePath, "github.com/Newton-School/gogo")
	}
}
