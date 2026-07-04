package checks

import (
	"context"
	"sort"
	"strings"
)

const (
	ContribSites          = "gogo.contrib.sites"
	ContribRedirects      = "gogo.contrib.redirects"
	ContribFlatpages      = "gogo.contrib.flatpages"
	ContribSitemaps       = "gogo.contrib.sitemaps"
	ContribSyndication    = "gogo.contrib.syndication"
	ContribHumanize       = "gogo.contrib.humanize"
	ContribAdminDocs      = "gogo.contrib.admindocs"
	ContribPostgres       = "gogo.contrib.postgres"
	ContribPostgresVector = "gogo.contrib.postgres.vector"
	ContribGIS            = "gogo.contrib.gis"
)

type ContribConfig struct {
	InstalledApps        []string
	Middleware           []string
	SiteID               int
	DatabaseDialect      string
	DatabaseExtensions   []string
	AllowUnsafeRedirects bool
}

func RegisterContribChecks(registry *Registry, config ContribConfig) {
	if registry == nil {
		return
	}
	for _, result := range ContribChecks(config) {
		result := result
		registry.Register(Check{
			ID:       result.ID,
			Tags:     result.Tags,
			Severity: result.Severity,
			Message:  result.Message,
			Hint:     result.Hint,
			Object:   result.Object,
			Run: func(context.Context) Result {
				return result
			},
		})
	}
}

func ContribChecks(config ContribConfig) []Result {
	installed := installedSet(config.InstalledApps)
	extensions := extensionSet(config.DatabaseExtensions)
	var results []Result

	if installed[ContribRedirects] && !installed[ContribSites] {
		results = append(results, contribResult("contrib.E001", SeverityError, "redirects requires sites", "Add gogo.contrib.sites before gogo.contrib.redirects in InstalledApps.", ContribRedirects))
	}
	if installed[ContribFlatpages] && !installed[ContribSites] {
		results = append(results, contribResult("contrib.E002", SeverityError, "flatpages requires sites", "Add gogo.contrib.sites before gogo.contrib.flatpages in InstalledApps.", ContribFlatpages))
	}
	if siteDependent(installed) && config.SiteID <= 0 {
		results = append(results, contribResult("contrib.E003", SeverityError, "SITE_ID must be positive for site-aware contrib apps", "Set SITE_ID to an existing sites.Site row before enabling site-aware contrib apps.", "SITE_ID"))
	}
	if middlewareOrderInvalid(config.Middleware, "gogo.contrib.sites.Middleware", []string{"gogo.contrib.redirects.Middleware", "gogo.contrib.flatpages.Middleware"}) {
		results = append(results, contribResult("contrib.E004", SeverityError, "site middleware must run before site-aware contrib middleware", "Place gogo.contrib.sites.Middleware before flatpages and redirects middleware.", "Middleware"))
	}
	if installed[ContribPostgres] && !postgresDialect(config.DatabaseDialect) {
		results = append(results, contribResult("contrib.E005", SeverityError, "postgres contrib requires PostgreSQL", "Use the postgres/postgresql dialect before enabling gogo.contrib.postgres helpers.", ContribPostgres))
	}
	if installed[ContribGIS] && !postgresDialect(config.DatabaseDialect) {
		results = append(results, contribResult("contrib.E006", SeverityError, "GIS contrib requires PostgreSQL with PostGIS", "Use the postgres/postgresql dialect before enabling gogo.contrib.gis.", ContribGIS))
	}
	if installed[ContribGIS] && !extensions["postgis"] {
		results = append(results, contribResult("contrib.E007", SeverityError, "GIS contrib requires the postgis extension", "Enable CREATE EXTENSION postgis in the application database.", "postgis"))
	}
	if installed[ContribPostgresVector] && !postgresDialect(config.DatabaseDialect) {
		results = append(results, contribResult("contrib.E008", SeverityError, "vector contrib requires PostgreSQL with pgvector", "Use the postgres/postgresql dialect before enabling gogo.contrib.postgres.vector helpers.", ContribPostgresVector))
	}
	if config.AllowUnsafeRedirects {
		results = append(results, contribResult("contrib.W001", SeverityWarning, "unsafe absolute redirect targets are enabled", "Keep AllowUnsafeTargets disabled unless every redirect row is trusted and reviewed.", "AllowUnsafeTargets"))
	}
	if installed[ContribPostgres] && postgresDialect(config.DatabaseDialect) && !extensions["pg_trgm"] {
		results = append(results, contribResult("contrib.W002", SeverityWarning, "pg_trgm extension is not reported", "Enable pg_trgm before using trigram similarity and distance helpers.", "pg_trgm"))
	}
	if installed[ContribPostgresVector] && postgresDialect(config.DatabaseDialect) && !extensions["vector"] {
		results = append(results, contribResult("contrib.W003", SeverityWarning, "vector extension is not reported", "Enable CREATE EXTENSION vector before using pgvector fields and indexes.", "vector"))
	}

	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results
}

func contribResult(id string, severity Severity, message string, hint string, object string) Result {
	return Result{ID: id, Tags: []string{"contrib"}, Severity: severity, Message: message, Hint: hint, Object: object}
}

func siteDependent(installed map[string]bool) bool {
	return installed[ContribSites] || installed[ContribRedirects] || installed[ContribFlatpages]
}

func installedSet(apps []string) map[string]bool {
	installed := make(map[string]bool, len(apps))
	for _, app := range apps {
		normalized := strings.TrimSpace(app)
		if normalized == "" {
			continue
		}
		installed[normalized] = true
	}
	return installed
}

func extensionSet(extensions []string) map[string]bool {
	set := make(map[string]bool, len(extensions))
	for _, extension := range extensions {
		normalized := strings.ToLower(strings.TrimSpace(extension))
		if normalized != "" {
			set[normalized] = true
		}
	}
	return set
}

func postgresDialect(dialect string) bool {
	return dialect == "postgres" || dialect == "postgresql"
}

func middlewareOrderInvalid(middleware []string, requiredBefore string, dependents []string) bool {
	requiredIndex := middlewareIndex(middleware, requiredBefore)
	if requiredIndex < 0 {
		return false
	}
	for _, dependent := range dependents {
		dependentIndex := middlewareIndex(middleware, dependent)
		if dependentIndex >= 0 && dependentIndex < requiredIndex {
			return true
		}
	}
	return false
}

func middlewareIndex(middleware []string, name string) int {
	for index, candidate := range middleware {
		if strings.TrimSpace(candidate) == name {
			return index
		}
	}
	return -1
}
