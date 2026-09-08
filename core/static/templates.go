package static

import (
	"context"

	"github.com/Newton-School/gogo/core/templates"
)

// Tags returns fresh explicit registrations for a frozen manifest. Include
// "static" in templates.Config.Libraries to permit {% load static %}. Both
// direct output and the engine's existing `as name` syntax are supported.
// Results are ordinary strings, never SafeHTML, and retain contextual escaping.
func (m *Manifest) Tags() map[string]templates.Tag {
	var captured Manifest
	if m != nil {
		captured = *m
	}
	return map[string]templates.Tag{
		"static": func(ctx context.Context, _ templates.Context, args []any) (any, error) {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
			if len(args) != 1 {
				return nil, ErrInvalid
			}
			name, ok := args[0].(string)
			if !ok {
				return nil, ErrInvalid
			}
			value, err := captured.URL(name)
			if err != nil {
				return nil, err
			}
			if err = contextError(ctx); err != nil {
				return nil, err
			}
			return value, nil
		},
		"get_static_prefix": func(ctx context.Context, _ templates.Context, args []any) (any, error) {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
			if len(args) != 0 || captured.assets == nil {
				return nil, ErrInvalid
			}
			return captured.prefix.base, nil
		},
	}
}
