package config

import (
	"os"
	"strings"
	"testing"
)

func TestEnvironmentTemplateAndRequiredSettings(t *testing.T) {
	content, err := os.ReadFile("../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != Settings().EnvTemplate() {
		t.Fatal(".env.example differs from Settings declarations")
	}
	_, err = Settings().Load(nil, "database", "redis", "signing", "bootstrap")
	if err == nil {
		t.Fatal("required configuration was silently optional")
	}
	for _, name := range []string{"GOGO_DATABASE_URL", "GOGO_REDIS_URL", "GOGO_SECRET_KEY", "GOGO_SHOWCASE_ADMIN_PASSWORD", "GOGO_SHOWCASE_ADMIN_IDENTIFIER"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("missing requirement %s", name)
		}
	}
}

func TestModelRegistry(t *testing.T) {
	registry, err := ModelRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"catalog.Product", "catalog.ProductNote", "fieldlab.Specimen", "fieldlab.Related", "gogo_auth.User"} {
		if _, exists := registry.Get(key); !exists {
			t.Errorf("model not registered: %s", key)
		}
	}
}
