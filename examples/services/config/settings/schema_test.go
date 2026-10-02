package settings

import (
	"os"
	"strings"
	"testing"
)

func TestEnvironmentExampleTracksSchema(t *testing.T) {
	data, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	core, _, ok := strings.Cut(string(data), "# Docker bootstrap")
	if !ok || core != Schema().EnvTemplate()+"\n" {
		t.Fatal("environment template differs from framework defaults")
	}
}
