package management

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/conf"
	gogohttp "github.com/Newton-School/gogo/http"
	"github.com/Newton-School/gogo/internal/cli"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
	"github.com/Newton-School/gogo/queue"
)

// Command is the public management command contract for generated projects.
type Command interface {
	Name() string
	Summary() string
	Run(context.Context, []string) error
}

// Project connects generated client project wiring to management commands.
type Project struct {
	Settings      func() conf.Settings
	AppConfigs    func() []app.Config
	ModelMetadata func() []models.Metadata
	Router        func() (*gogohttp.Router, error)
	QueueApp      func() *queue.App
	Migrations    func() []migrations.Migration
	Commands      func() []Command
	Checks        func() []checks.Check
	Middleware    func(conf.Settings) (gogohttp.MiddlewareRegistry, error)
	ServerConfig  func(conf.Settings) gogohttp.ServerConfig
	Ready         func(context.Context) error
	Shutdown      func(context.Context) error
}

// Execute runs the Gogo management command registry.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return cli.NewRoot().Execute(ctx, args, stdout, stderr)
}

// ExecuteProject runs management commands with generated project wiring.
func ExecuteProject(ctx context.Context, args []string, stdout, stderr io.Writer, project Project) error {
	root := cli.NewRootWithOptions(cli.RootOptions{
		RunserverStarter:  project.serverStarter(stdout),
		AuthStore:         project.authStore(context.Background()),
		QueueRuntime:      project.queueRuntime(),
		FixtureStore:      project.fixtureStore(context.Background()),
		ProjectChecks:     project.checks(),
		ProjectMigrations: project.migrations(),
		ProjectModels:     project.modelMetadata(),
	})
	for _, command := range project.commands() {
		if err := root.Register(command); err != nil {
			return err
		}
	}
	return root.Execute(ctx, args, stdout, stderr)
}

