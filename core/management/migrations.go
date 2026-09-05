package management

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"path/filepath"
	"strings"
)

// MigrationCommands uses project-owned lazy resources; Core never imports a
// concrete connector. The project explicitly contributes schemas and migrations.
func MigrationCommands(backend func() db.Backend, editor func() db.SchemaEditor) []Command {
	collect := func(i *Invocation) ([]migrations.Migration, []models.Schema, error) {
		var all []migrations.Migration
		var schemas []models.Schema
		for _, name := range i.Application.Registry.Names("migrations") {
			value, _ := i.Application.Registry.Get("migrations", name)
			items, ok := value.([]migrations.Migration)
			if !ok {
				return nil, nil, errors.New("invalid migration contribution")
			}
			all = append(all, items...)
		}
		for _, name := range i.Application.Registry.Names("models") {
			value, _ := i.Application.Registry.Get("models", name)
			schema, ok := value.(models.Schema)
			if !ok {
				return nil, nil, errors.New("invalid model contribution")
			}
			schemas = append(schemas, schema)
		}
		return all, schemas, nil
	}
	engine := func(i *Invocation) (*migrations.Executor, error) {
		all, _, err := collect(i)
		if err != nil {
			return nil, err
		}
		if backend == nil || editor == nil || backend() == nil || editor() == nil {
			return nil, errors.New("migration database resource is not open")
		}
		return &migrations.Executor{Backend: backend(), Editor: editor(), Migrations: all}, nil
	}
	target := func(args []string) (string, error) {
		if len(args) == 0 {
			return "", nil
		}
		if len(args) == 1 {
			if strings.Contains(args[0], ".") {
				return args[0], nil
			}
			return "", errors.New("provide app and migration, or app.migration")
		}
		if len(args) == 2 {
			return args[0] + "." + args[1], nil
		}
		return "", errors.New("too many migration arguments")
	}
	return []Command{
		{Name: "migrate", Help: "Apply or reverse checksummed migrations", Resources: []string{"database"}, OpenResources: true, Configure: func(flags *flag.FlagSet) Runner {
			planOnly := flags.Bool("plan", false, "Print planned migrations without mutation")
			reverse := flags.Bool("reverse", false, "Reverse to the selected migration or app.zero")
			return func(ctx context.Context, i *Invocation, args []string) error {
				to, err := target(args)
				if err != nil {
					return err
				}
				e, err := engine(i)
				if err != nil {
					return err
				}
				if *planOnly {
					plan, err := e.Plan(to)
					if err != nil {
						return err
					}
					for _, m := range plan {
						if _, err := fmt.Fprintln(i.Stdout, m.Key()); err != nil {
							return err
						}
					}
					return nil
				}
				if *reverse {
					if to == "" {
						return errors.New("reverse requires explicit target")
					}
					err = e.Reverse(ctx, to)
				} else {
					err = e.Apply(ctx, to)
				}
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(i.Stdout, "Migration command completed")
				return err
			}
		}},
		{Name: "showmigrations", Help: "Show source graph and applied checksums", Resources: []string{"database"}, OpenResources: true, Configure: fixed(func(ctx context.Context, i *Invocation, args []string) error {
			if len(args) > 0 {
				return errors.New("showmigrations takes no positional arguments")
			}
			e, err := engine(i)
			if err != nil {
				return err
			}
			plan, err := e.Plan("")
			if err != nil {
				return err
			}
			history, err := e.History(ctx)
			if err != nil {
				return err
			}
			applied := map[string]bool{}
			for _, h := range history {
				applied[h.Key] = true
			}
			for _, m := range plan {
				marker := " "
				if applied[m.Key()] {
					marker = "X"
				}
				if _, err := fmt.Fprintf(i.Stdout, "[%s] %s\n", marker, m.Key()); err != nil {
					return err
				}
			}
			return nil
		})},
		{Name: "sqlmigrate", Help: "Preview SQL without applying migrations", Resources: []string{"database"}, OpenResources: true, Configure: func(flags *flag.FlagSet) Runner {
			reverse := flags.Bool("backwards", false, "Preview reverse SQL")
			return func(ctx context.Context, i *Invocation, args []string) error {
				to, err := target(args)
				if err != nil || to == "" {
					return errors.New("sqlmigrate requires app and migration")
				}
				e, err := engine(i)
				if err != nil {
					return err
				}
				statements, err := e.SQL(ctx, to, *reverse)
				if err != nil {
					return err
				}
				for _, statement := range statements {
					if statement.Comment != "" {
						if _, err := fmt.Fprintln(i.Stdout, "-- "+statement.Comment); err != nil {
							return err
						}
					} else {
						if _, err := fmt.Fprintln(i.Stdout, statement.SQL+";"); err != nil {
							return err
						}
						if len(statement.Args) > 0 {
							if _, err := fmt.Fprintln(i.Stdout, "-- Bound parameters intentionally omitted from preview"); err != nil {
								return err
							}
						}
					}
				}
				return nil
			}
		}},
		{Name: "makemigrations", Help: "Generate deterministic migrations from registered model schemas", Configure: func(flags *flag.FlagSet) Runner {
			dryRun := flags.Bool("dry-run", false, "Print operations without writing source")
			check := flags.Bool("check", false, "Fail if registered schemas differ from migration state")
			name := flags.String("name", "auto", "Migration label")
			return func(_ context.Context, i *Invocation, args []string) error {
				if len(args) != 1 || !models.ValidIdentifier(args[0]) || !models.ValidIdentifier(*name) {
					return errors.New("makemigrations requires one app label and valid name")
				}
				all, schemas, err := collect(i)
				if err != nil {
					return err
				}
				e := &migrations.Executor{Migrations: all}
				before, err := e.State("")
				if err != nil {
					return err
				}
				filter := func(input []models.Schema) []models.Schema {
					output := []models.Schema{}
					for _, s := range input {
						if s.AppLabel == args[0] {
							output = append(output, s)
						}
					}
					return output
				}
				operations, err := migrations.Detect(filter(before), filter(schemas), migrations.DetectOptions{})
				if err != nil {
					return err
				}
				if len(operations) == 0 {
					_, err = fmt.Fprintln(i.Stdout, "No changes detected")
					return err
				}
				if *check {
					return errors.New("model schema drift detected")
				}
				if *dryRun {
					return json.NewEncoder(i.Stdout).Encode(operations)
				}
				number := 1
				dependencies := []string{}
				for _, migration := range all {
					if migration.App == args[0] {
						number++
						dependencies = []string{migration.Key()}
					}
				}
				migration := migrations.Migration{App: args[0], Name: fmt.Sprintf("%04d_%s", number, *name), Dependencies: dependencies, Operations: operations}
				path, err := migrations.Generate(filepath.Join(i.Project.Root, "apps", args[0], "migrations"), migration)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(i.Stdout, path)
				return err
			}
		}},
	}
}
