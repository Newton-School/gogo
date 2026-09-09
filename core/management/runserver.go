package management

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	ghttp "github.com/Newton-School/gogo/core/http"
)

// RunServerOptions selects the explicitly configured HTTP application. Reload is
// a development-only supervisor, not an in-process application reload.
type RunServerOptions struct {
	Address string
	Reload  bool
	// WatchDirectories adds at most 32 canonical project-relative directories
	// for assets embedded by the application. It does not serve their contents.
	WatchDirectories []string
}

type runServerEntryKey struct{}
type runServerEntry struct{ environment []string }

type runServerFailure struct {
	cause error
	code  int
}

func (*runServerFailure) Error() string                { return "development server operation failed" }
func (e *runServerFailure) Unwrap() error              { return e.cause }
func (e *runServerFailure) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, e.Error()) }
func serverFailure(err error) error {
	if err == nil {
		return nil
	}
	return &runServerFailure{cause: err}
}

// captureRunServerProject runs before registration or context callbacks. Only
// plain declarations are copied; callback handles retain their explicit owner.
func captureRunServerProject(p Project) (Project, *runServerEntry, error) {
	if len(p.Schema) > 1024 || len(p.Apps) > 1024 || len(p.Environment) > 4096 || len(p.RuntimeResources) > 128 {
		return Project{}, nil, serverFailure(errors.New("server configuration exceeds limits"))
	}
	if p.Root == "" {
		p.Root = "."
	}
	root, err := filepath.Abs(p.Root)
	if err != nil || len(root) > 4096 {
		return Project{}, nil, serverFailure(errors.New("invalid project root"))
	}
	p.Root = root
	if len(p.Schema) == 0 {
		p.Schema = conf.CoreSchema()
	}
	p.Schema = slices.Clone(p.Schema)
	budget := 1 << 20
	charge := func(s string) bool { budget -= len(s); return budget >= 0 && !strings.ContainsRune(s, 0) }
	for i := range p.Schema {
		d := &p.Schema[i]
		if len(d.RequiredFor) > 128 || len(d.Choices) > 1024 || !charge(d.Name) || !charge(d.Default) || !charge(d.Group) {
			return Project{}, nil, serverFailure(errors.New("invalid server schema"))
		}
		d.RequiredFor = slices.Clone(d.RequiredFor)
		d.Choices = slices.Clone(d.Choices)
		for _, s := range append(slices.Clone(d.RequiredFor), d.Choices...) {
			if !charge(s) {
				return Project{}, nil, serverFailure(errors.New("invalid server schema"))
			}
		}
	}
	p.Apps = slices.Clone(p.Apps)
	for i := range p.Apps {
		if len(p.Apps[i].Requires) > 1024 {
			return Project{}, nil, serverFailure(errors.New("invalid app dependencies"))
		}
		p.Apps[i].Requires = slices.Clone(p.Apps[i].Requires)
	}
	p.RuntimeResources = slices.Clone(p.RuntimeResources)
	p.Environment = conf.Merge(p.Environment)
	for k, v := range p.Environment {
		if !charge(k) || !charge(v) {
			return Project{}, nil, serverFailure(errors.New("invalid server environment"))
		}
	}
	env := os.Environ()
	if len(env) > 8192 {
		return Project{}, nil, serverFailure(errors.New("server environment exceeds limits"))
	}
	for _, value := range env {
		if !charge(value) {
			return Project{}, nil, serverFailure(errors.New("server environment exceeds limits"))
		}
	}
	return p, &runServerEntry{environment: env}, nil
}

func runServerCommands() []Command {
	return []Command{
		{Name: "serve", Help: "Serve the explicitly configured project", Resources: []string{"runtime"}, OpenResources: true, Validate: noArgs, Configure: func(f *flag.FlagSet) Runner {
			address := f.String("addr", "", "listen address override")
			return func(ctx context.Context, i *Invocation, _ []string) error {
				return runPlainServer(ctx, *i, *address, false)
			}
		}},
		{Name: "runserver", Help: "Run the development server, optionally rebuilding on edits", Resources: []string{"runtime"}, Validate: noArgs, Configure: func(f *flag.FlagSet) Runner {
			address := f.String("addr", "", "listen address override")
			reload := f.Bool("reload", false, "rebuild and restart on development source changes")
			var watch runServerWatchFlags
			f.Var(&watch, "watch", "extra project-relative directory to watch (repeatable; requires reload)")
			return func(ctx context.Context, i *Invocation, _ []string) error {
				return RunServer(ctx, i, RunServerOptions{Address: *address, Reload: *reload, WatchDirectories: slices.Clone(watch)})
			}
		}},
	}
}

type runServerWatchFlags []string

