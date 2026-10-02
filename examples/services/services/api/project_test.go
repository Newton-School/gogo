package main

import (
	"strings"
	"testing"
)

func TestAPIRequiresOnlyDatabase(t *testing.T) {
	p := Project()
	_, err := p.Schema.Load(nil, p.RuntimeResources...)
	if err == nil || !strings.Contains(err.Error(), "GOGO_DATABASE_URL") || strings.Contains(err.Error(), "REDIS") || strings.Contains(err.Error(), "SECRET_KEY") {
		t.Fatal(err)
	}
	if _, err := p.Schema.Load(map[string]string{"GOGO_DATABASE_URL": "postgres://localhost/example"}, p.RuntimeResources...); err != nil {
		t.Fatal(err)
	}
	if len(p.Commands) != 0 || len(p.Apps) != 1 {
		t.Fatal("unrelated components installed")
	}
}
