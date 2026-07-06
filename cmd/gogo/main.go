package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Newton-School/gogo/internal/cli"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, runOptions{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, cli.ErrInvalidArguments) || errors.Is(err, cli.ErrUnknownCommand) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type commandRunner func(context.Context, string, string, []string, io.Writer, io.Writer) error

type runOptions struct {
	commandRunner commandRunner
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, options runOptions) error {
	if root, ok := projectAwareRoot(args); ok {
		runner := options.commandRunner
		if runner == nil {
			runner = runExternalCommand
		}
		delegateArgs := append([]string{"run", "manage.go"}, args...)
		return runner(ctx, root, "go", delegateArgs, stdout, stderr)
	}

	return cli.NewRoot().Execute(ctx, args, stdout, stderr)
}

func projectAwareRoot(args []string) (string, bool) {
	command := commandName(args)
	if !projectAwareCommand(command) {
		return "", false
	}
	root, err := findProjectRoot()
	if err != nil {
		return "", false
	}
	return root, true
}

func commandName(args []string) string {
	if len(args) == 0 {
		return "help"
	}
	switch args[0] {
	case "--help", "-h":
		return "help"
	case "--version", "-version":
		return "version"
	default:
		return args[0]
	}
}

func projectAwareCommand(command string) bool {
	switch command {
	case "check",
		"runserver",
		"startapp",
		"makemigrations",
		"migrate",
		"showmigrations",
		"sqlmigrate",
		"squashmigrations",
		"optimizemigration",
		"createsuperuser",
		"changepassword",
		"collectstatic",
		"shell",
		"dbshell",
		"test",
		"worker",
		"beat",
		"inspect",
		"queues",
		"dumpdata",
		"loaddata":
		return true
	default:
		return false
	}
}

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "manage.go")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runExternalCommand(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}
