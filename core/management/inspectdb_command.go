package management

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// InspectDBCommand opens only the project-declared database resource. Resolve
// runs lazily afterward and must select an explicitly configured alias; it must
// never interpret the alias as a DSN or create an unconfigured connection.
func InspectDBCommand(resolve func(string) (db.Backend, db.CatalogIntrospector, error)) Command {
	validName := func(name string) bool {
		return name != "" && len(name) <= 63 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
	}
	validateRelations := func(args []string) error {
		if len(args) > 1000 {
			return errors.New("inspectdb: select at most 1000 relations")
		}
		seen := map[string]bool{}
		for _, name := range args {
			if !validName(name) || seen[name] {
				return errors.New("inspectdb: invalid or duplicate selected relation")
			}
			seen[name] = true
		}
		return nil
	}
	return Command{Name: "inspectdb", Help: "Print reviewed unmanaged model source from a read-only database catalog", Resources: []string{"database"}, OpenResources: true, Validate: validateRelations, Configure: func(flags *flag.FlagSet) Runner {
		alias := "default"
		options := InspectDBOptions{AppLabel: "legacy", PrimaryKeys: map[string][]string{}}
		flags.Func("database", "Configured database alias (default: default)", func(value string) error {
			if !validName(value) || !models.ValidIdentifier(value) {
				return errors.New("valid database alias required")
			}
			alias = value
			return nil
		})
		flags.Func("schema", "Exact database schema (default: connection current schema)", func(value string) error {
			if value != "" && !validName(value) {
				return errors.New("valid schema required")
			}
			options.Catalog.Schema = value
			return nil
		})
		flags.Func("app", "Application label (default: legacy)", func(value string) error {
			candidate := options
			candidate.AppLabel = value
			if err := validateInspectionOptions(candidate); err != nil {
				return err
			}
			options.AppLabel = value
			return nil
		})
		flags.Func("package", "Go package name (default: app label)", func(value string) error {
			candidate := options
			candidate.Package = value
			if err := validateInspectionOptions(candidate); err != nil {
				return err
			}
			options.Package = value
			return nil
		})
		views := flags.Bool("include-views", false, "Include views and materialized views; keyless relations require explicit verified identity")
		flags.Func("primary-key", "Developer-verified row identity table.column; repeat for composite identities", func(value string) error {
			parts := strings.Split(value, ".")
			if len(parts) != 2 {
				return errors.New("primary-key must be table.column")
			}
			if _, exists := options.PrimaryKeys[parts[0]]; !exists && len(options.PrimaryKeys) >= 1000 {
				return errors.New("inspectdb: declare row identities for at most 1000 relations")
			}
			candidate := InspectDBOptions{AppLabel: options.AppLabel, Package: options.Package, PrimaryKeys: map[string][]string{parts[0]: append(append([]string(nil), options.PrimaryKeys[parts[0]]...), parts[1])}}
			if err := validateInspectionOptions(candidate); err != nil {
				return err
			}
			options.PrimaryKeys[parts[0]] = candidate.PrimaryKeys[parts[0]]
			return nil
		})
		return func(ctx context.Context, i *Invocation, args []string) error {
			if resolve == nil {
				return errors.New("inspectdb: configured catalog resolver required")
			}
			options.Catalog.Relations = append([]string(nil), args...)
			options.Catalog.IncludeViews = *views
			backend, introspector, err := resolve(alias)
			if err != nil {
				return err
			}
			source, err := InspectDB(ctx, backend, introspector, options)
			if err != nil {
				var mapping *inspectionMappingError
				if errors.As(err, &mapping) {
					return &CommandError{Code: 1, Message: mapping.Error(), Cause: err}
				}
				return err
			}
			n, err := i.Stdout.Write(source)
			if err == nil && n != len(source) {
				err = io.ErrShortWrite
			}
			return err
		}
	}}
}