// Main runs management commands using os.Args and exits with a process status.
func Main() {
	if err := Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// MainProject runs project-aware management commands using os.Args.
func MainProject(project Project) {
	if err := ExecuteProject(context.Background(), os.Args[1:], os.Stdout, os.Stderr, project); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (p Project) queueRuntime() *cli.QueueRuntime {
	if p.QueueApp == nil {
		return nil
	}
	runtime := cli.NewQueueRuntime()
	if app := p.QueueApp(); app != nil {
		runtime.App = app
	}
	return runtime
}

func (p Project) commands() []cli.Command {
	if p.Commands == nil {
		return nil
	}
	commands := p.Commands()
	out := make([]cli.Command, 0, len(commands))
	for _, command := range commands {
		out = append(out, command)
	}
	return out
}

func (p Project) migrations() []migrations.Migration {
	if p.Migrations == nil {
		return nil
	}
	return append([]migrations.Migration(nil), p.Migrations()...)
}

func (p Project) modelMetadata() []models.Metadata {
	if p.ModelMetadata == nil {
		return nil
	}
	return append([]models.Metadata(nil), p.ModelMetadata()...)
}

func (p Project) checks() []checks.Check {
	if p.Checks == nil {
		return nil
	}
	return append([]checks.Check(nil), p.Checks()...)
}

type projectAuthStore interface {
	Add(auth.User) error
	FindByUsername(context.Context, string) (auth.User, bool, error)
	UpdateUser(context.Context, auth.User) error
}

func (p Project) authStore(ctx context.Context) projectAuthStore {
	if p.ModelMetadata == nil {
		return nil
	}
	settings, err := conf.LoadFromEnv()
	if err != nil {
		return errorAuthStore{err: err}
	}
	if p.Settings != nil {
		settings = mergeSettings(p.Settings(), settings)
	}
	database, err := orm.OpenDatabaseURL(ctx, orm.DefaultDatabase, settings.DatabaseURL)
	if err != nil {
		return errorAuthStore{err: err}
	}
	return auth.NewSQLUserStore(database)
}

type errorAuthStore struct {
	err error
}

func (s errorAuthStore) Add(auth.User) error {
	return s.err
}

func (s errorAuthStore) FindByUsername(context.Context, string) (auth.User, bool, error) {
	return auth.User{}, false, s.err
}

func (s errorAuthStore) UpdateUser(context.Context, auth.User) error {
	return s.err
}

func (p Project) fixtureStore(ctx context.Context) cli.FixtureStore {
	if p.ModelMetadata == nil {
		return nil
	}
	settings, err := conf.LoadFromEnv()
	if err != nil {
		return cli.NewErrorFixtureStore(err)
	}
	if p.Settings != nil {
		settings = mergeSettings(p.Settings(), settings)
	}
	database, err := orm.OpenDatabaseURL(ctx, orm.DefaultDatabase, settings.DatabaseURL)
	if err != nil {
		return cli.NewErrorFixtureStore(err)
	}
	return cli.NewMetadataFixtureStore(orm.NewMetadataStore(database, p.ModelMetadata()...))
}

func (p Project) serverStarter(accessLog io.Writer) cli.ServerStarter {
	if p.Router == nil && p.AppConfigs == nil && p.Settings == nil && p.Middleware == nil && p.ServerConfig == nil && p.Ready == nil && p.Shutdown == nil {
		return nil
	}
	return func(ctx context.Context, config cli.RunserverConfig) error {
		server, err := p.buildServer(ctx, accessLog, config)
		if err != nil {
			return err
		}
		projectReady := true
		if p.Ready != nil {
			if err := p.Ready(ctx); err != nil {
				return err
			}
		}
		err = server.ListenAndServe(ctx)
		if projectReady && p.Shutdown != nil {
			if shutdownErr := p.Shutdown(context.Background()); err == nil {
				err = shutdownErr
			}
		}
		return err
	}
}

func (p Project) buildServer(_ context.Context, accessLog io.Writer, config cli.RunserverConfig) (*gogohttp.Server, error) {
	settings := config.Settings
	if p.Settings != nil {
		settings = mergeSettings(p.Settings(), config.Settings)
	}
	settings.HTTPAddr = config.Addr

	registry := app.NewRegistry()
	if p.AppConfigs != nil {
		for _, config := range p.AppConfigs() {
			if err := registry.Register(config); err != nil {
				return nil, err
			}
		}
	}

	router := gogohttp.NewRouter()
	if p.Router != nil {
		resolved, err := p.Router()
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			router = resolved
		}
	}

	middlewareRegistry := gogohttp.BuiltInMiddlewareRegistry(accessLog)
	if p.Middleware != nil {
		projectRegistry, err := p.Middleware(settings)
		if err != nil {
			return nil, err
		}
		for name, factory := range projectRegistry {
			middlewareRegistry[name] = factory
		}
	}
	middleware, err := gogohttp.BuildMiddleware(settings, middlewareRegistry)
	if err != nil {
		return nil, err
	}

	serverConfig := gogohttp.ServerConfig{}
	if p.ServerConfig != nil {
		serverConfig = p.ServerConfig(settings)
	}
	serverConfig.Settings = settings
	if serverConfig.Registry == nil {
		serverConfig.Registry = registry
	}
	if serverConfig.Router == nil {
		serverConfig.Router = router
	}
	if serverConfig.Middleware == nil {
		serverConfig.Middleware = middleware
	}
	return gogohttp.NewServer(serverConfig)
}

func mergeSettings(projectSettings conf.Settings, loaded conf.Settings) conf.Settings {
	merged := projectSettings
	if loaded.Env != "" {
		merged.Env = loaded.Env
	}
	if loaded.SecretKey != "" {
		merged.SecretKey = loaded.SecretKey
	}
	merged.Debug = loaded.Debug
	if len(loaded.AllowedHosts) > 0 {
		merged.AllowedHosts = loaded.AllowedHosts
	}
	if loaded.HTTPAddr != "" {
		merged.HTTPAddr = loaded.HTTPAddr
	}
	if loaded.DatabaseURL != "" {
		merged.DatabaseURL = loaded.DatabaseURL
	}
	if len(loaded.InstalledApps) > 0 {
		merged.InstalledApps = loaded.InstalledApps
	}
	if len(loaded.Middleware) > 0 {
		merged.Middleware = loaded.Middleware
	}
	if loaded.RootURLConf != "" && loaded.RootURLConf != conf.DefaultSettings().RootURLConf {
		merged.RootURLConf = loaded.RootURLConf
	}
	if loaded.StaticURL != "" {
		merged.StaticURL = loaded.StaticURL
	}
	if loaded.StaticRoot != "" {
		merged.StaticRoot = loaded.StaticRoot
	}
	if loaded.MediaURL != "" {
		merged.MediaURL = loaded.MediaURL
	}
	if loaded.MediaRoot != "" {
		merged.MediaRoot = loaded.MediaRoot
	}
	if len(loaded.TemplateDirs) > 0 {
		merged.TemplateDirs = loaded.TemplateDirs
	}
	if loaded.DefaultAutoField != "" {
		merged.DefaultAutoField = loaded.DefaultAutoField
	}
	if loaded.TimeZone != "" {
		merged.TimeZone = loaded.TimeZone
	}
	if loaded.LanguageCode != "" {
		merged.LanguageCode = loaded.LanguageCode
	}
	if loaded.SessionCookieName != "" {
		merged.SessionCookieName = loaded.SessionCookieName
	}
	merged.SessionCookieSecure = loaded.SessionCookieSecure
	if loaded.CSRFCookieName != "" {
		merged.CSRFCookieName = loaded.CSRFCookieName
	}
	merged.CSRFCookieSecure = loaded.CSRFCookieSecure
	merged.HTTPSEnabled = loaded.HTTPSEnabled
	if len(loaded.CSRFTrustedOrigins) > 0 {
		merged.CSRFTrustedOrigins = loaded.CSRFTrustedOrigins
	}
	if loaded.AdminPath != "" {
		merged.AdminPath = loaded.AdminPath
	}
	merged.AdminPathReviewed = loaded.AdminPathReviewed
	merged.MigrationsApplied = loaded.MigrationsApplied
	merged.StaticFilesCollected = loaded.StaticFilesCollected
	merged.PasswordResetEnabled = loaded.PasswordResetEnabled
	if loaded.BrokerURL != "" {
		merged.BrokerURL = loaded.BrokerURL
	}
	if loaded.ResultBackend != "" {
		merged.ResultBackend = loaded.ResultBackend
	}
	if loaded.CacheURL != "" {
		merged.CacheURL = loaded.CacheURL
	}
	if loaded.EmailURL != "" {
		merged.EmailURL = loaded.EmailURL
	}
	return merged
}
