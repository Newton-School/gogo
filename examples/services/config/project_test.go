package config

import (
	"context"
	"github.com/Newton-School/gogo/core/management"
	"io"
	"testing"
)

func TestMigrationPlan(t *testing.T) {
	p := Project()
	p.Root = ".."
	if err := management.Call(context.Background(), p, []string{"makemigrations", "catalog", "--check"}, management.Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
}
