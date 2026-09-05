package admin

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
)

func (s *Site) actorLabel(ctx context.Context, principal auth.Principal) (label string, err error) {
	defer func() {
		if recover() != nil {
			label, err = "", errors.New("admin: actor label unavailable")
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.config.ActorLabel == nil {
		return principal.ID, nil
	}
	bound := auth.WithPrincipal(ctx, principal)
	label, err = s.config.ActorLabel(bound, auth.FromContext(bound))
	if canceled := ctx.Err(); canceled != nil {
		return "", canceled
	}
	if err != nil || label == "" || len(label) > 1024 || !utf8.ValidString(label) {
		return "", errors.New("admin: actor label unavailable")
	}
	return label, nil
}
