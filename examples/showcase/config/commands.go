package config

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func noArgs(args []string) error {
	if len(args) != 0 {
		return errors.New("this command takes no positional arguments")
	}
	return nil
}

func (c *Connections) Commands() []management.Command {
	return []management.Command{
		{Name: "seed", Help: "Insert missing demonstration records and synchronize model permissions", Resources: []string{"database"}, OpenResources: true, Validate: noArgs, Configure: func(*flag.FlagSet) management.Runner {
			return func(ctx context.Context, i *management.Invocation, _ []string) error {
				if _, err := contenttypes.Sync(ctx, c.Store, c.Store.Registry, nil); err != nil {
					return err
				}
				if err := auth.SyncPermissions(ctx, c.Store, c.Store.Registry, nil); err != nil {
					return err
				}
				if err := catalog.Seed(ctx, c.Store); err != nil {
					return err
				}
				for _, schema := range fieldlab.Schemas() {
					if schema.Name != "Specimen" {
						continue
					}
					factory := fieldlab.Factories()[schema.Key()]
					exists, err := orm.For(c.Store, factory).Exists(ctx)
					if err != nil {
						return err
					}
					if !exists {
						record, err := fieldlab.NewSpecimen()
						if err != nil {
							return err
						}
						if err := c.Store.Save(ctx, record, orm.SaveOptions{ForceInsert: true}); err != nil {
							return err
						}
					}
				}
				_, err := fmt.Fprintln(i.Stdout, "Missing sample records inserted; existing records and credentials preserved.")
				return err
			}
		}},
		{Name: "createadmin", Help: "Create an administrator from required private environment values; never reset an existing user", Resources: []string{"database", "bootstrap"}, OpenResources: true, Validate: noArgs, Configure: func(*flag.FlagSet) management.Runner {
			return func(ctx context.Context, i *management.Invocation, _ []string) error {
				identifier, err := auth.NormalizeIdentifier(i.Settings.String("GOGO_SHOWCASE_ADMIN_IDENTIFIER"))
				if err != nil {
					return err
				}
				// This capability exists only in this explicit local management
				// invocation. It is never installed in the HTTP account service.
				accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: c.Store, Models: AccountModels(), Authorize: func(ctx context.Context, change auth.AccountChange) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if change.Action != "create_user" || change.Name != identifier || !change.Staff || !change.Superuser {
						return auth.ErrPermissionDenied
					}
					return nil
				}})
				if err != nil {
					return err
				}
				if _, err := accounts.CreateUser(ctx, identifier, i.Settings.Secret("GOGO_SHOWCASE_ADMIN_PASSWORD").Reveal(), auth.CreateUserOptions{Staff: true, Superuser: true}); err != nil {
					return errors.New("administrator creation failed; verify password rules and that the identifier is new")
				}
				_, err = fmt.Fprintln(i.Stdout, "Administrator created. Remove bootstrap password from the environment before running the web process.")
				return err
			}
		}},
		{Name: "fields", Help: "Print the field/form/widget coverage inventory", Validate: noArgs, Configure: func(*flag.FlagSet) management.Runner {
			return func(_ context.Context, i *management.Invocation, _ []string) error {
				return json.NewEncoder(i.Stdout).Encode(fieldlab.Catalog())
			}
		}},
		{Name: "envtemplate", Help: "Print grouped safe environment declarations without credentials", Validate: noArgs, Configure: func(*flag.FlagSet) management.Runner {
			return func(_ context.Context, i *management.Invocation, _ []string) error {
				_, err := fmt.Fprint(i.Stdout, Settings().EnvTemplate())
				return err
			}
		}},
	}
}

// Keep model registration a visible public contract, not a database discovery.
var _ models.Model = (*catalog.Product)(nil)
