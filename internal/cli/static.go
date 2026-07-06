package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/static"
)

// StaticCollector runs one collectstatic operation.
type StaticCollector func(context.Context, static.CollectOptions) (static.CollectResult, error)

// NewCollectstaticCommand creates the collectstatic command.
func NewCollectstaticCommand(collector StaticCollector) Command {
	if collector == nil {
		collector = static.Collect
	}
	return collectstaticCommand{collector: collector}
}

type collectstaticCommand struct {
	collector StaticCollector
}

func (c collectstaticCommand) Name() string {
	return "collectstatic"
}

func (c collectstaticCommand) Summary() string {
	return "Collect static files"
}

func (c collectstaticCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c collectstaticCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	options, err := parseCollectstaticFlags(args)
	if err != nil {
		return err
	}
	result, err := c.collector(ctx, options)
	if err != nil {
		return fmt.Errorf("%w: collect static files: %v", ErrCommandFailed, err)
	}
	verb := "collected"
	if options.DryRun {
		verb = "would collect"
	}
	if _, err := fmt.Fprintf(stdout, "%s %d static files\n", verb, len(result.Copied)); err != nil {
		return fmt.Errorf("%w: write collectstatic output: %v", ErrCommandFailed, err)
	}
	return nil
}

func parseCollectstaticFlags(args []string) (static.CollectOptions, error) {
	flags := flag.NewFlagSet("collectstatic", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var projectDirs repeatedFlag
	var appDirs repeatedFlag
	options := static.CollectOptions{}
	flags.StringVar(&options.Destination, "dest", "", "collection destination")
	flags.BoolVar(&options.Manifest, "manifest", false, "write hashed manifest")
	flags.BoolVar(&options.Clear, "clear", false, "clear destination before collecting")
	flags.BoolVar(&options.DryRun, "dry-run", false, "discover files without writing output")
	flags.Var(&projectDirs, "project-dir", "project static directory")
	flags.Var(&appDirs, "app-dir", "app static directory")
	if err := flags.Parse(args); err != nil {
		return static.CollectOptions{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	options.Finder.ProjectDirs = []string(projectDirs)
	options.Finder.AppDirs = []string(appDirs)
	applyCollectstaticDefaults(&options)
	return options, nil
}

func applyCollectstaticDefaults(options *static.CollectOptions) {
	settings, err := conf.LoadFromEnv()
	if err == nil {
		if options.Destination == "" {
			options.Destination = settings.StaticRoot
		}
	}
	if len(options.Finder.ProjectDirs) == 0 {
		if _, err := os.Stat("static"); err == nil {
			options.Finder.ProjectDirs = []string{"static"}
		}
	}
	if len(options.Finder.AppDirs) == 0 {
		options.Finder.AppDirs = discoverAppStaticDirs("apps")
	}
}

func discoverAppStaticDirs(appsRoot string) []string {
	entries, err := os.ReadDir(appsRoot)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		staticDir := filepath.Join(appsRoot, entry.Name(), "static")
		if stat, err := os.Stat(staticDir); err == nil && stat.IsDir() {
			dirs = append(dirs, staticDir)
		}
	}
	return dirs
}

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	return fmt.Sprint([]string(*f))
}

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}
