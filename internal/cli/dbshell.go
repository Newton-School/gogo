package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/Newton-School/gogo/conf"
)

type DBShellConfig struct {
	Settings      conf.Settings
	DatabaseAlias string
	Command       string
	DryRun        bool
	Executable    string
	Args          []string
	Env           []string
	Stdout        io.Writer
	Stderr        io.Writer
}

type DBShellExecutor func(context.Context, DBShellConfig) error

func NewDBShellCommand(executor DBShellExecutor) Command {
	if executor == nil {
		executor = defaultDBShellExecutor
	}
	return dbShellCommand{executor: executor}
}

type dbShellCommand struct {
	executor DBShellExecutor
}

func (c dbShellCommand) Name() string {
	return "dbshell"
}

func (c dbShellCommand) Summary() string {
	return "Open a database shell"
}

func (c dbShellCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c dbShellCommand) runWithIO(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dbshell", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "default", "database alias")
	sqlCommand := flags.String("command", "", "non-interactive SQL command")
	dryRun := flags.Bool("dry-run", false, "print resolved shell command without executing it")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}

	settings, err := conf.LoadFromEnv()
	if err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}

	executable, execArgs, execEnv, err := resolveDBShellCommand(settings.DatabaseURL, *sqlCommand, flags.Args())
	if err != nil {
		return err
	}
	config := DBShellConfig{
		Settings:      settings,
		DatabaseAlias: *database,
		Command:       *sqlCommand,
		DryRun:        *dryRun,
		Executable:    executable,
		Args:          execArgs,
		Env:           execEnv,
		Stdout:        stdout,
		Stderr:        stderr,
	}
	if config.DryRun {
		_, err := fmt.Fprintf(stdout, "%s %s\n", executable, strings.Join(redactDBShellArgs(execArgs), " "))
		if err != nil {
			return fmt.Errorf("%w: write dbshell dry run: %v", ErrCommandFailed, err)
		}
		return nil
	}
	return c.executor(ctx, config)
}

func resolveDBShellCommand(databaseURL, command string, extraArgs []string) (string, []string, []string, error) {
	switch {
	case strings.HasPrefix(databaseURL, "sqlite://"):
		dsn := strings.TrimPrefix(databaseURL, "sqlite://")
		if dsn == "" {
			dsn = ":memory:"
		}
		args := []string{dsn}
		if command != "" {
			args = append(args, command)
		}
		args = append(args, extraArgs...)
		return "sqlite3", args, nil, nil
	case strings.HasPrefix(databaseURL, "sqlite3://"):
		dsn := strings.TrimPrefix(databaseURL, "sqlite3://")
		if dsn == "" {
			dsn = ":memory:"
		}
		args := []string{dsn}
		if command != "" {
			args = append(args, command)
		}
		args = append(args, extraArgs...)
		return "sqlite3", args, nil, nil
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: parse DATABASE_URL: %v", ErrInvalidArguments, err)
	}
	switch parsed.Scheme {
	case "postgres", "postgresql":
		connectionURL := databaseURL
		env := []string(nil)
		if parsed.User != nil {
			username := parsed.User.Username()
			if password, ok := parsed.User.Password(); ok {
				env = append(env, "PGPASSWORD="+password)
				parsed.User = url.User(username)
				connectionURL = parsed.String()
			}
		}
		args := []string{connectionURL}
		if command != "" {
			args = append(args, "-c", command)
		}
		args = append(args, extraArgs...)
		return "psql", args, env, nil
	case "mysql", "mariadb":
		args := []string(nil)
		env := []string(nil)
		if host := parsed.Hostname(); host != "" {
			args = append(args, "--host", host)
		}
		if port := parsed.Port(); port != "" {
			args = append(args, "--port", port)
		}
		if parsed.User != nil {
			if username := parsed.User.Username(); username != "" {
				args = append(args, "--user", username)
			}
			if password, ok := parsed.User.Password(); ok {
				env = append(env, "MYSQL_PWD="+password)
			}
		}
		database := strings.TrimPrefix(parsed.Path, "/")
		if database != "" {
			args = append(args, database)
		}
		if command != "" {
			args = append(args, "--execute", command)
		}
		args = append(args, extraArgs...)
		return "mysql", args, env, nil
	default:
		return "", nil, nil, fmt.Errorf("%w: unsupported database shell scheme %q", ErrInvalidArguments, parsed.Scheme)
	}
}

func defaultDBShellExecutor(ctx context.Context, config DBShellConfig) error {
	if config.Command == "" && !stdinIsTerminal(os.Stdin) {
		if config.Stdout != nil {
			if _, err := fmt.Fprintln(config.Stdout, "dbshell requires an interactive terminal; use --command for non-interactive SQL or --dry-run to inspect the resolved command"); err != nil {
				return fmt.Errorf("%w: write dbshell output: %v", ErrCommandFailed, err)
			}
		}
		return nil
	}

	if _, err := exec.LookPath(config.Executable); err != nil {
		return fmt.Errorf("%w: %s not found in PATH", ErrCommandFailed, config.Executable)
	}
	command := exec.CommandContext(ctx, config.Executable, config.Args...)
	command.Stdin = os.Stdin
	if config.Stdout != nil {
		command.Stdout = config.Stdout
	}
	if config.Stderr != nil {
		command.Stderr = config.Stderr
	}
	if len(config.Env) > 0 {
		command.Env = append(os.Environ(), config.Env...)
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: dbshell failed: %v", ErrCommandFailed, err)
	}
	return nil
}

func redactDBShellArgs(args []string) []string {
	redacted := make([]string, len(args))
	for index, arg := range args {
		redacted[index] = redactDatabaseURL(arg)
	}
	return redacted
}

func redactDatabaseURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return value
	}
	username := parsed.User.Username()
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.UserPassword(username, "xxxxx")
	}
	return parsed.String()
}
