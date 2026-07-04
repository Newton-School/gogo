package checks

import (
	"context"
	"reflect"
	"testing"
)

func TestCheckRegistrationFilteringAndSeverityThresholds(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Check{ID: "models.E001", Tags: []string{"models"}, Severity: SeverityError, Message: "model error"})
	registry.Register(Check{ID: "security.W001", Tags: []string{"security"}, Severity: SeverityWarning, Message: "security warning"})
	results := registry.Run(context.Background(), Options{Tags: []string{"models"}, MinimumSeverity: SeverityDebug})
	if len(results) != 1 || results[0].ID != "models.E001" {
		t.Fatalf("filtered results = %#v", results)
	}
	if !HasFailures(results, SeverityError) || HasFailures(results, SeverityCritical) {
		t.Fatalf("failure threshold mismatch")
	}
	if got := resultIDs(DefaultRegistry().Run(context.Background(), Options{})); !contains(got, "settings.I001") || !contains(got, "queue.I001") {
		t.Fatalf("default check IDs = %#v", got)
	}
}

func TestResultOrdering(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Check{ID: "b.W001", Severity: SeverityWarning})
	registry.Register(Check{ID: "a.W001", Severity: SeverityWarning})
	got := resultIDs(registry.Run(context.Background(), Options{}))
	if !reflect.DeepEqual(got, []string{"a.W001", "b.W001"}) {
		t.Fatalf("ordered IDs = %#v", got)
	}
}

func TestContribChecksCatchDependenciesSettingsUnsafeRedirectsAndDialect(t *testing.T) {
	results := ContribChecks(ContribConfig{
		InstalledApps:        []string{"gogo.contrib.redirects", "gogo.contrib.flatpages", "gogo.contrib.postgres", "gogo.contrib.postgres.vector", "gogo.contrib.gis"},
		Middleware:           []string{"gogo.contrib.redirects.Middleware", "gogo.contrib.sites.Middleware"},
		SiteID:               0,
		AllowUnsafeRedirects: true,
		DatabaseDialect:      "sqlite",
	})
	ids := resultIDs(results)
	for _, want := range []string{"contrib.E001", "contrib.E002", "contrib.E003", "contrib.E004", "contrib.E005", "contrib.E006", "contrib.E007", "contrib.E008", "contrib.W001"} {
		if !contains(ids, want) {
			t.Fatalf("contrib check IDs = %#v, missing %s", ids, want)
		}
	}

	valid := ContribChecks(ContribConfig{
		InstalledApps:        []string{"gogo.contrib.sites", "gogo.contrib.redirects", "gogo.contrib.flatpages", "gogo.contrib.postgres", "gogo.contrib.postgres.vector", "gogo.contrib.gis"},
		Middleware:           []string{"gogo.contrib.sites.Middleware", "gogo.messages.Middleware", "gogo.contrib.flatpages.Middleware", "gogo.contrib.redirects.Middleware"},
		SiteID:               1,
		DatabaseDialect:      "postgres",
		DatabaseExtensions:   []string{"postgis", "pg_trgm", "vector"},
		AllowUnsafeRedirects: false,
	})
	if len(valid) != 0 {
		t.Fatalf("valid contrib checks = %#v", valid)
	}
}

func TestContribChecksWarnWhenVectorExtensionMissing(t *testing.T) {
	results := ContribChecks(ContribConfig{
		InstalledApps:      []string{"gogo.contrib.postgres.vector"},
		DatabaseDialect:    "postgres",
		DatabaseExtensions: []string{"pg_trgm"},
	})
	if got := resultIDs(results); !contains(got, "contrib.W003") {
		t.Fatalf("vector contrib check IDs = %#v", got)
	}
}

func TestRegisterContribChecksAddsDynamicCheck(t *testing.T) {
	registry := NewRegistry()
	RegisterContribChecks(registry, ContribConfig{
		InstalledApps:   []string{"gogo.contrib.gis"},
		DatabaseDialect: "sqlite",
	})
	results := registry.Run(context.Background(), Options{Tags: []string{"contrib"}})
	if got := resultIDs(results); !contains(got, "contrib.E006") {
		t.Fatalf("registered contrib check IDs = %#v", got)
	}
}

func resultIDs(results []Result) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
