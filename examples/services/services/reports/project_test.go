package main

import (
	"strings"
	"testing"
)

func TestReportSelectsResourcesPerCommand(t *testing.T) {
	p := Project()
	if _, err := p.Schema.Load(map[string]string{"GOGO_DATABASE_URL": "postgres://localhost/example"}, p.RuntimeResources...); err != nil {
		t.Fatal(err)
	}
	command := p.Commands[0]
	if command.Name != "report" || !command.OpenResources {
		t.Fatal("report command missing")
	}
	_, err := p.Schema.Load(nil, command.Resources...)
	if err == nil || !strings.Contains(err.Error(), "GOGO_DATABASE_URL") || !strings.Contains(err.Error(), "GOGO_REDIS_URL") || strings.Contains(err.Error(), "SECRET_KEY") {
		t.Fatal(err)
	}
}
