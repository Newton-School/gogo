package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/internal/release"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

// NewCheckCommand creates the built-in system check command.
func NewCheckCommand(projectChecks ...checks.Check) Command {
	return checkCommand{projectChecks: append([]checks.Check(nil), projectChecks...)}
}

func NewCheckCommandWithProject(projectModels []models.Metadata, projectMigrations []migrations.Migration, projectChecks ...checks.Check) Command {
	return checkCommand{
		projectChecks:     append([]checks.Check(nil), projectChecks...),
		projectModels:     append([]models.Metadata(nil), projectModels...),
		projectMigrations: append([]migrations.Migration(nil), projectMigrations...),
	}
}

type checkCommand struct {
	projectChecks     []checks.Check
	projectModels     []models.Metadata
	projectMigrations []migrations.Migration
}

func (c checkCommand) Name() string {
	return "check"
}

func (c checkCommand) Summary() string {
	return "Run system checks"
}

func (c checkCommand) Run(ctx context.Context, args []string) error {
	settings, err := conf.LoadFromEnv()
	if err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	options, failLevel, deploy, err := parseCheckFlags(args)
	if err != nil {
		return err
	}
	results := c.registry().Run(ctx, options)
	if deploy {
		results = append(results, release.RunDeployChecks(release.BuildDeployConfig(ctx, settings))...)
	}
	if checks.HasFailures(results, failLevel) {
		return ErrCommandFailed
	}
	return nil
}

func (c checkCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	options, failLevel, deploy, err := parseCheckFlags(args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	settings, err := conf.LoadFromEnv()
	if err != nil {
		return err
	}

	if err := settings.Validate(); err != nil {
		fmt.Fprintf(stdout, "ERROR config %v\n", err)
		return err
	}

	fmt.Fprintln(stdout, "OK config settings valid")
	results := c.registry().Run(ctx, options)
	if deploy {
		results = append(results, release.RunDeployChecks(release.BuildDeployConfig(ctx, settings))...)
	}
	for _, result := range results {
		tag := "general"
		if len(result.Tags) > 0 {
			tag = result.Tags[0]
		}
		fmt.Fprintf(stdout, "%s %s %s\n", result.Severity.String(), tag, result.Message)
		if result.Hint != "" {
			fmt.Fprintf(stdout, "HINT %s %s\n", result.ID, result.Hint)
		}
	}
	if checks.HasFailures(results, failLevel) {
		return ErrCommandFailed
	}

	return nil
}

func (c checkCommand) registry() *checks.Registry {
	registry := checks.DefaultRegistry()
	if len(c.projectModels) > 0 {
		registry.Register(checks.Check{
			ID:       "models.E001",
			Tags:     []string{"models"},
			Severity: checks.SeverityError,
			Message:  "project model metadata is invalid",
			Run: func(context.Context) checks.Result {
				if err := validateProjectModels(c.projectModels); err != nil {
					return checks.Result{ID: "models.E001", Tags: []string{"models"}, Severity: checks.SeverityError, Message: err.Error()}
				}
				return checks.Result{ID: "models.I002", Tags: []string{"models"}, Severity: checks.SeverityInfo, Message: "project model metadata valid"}
			},
		})
	}
	if len(c.projectMigrations) > 0 {
		registry.Register(checks.Check{
			ID:       "migrations.E001",
			Tags:     []string{"migrations"},
			Severity: checks.SeverityError,
			Message:  "project migration graph is invalid",
			Run: func(context.Context) checks.Result {
				if err := validateProjectMigrationGraph(c.projectMigrations); err != nil {
					return checks.Result{ID: "migrations.E001", Tags: []string{"migrations"}, Severity: checks.SeverityError, Message: err.Error()}
				}
				return checks.Result{ID: "migrations.I002", Tags: []string{"migrations"}, Severity: checks.SeverityInfo, Message: "project migration graph valid"}
			},
		})
	}
	for _, check := range c.projectChecks {
		registry.Register(check)
	}
	return registry
}

func validateProjectModels(metadata []models.Metadata) error {
	registry := models.NewRegistry()
	for _, meta := range metadata {
		if err := registry.RegisterMetadata(meta); err != nil {
			return err
		}
	}
	return registry.ValidateRelations()
}

func validateProjectMigrationGraph(projectMigrations []migrations.Migration) error {
	graph := migrations.NewGraph()
	ordered := append([]migrations.Migration(nil), projectMigrations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].AppLabel == ordered[j].AppLabel {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].AppLabel < ordered[j].AppLabel
	})
	for _, migration := range ordered {
		if err := migration.Validate(); err != nil {
			return err
		}
		if err := graph.Add(migration); err != nil {
			return err
		}
	}
	conflicts := graph.ConflictingLeaves()
	if len(conflicts) > 0 {
		for appLabel, leaves := range conflicts {
			names := make([]string, 0, len(leaves))
			for _, migration := range leaves {
				names = append(names, migration.Name)
			}
			return fmt.Errorf("%w: app %s has conflicting leaf migrations %s", migrations.ErrInvalidMigration, appLabel, strings.Join(names, ", "))
		}
	}
	return nil
}

func parseCheckFlags(args []string) (checks.Options, checks.Severity, bool, error) {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var tags string
	var failLevel string
	var deploy bool
	flags.StringVar(&tags, "tag", "", "comma-separated check tags")
	flags.StringVar(&failLevel, "fail-level", "ERROR", "minimum severity that fails the command")
	flags.BoolVar(&deploy, "deploy", false, "run production deployment checks")
	if err := flags.Parse(args); err != nil {
		return checks.Options{}, checks.SeverityError, false, err
	}
	level := parseSeverity(failLevel)
	return checks.Options{Tags: splitCheckTags(tags)}, level, deploy, nil
}

func splitCheckTags(tags string) []string {
	if tags == "" {
		return nil
	}
	parts := strings.Split(tags, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseSeverity(value string) checks.Severity {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return checks.SeverityDebug
	case "INFO":
		return checks.SeverityInfo
	case "WARN", "WARNING":
		return checks.SeverityWarning
	case "CRITICAL":
		return checks.SeverityCritical
	default:
		return checks.SeverityError
	}
}
