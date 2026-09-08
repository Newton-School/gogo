package management

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/static"
)

// StaticCommands explicitly registers collectstatic and findstatic. Resolve
// reuses the project's static configuration and app source declarations; it is
// cooperative and must not open unrelated services. The commands never run
// Ready hooks or open database/Redis resources. collectstatic validates the
// existing GOGO_STATIC_ROOT setting through its "static" configuration role.
// Use settings.String("GOGO_STATIC_ROOT") as Config.Destination in Resolve.
// findstatic requires no collection destination. Output is bounded JSON to the
// invocation writer, never process-global stdout; --clear is not supported.
func StaticCommands(resolve func(context.Context, *app.Registry, conf.Values) (*static.Collector, error)) []Command {
	return []Command{
		{Name: "collectstatic", Help: "Collect explicitly public assets and atomically publish their hashed manifest", Resources: []string{"static"}, Validate: noArgs, Configure: func(flags *flag.FlagSet) Runner {
			dry := flags.Bool("dry-run", false, "validate and fingerprint without destination writes")
			return func(ctx context.Context, invocation *Invocation, args []string) error {
				chosenDry := *dry
				return staticRunner(resolve, func(ctx context.Context, collector *static.Collector, args []string) (staticCommandResult, error) {
					if err := noArgs(args); err != nil {
						return staticCommandResult{}, err
					}
					report, err := collector.Collect(ctx, static.CollectOptions{DryRun: chosenDry})
					if err != nil {
						message := "Static collection failed before publication"
						if report.Published {
							message = "Static manifest published; completion requires reconciliation"
						}
						return staticCommandResult{}, &CommandError{Code: 1, Message: message, Cause: err}
					}
					return staticCommandResult{DryRun: report.DryRun, Published: report.Published, Assets: report.Manifest.Assets(), Matches: report.Matches}, nil
				})(ctx, invocation, args)
			}
		}},
		{Name: "findstatic", Help: "Explain all explicit source matches in static precedence order", Validate: oneStaticName, Configure: fixedStatic(func() Runner {
			return staticRunner(resolve, func(ctx context.Context, collector *static.Collector, args []string) (staticCommandResult, error) {
				if err := oneStaticName(args); err != nil {
					return staticCommandResult{}, err
				}
				matches, err := collector.Find(ctx, args[0])
				if err != nil {
					return staticCommandResult{}, &CommandError{Code: 1, Message: "Static lookup failed", Cause: err}
				}
				return staticCommandResult{Matches: matches}, err
			})
		})},
	}
}
func fixedStatic(makeRunner func() Runner) func(*flag.FlagSet) Runner {
	return func(*flag.FlagSet) Runner { return makeRunner() }
}
func oneStaticName(args []string) error {
	if len(args) != 1 || len(args[0]) == 0 || len(args[0]) > 2048 {
		return errors.New("one static path required")
	}
	return nil
}

type staticCommandResult struct {
	DryRun    bool           `json:"dry_run"`
	Published bool           `json:"published"`
	Assets    []static.Asset `json:"assets,omitempty"`
	Matches   []static.Match `json:"matches"`
}

func staticCommandBound(result staticCommandResult) bool {
	remaining := 16 << 20
	charge := func(size int) bool {
		if size < 0 || size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	for _, asset := range result.Assets {
		if !charge(256 + 6*(len(asset.Path)+len(asset.Versioned)+len(asset.SHA256))) {
			return false
		}
	}
	for _, match := range result.Matches {
		if !charge(128 + 6*(len(match.Path)+len(match.Owner))) {
			return false
		}
	}
	return true
}
func staticRunner(resolve func(context.Context, *app.Registry, conf.Values) (*static.Collector, error), run func(context.Context, *static.Collector, []string) (staticCommandResult, error)) Runner {
	return func(ctx context.Context, invocation *Invocation, args []string) (err error) {
		published := false
		defer func() {
			if recover() != nil {
				message := "Static command failed"
				if published {
					message = "Static manifest published; command output failed"
				}
				err = &CommandError{Code: 1, Message: message, Cause: errors.New("static command callback panicked")}
			}
		}()
		if invocation == nil || invocation.Application == nil || invocation.Application.Registry == nil || resolve == nil || staticCommandNil(invocation.Stdout) {
			return &CommandError{Code: 1, Message: "Static command is not configured"}
		}
		writer, registry, settings := invocation.Stdout, invocation.Application.Registry, invocation.Settings
		arguments := append([]string(nil), args...)
		if e := staticCommandContextError(ctx); e != nil {
			return &CommandError{Code: 1, Message: "Static command canceled", Cause: e}
		}
		collector, e := resolve(ctx, registry, settings)
		if collector != nil {
			captured := *collector
			collector = &captured
		}
		if e = errors.Join(e, staticCommandContextError(ctx)); e != nil || collector == nil {
			return &CommandError{Code: 1, Message: "Static configuration failed", Cause: e}
		}
		value, e := run(ctx, collector, arguments)
		if e != nil {
			return e
		}
		// Collection may already be published. Output failure is a separate
		// completion failure, not evidence that filesystem changes rolled back.
		published = value.Published
		if !staticCommandBound(value) {
			message := "Static command result exceeds output limit"
			if published {
				message = "Static manifest published; command result exceeds output limit"
			}
			return &CommandError{Code: 1, Message: message, Cause: static.ErrLimit}
		}
		data, e := json.Marshal(value)
		if e != nil {
			return &CommandError{Code: 1, Message: "Static result encoding failed", Cause: e}
		}
		data = append(data, '\n')
		n, e := writer.Write(data)
		if n != len(data) {
			e = errors.Join(e, io.ErrShortWrite)
		}
		if e != nil {
			message := "Static command completed; output failed"
			if published {
				message = "Static manifest published; command output failed"
			}
			return &CommandError{Code: 1, Message: message, Cause: e}
		}
		return nil
	}
}
func staticCommandNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
func staticCommandContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = static.ErrInvalid
		}
	}()
	if staticCommandNil(ctx) {
		return static.ErrInvalid
	}
	return ctx.Err()
}
