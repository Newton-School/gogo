package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Newton-School/gogo/auth"
)

var defaultAuthStore = newDefaultAuthStore()

type authUserStore interface {
	Add(auth.User) error
	FindByUsername(context.Context, string) (auth.User, bool, error)
	UpdateUser(context.Context, auth.User) error
}

func newDefaultAuthStore() authUserStore {
	return newFileAuthUserStore(defaultCLIAuthStorePath)
}

// NewCreateSuperuserCommand creates the built-in createsuperuser command.
func NewCreateSuperuserCommand(store authUserStore) Command {
	return authCommand{name: "createsuperuser", summary: "Create an admin user", store: store}
}

// NewChangePasswordCommand creates the built-in changepassword command.
func NewChangePasswordCommand(store authUserStore) Command {
	return authCommand{name: "changepassword", summary: "Change a user password", store: store}
}

type authCommand struct {
	name    string
	summary string
	store   authUserStore
}

func (c authCommand) Name() string    { return c.name }
func (c authCommand) Summary() string { return c.summary }
func (c authCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c authCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	options, err := parseAuthFlags(c.name, args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	if err := validatePasswordFlagSafety(options); err != nil {
		return err
	}
	switch c.name {
	case "createsuperuser":
		return c.runCreateSuperuser(ctx, options, stdout)
	case "changepassword":
		return c.runChangePassword(ctx, options, stdout)
	default:
		return fmt.Errorf("%w: unknown auth command %s", ErrCommandFailed, c.name)
	}
}

func (c authCommand) runCreateSuperuser(ctx context.Context, options authCommandOptions, stdout io.Writer) error {
	if c.store == nil {
		return fmt.Errorf("%w: auth user store is required", ErrCommandFailed)
	}
	username := auth.NormalizeUsername(options.username)
	email := auth.NormalizeEmail(options.email)
	if username == "" {
		return fmt.Errorf("%w: username is required", ErrCommandFailed)
	}
	user := auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{
			IsSuperuser: true,
			IsActive:    true,
			DateJoined:  time.Now().UTC(),
		},
		Username: username,
		Email:    email,
		IsStaff:  true,
	}}
	if err := auth.ValidatePassword(options.password, user); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	hash, err := auth.MakePassword(options.password)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	user.Password = hash
	if err := c.store.Add(user); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	_, err = fmt.Fprintf(stdout, "created superuser %s on database %s\n", username, options.database)
	if err != nil {
		return fmt.Errorf("%w: write auth command output: %v", ErrCommandFailed, err)
	}
	return nil
}

func (c authCommand) runChangePassword(ctx context.Context, options authCommandOptions, stdout io.Writer) error {
	if c.store == nil {
		return fmt.Errorf("%w: auth user store is required", ErrCommandFailed)
	}
	username := auth.NormalizeUsername(options.username)
	if username == "" {
		return fmt.Errorf("%w: username is required", ErrCommandFailed)
	}
	user, ok, err := c.store.FindByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	if !ok {
		return fmt.Errorf("%w: %w", ErrCommandFailed, auth.ErrUserNotFound)
	}
	if err := auth.ValidatePassword(options.password, user); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	hash, err := auth.MakePassword(options.password)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	user.Password = hash
	if err := c.store.UpdateUser(ctx, user); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	_, err = fmt.Fprintf(stdout, "changed password for %s on database %s\n", username, options.database)
	if err != nil {
		return fmt.Errorf("%w: write auth command output: %v", ErrCommandFailed, err)
	}
	return nil
}

type authCommandOptions struct {
	username string
	email    string
	password string
	database string
	noinput  bool
}

func parseAuthFlags(command string, args []string) (authCommandOptions, error) {
	options := authCommandOptions{database: "default"}
	positionalUsername := ""
	if command == "changepassword" && len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		positionalUsername = args[0]
		args = append([]string{}, args[1:]...)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.StringVar(&options.username, "username", "", "username")
	flags.StringVar(&options.email, "email", "", "email")
	flags.StringVar(&options.password, "password", "", "password for non-interactive automation")
	flags.StringVar(&options.database, "database", "default", "database alias")
	flags.BoolVar(&options.noinput, "noinput", false, "disable input")
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if options.username == "" && positionalUsername != "" {
		options.username = positionalUsername
	}
	if command == "changepassword" && options.username == "" && flags.NArg() > 0 {
		options.username = flags.Arg(0)
	}
	return options, nil
}

func validatePasswordFlagSafety(options authCommandOptions) error {
	if options.password != "" && !options.noinput {
		return fmt.Errorf("%w: --password requires --noinput for explicit automation", ErrCommandFailed)
	}
	if !options.noinput {
		return fmt.Errorf("%w: interactive password input is not available; use --noinput with --password for automation", ErrCommandFailed)
	}
	if options.password == "" {
		return fmt.Errorf("%w: password is required in non-interactive mode", ErrCommandFailed)
	}
	return nil
}
