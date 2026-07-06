package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/conf"
	gogohttp "github.com/Newton-School/gogo/http"
)

// RunserverConfig contains resolved settings for the development server.
type RunserverConfig struct {
	Addr         string
	SettingsPath string
	Reload       bool
	Settings     conf.Settings
}

// ServerStarter starts an HTTP server with the resolved runserver config.
type ServerStarter func(context.Context, RunserverConfig) error

// NewRunserverCommand creates the runserver command.
func NewRunserverCommand(starter ServerStarter) Command {
	if starter == nil {
		starter = defaultServerStarter
	}

	return runserverCommand{starter: starter}
}

type runserverCommand struct {
	starter ServerStarter
}

func (c runserverCommand) Name() string {
	return "runserver"
}

func (c runserverCommand) Summary() string {
	return "Run the development server"
}

func (c runserverCommand) Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("runserver", flag.ContinueOnError)
	addr := flags.String("addr", "", "address to bind")
	settingsPath := flags.String("settings", "", "settings file path")
	reload := flags.Bool("reload", false, "enable development reload")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	positionals := flags.Args()
	if len(positionals) > 1 {
		return fmt.Errorf("%w: usage runserver [addr] [--addr addr]", ErrInvalidArguments)
	}

	settings, err := loadRunserverSettings(*settingsPath)
	if err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}

	resolvedAddr := settings.HTTPAddr
	if len(positionals) == 1 && positionals[0] != "" {
		resolvedAddr = positionals[0]
	}
	if *addr != "" {
		resolvedAddr = *addr
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	return c.starter(runCtx, RunserverConfig{
		Addr:         resolvedAddr,
		SettingsPath: *settingsPath,
		Reload:       *reload,
		Settings:     settings,
	})
}

func loadRunserverSettings(settingsPath string) (conf.Settings, error) {
	if settingsPath == "" {
		return conf.LoadFromEnv()
	}

	values, err := conf.LoadEnvFile(settingsPath)
	if err != nil {
		return conf.Settings{}, err
	}

	return conf.SettingsFromMap(values), nil
}

func defaultServerStarter(ctx context.Context, config RunserverConfig) error {
	settings := config.Settings
	settings.HTTPAddr = config.Addr

	middleware, err := gogohttp.BuildMiddleware(settings, gogohttp.BuiltInMiddlewareRegistry(os.Stdout))
	if err != nil {
		return err
	}

	server, err := gogohttp.NewServer(gogohttp.ServerConfig{
		Settings:   settings,
		Registry:   app.NewRegistry(),
		Router:     gogohttp.NewRouter(),
		Middleware: middleware,
	})
	if err != nil {
		return err
	}

	return server.ListenAndServe(ctx)
}