func (f *runServerWatchFlags) String() string { return "" }
func (f *runServerWatchFlags) Set(s string) error {
	if len(*f) >= 32 || !reloadRelative(s) {
		return errors.New("invalid watch directory")
	}
	*f = append(*f, s)
	return nil
}

// RunServer serves a prepared invocation, or supervises fresh generated manage
// children without opening resources in the parent. Call owns Application.Close;
// direct callers must close their prepared Application after this method returns.
// Reload supports Linux and macOS and owns only its direct child/tool processes.
func RunServer(ctx context.Context, invocation *Invocation, options RunServerOptions) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("server callback panicked")
		}
		err = serverFailure(err)
	}()
	if invocation == nil || invocation.Project == nil || invocation.Application == nil {
		return errors.New("prepared server invocation required")
	}
	i := *invocation
	opts := options
	if len(opts.WatchDirectories) > 32 || (!opts.Reload && len(opts.WatchDirectories) != 0) {
		return errors.New("invalid server options")
	}
	opts.WatchDirectories = slices.Clone(opts.WatchDirectories)
	for _, dir := range opts.WatchDirectories {
		if !reloadRelative(dir) {
			return errors.New("invalid watch directory")
		}
	}
	p, entry, e := captureRunServerProject(*i.Project)
	if e != nil {
		return e
	}
	i.Project = &p
	if i.Stdout == nil {
		i.Stdout = io.Discard
	}
	if i.Stderr == nil {
		i.Stderr = io.Discard
	}
	opts.Address = runServerAddress(opts.Address, i.Settings)
	if len(opts.Address) == 0 || len(opts.Address) > 1024 || strings.ContainsAny(opts.Address, "\x00\r\n") {
		return errors.New("invalid listen address")
	}
	if e = reloadContextError(ctx); e != nil {
		return e
	}
	if inherited, ok := ctx.Value(runServerEntryKey{}).(*runServerEntry); ok && inherited != nil {
		entry = &runServerEntry{environment: slices.Clone(inherited.environment)}
	}
	if e = reloadContextError(ctx); e != nil {
		return e
	}
	if opts.Reload {
		if !reloadSupported || i.Settings.String("GOGO_ENV") == "production" {
			return errors.New("reload is unavailable in this environment")
		}
		return runReload(ctx, i, opts, entry)
	}
	resources := slices.Clone(p.RuntimeResources)
	var selected []app.Resource
	if p.ResourceFactory != nil {
		selected, err = p.ResourceFactory(i.Settings, resources)
		if err != nil {
			return err
		}
	} else if len(resources) > 0 {
		return errors.New("selected resources require a resource factory")
	}
	if err = reloadContextError(ctx); err != nil {
		return err
	}
	if err = i.Application.Start(ctx, selected); err != nil {
		return err
	}
	return runPlainServer(ctx, i, opts.Address, true)
}

func runPlainServer(ctx context.Context, i Invocation, address string, announce bool) error {
	address = runServerAddress(address, i.Settings)
	if i.Project.Handler == nil {
		return errors.New("project HTTP handler required")
	}
	config := ghttp.ServerConfig{Address: address, ReadHeaderTimeout: i.Settings.Duration("GOGO_HTTP_READ_HEADER_TIMEOUT"), IdleTimeout: i.Settings.Duration("GOGO_HTTP_IDLE_TIMEOUT"), ShutdownGrace: i.Settings.Duration("GOGO_SHUTDOWN_GRACE")}
	handler, err := i.Project.Handler(i.Application.Registry, i.Settings)
	if err != nil {
		return err
	}
	if err = reloadContextError(ctx); err != nil {
		return err
	}
	config.Handler = handler
	server, err := ghttp.NewServer(config)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	if announce {
		if err = reloadMessage(i.Stdout, "Development server listening at "+listener.Addr().String()); err != nil {
			return err
		}
	}
	return server.Serve(ctx, listener)
}

func runServerAddress(override string, settings conf.Values) string {
	if override != "" {
		return override
	}
	if configured := settings.String("GOGO_HTTP_ADDR"); configured != "" {
		return configured
	}
	return "127.0.0.1:8000"
}

func reloadContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("invalid server context")
		}
	}()
	if ctx == nil {
		return errors.New("server context required")
	}
	err = ctx.Err()
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return errors.New("invalid server context")
	}
	if err == nil {
		select {
		case <-ctx.Done():
			return errors.New("invalid server context")
		default:
		}
	}
	return err
}
func reloadMessage(w io.Writer, message string) error {
	data := []byte(message + "\n")
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
func reloadGrace(settings conf.Values) (time.Duration, error) {
	grace := settings.Duration("GOGO_SHUTDOWN_GRACE")
	if grace <= 0 || grace > time.Minute {
		return 0, errors.New("reload shutdown grace must be at most one minute")
	}
	// HTTP drain and subsequent application cleanup each own a grace interval.
	return 2*grace + time.Second, nil
}
