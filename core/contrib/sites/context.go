package sites

import (
	"context"
	"net/http"
)

type siteKey struct{}

// FromContext returns a value snapshot selected by WithSite or WithCurrent.
// It is not an authorization check and does not revalidate subsequent DB edits.
func FromContext(ctx context.Context) (Info, bool) {
	if nilValue(ctx) {
		return Info{}, false
	}
	value, ok := ctx.Value(siteKey{}).(Info)
	return value, ok
}

func (r *Resolver) WithSite(ctx context.Context, id string) (context.Context, error) {
	info, err := r.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, siteKey{}, info), nil
}

// WithCurrent returns a new context; it never mutates the request or its parent
// context. Views, email and contrib queries opt in to this explicit selection.
func (r *Resolver) WithCurrent(request *http.Request) (context.Context, error) {
	if request == nil {
		return nil, ErrConfiguration
	}
	ctx := request.Context()
	info, err := r.Current(request)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, siteKey{}, info), nil
}
