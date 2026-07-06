package testing

import "github.com/Newton-School/gogo/conf"

type SettingsOption func(*conf.Settings)

type SettingsOverride struct {
	target   *conf.Settings
	original conf.Settings
	restored bool
}

type CleanupHelper interface {
	Helper()
	Cleanup(func())
}

func OverrideSettings(target *conf.Settings, options ...SettingsOption) *SettingsOverride {
	if target == nil {
		return &SettingsOverride{restored: true}
	}
	original := cloneSettings(*target)
	next := cloneSettings(*target)
	for _, option := range options {
		if option != nil {
			option(&next)
		}
	}
	*target = next
	return &SettingsOverride{target: target, original: original}
}

func OverrideSettingsForTest(t CleanupHelper, target *conf.Settings, options ...SettingsOption) *SettingsOverride {
	if t != nil {
		t.Helper()
	}
	override := OverrideSettings(target, options...)
	if t != nil {
		t.Cleanup(override.Restore)
	}
	return override
}

func TemporarySettings(base conf.Settings, options ...SettingsOption) conf.Settings {
	settings := cloneSettings(base)
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	return settings
}

func (o *SettingsOverride) Restore() {
	if o == nil || o.restored || o.target == nil {
		return
	}
	*o.target = cloneSettings(o.original)
	o.restored = true
}

func WithInstalledApps(apps ...string) SettingsOption {
	return func(settings *conf.Settings) {
		settings.InstalledApps = append([]string(nil), apps...)
	}
}

func WithMiddleware(middleware ...string) SettingsOption {
	return func(settings *conf.Settings) {
		settings.Middleware = append([]string(nil), middleware...)
	}
}

func WithDatabaseURL(databaseURL string) SettingsOption {
	return func(settings *conf.Settings) {
		settings.DatabaseURL = databaseURL
	}
}

func WithTemporaryDatabase(database *TestDatabase) SettingsOption {
	return func(settings *conf.Settings) {
		if database != nil && database.Database != nil {
			settings.DatabaseURL = database.Database.DSN
		}
	}
}

func WithTemplateDirs(dirs ...string) SettingsOption {
	return func(settings *conf.Settings) {
		settings.TemplateDirs = append([]string(nil), dirs...)
	}
}

func cloneSettings(settings conf.Settings) conf.Settings {
	settings.AllowedHosts = append([]string(nil), settings.AllowedHosts...)
	settings.InstalledApps = append([]string(nil), settings.InstalledApps...)
	settings.Middleware = append([]string(nil), settings.Middleware...)
	settings.TemplateDirs = append([]string(nil), settings.TemplateDirs...)
	return settings
}
