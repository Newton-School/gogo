package management

import (
	"context"
	"errors"
	"io"
	"reflect"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/urls"
)

// OpenAPICommand registers an opt-in "openapi" command that writes the complete
// bound read-router document to Invocation.Stdout. Resolve should reuse the
// application's selected API-router factory, not its middleware-wrapped whole
// site handler. It is a trusted, cooperative read-only callback; generation
// itself never queries rows or samples policies or representations.
//
// No resources are selected or opened by default. Applications that need them
// to build their router must explicitly set the returned Command.Resources and
// Command.OpenResources. Ordinary management.Call parsing rejects arguments and
// handles help before resource startup. No file or HTTP endpoint is created.
//
// Output is attempted once, after complete generation. Writer failure, invalid
// counts, panic or cancellation return a safe CommandError; a failed write may
// already have emitted bytes and is never retried or followed by an error body.
func OpenAPICommand(resolve func(context.Context, *app.Registry, conf.Values) (*urls.Router, error), options api.OpenAPIOptions) Command {
	return Command{
		Name: "openapi", Help: "Print the complete OpenAPI document for an explicitly configured read API router",
		Validate: noArgs,
		Configure: fixed(func(ctx context.Context, invocation *Invocation, args []string) (err error) {
			defer func() {
				if recover() != nil {
					err = openAPICommandFailure(errors.Join(errors.New("OpenAPI command callback panicked"), openAPICommandContextError(ctx)))
				}
			}()
			if err := noArgs(args); err != nil {
				return &CommandError{Code: 2, Message: "Invalid command arguments", Cause: err}
			}
			if invocation == nil || invocation.Application == nil || invocation.Application.Registry == nil || resolve == nil || openAPICommandNil(invocation.Stdout) {
				return openAPICommandFailure(api.ErrOpenAPI)
			}
			// Context methods and resolve are callbacks. Freeze these handles
			// first; never copy app.Registry, which contains a live mutex.
			stdout := invocation.Stdout
			settings := invocation.Settings
			registry := invocation.Application.Registry
			if err := openAPICommandContextError(ctx); err != nil {
				return openAPICommandFailure(err)
			}
			router, resolveErr := resolve(ctx, registry, settings)
			if router != nil {
				// Router is an immutable compiled declaration handle with no
				// mutex. Freeze it before another context callback can replace
				// the resolver's public handle.
				captured := *router
				router = &captured
			}
			if err := errors.Join(resolveErr, openAPICommandContextError(ctx)); err != nil {
				return openAPICommandFailure(err)
			}
			document, generationErr := api.OpenAPI(ctx, router, options)
			if err := errors.Join(generationErr, openAPICommandContextError(ctx)); err != nil {
				return openAPICommandFailure(err)
			}
			n, writeErr := stdout.Write(document)
			if n != len(document) {
				writeErr = errors.Join(writeErr, io.ErrShortWrite)
			}
			if err := errors.Join(writeErr, openAPICommandContextError(ctx)); err != nil {
				return openAPICommandFailure(err)
			}
			return nil
		}),
	}
}

func openAPICommandFailure(cause error) error {
	return &CommandError{Code: 1, Message: "OpenAPI command failed", Cause: cause}
}

func openAPICommandNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func openAPICommandContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = api.ErrOpenAPI
		}
	}()
	if openAPICommandNil(ctx) {
		return api.ErrOpenAPI
	}
	return ctx.Err()
}
