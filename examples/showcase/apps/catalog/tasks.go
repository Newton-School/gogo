package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/Newton-School/gogo/async"
)

// These pure tasks are deliberately safe to deliver again. Real external effects
// need an application idempotency key; queue acknowledgement is not exactly-once.
type Tasks struct {
	Registry *async.Registry
	Double   *async.Task[int, int]
	Total    *async.Task[[]int, int]
}

func NewTasks() (*Tasks, error) {
	r := async.NewRegistry()
	options := async.TaskOptions{Queue: "showcase", PerWorkerConcurrency: 2,
		SoftLimit: 5 * time.Second,
		Authorize: func(ctx context.Context, tc async.TaskContext) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if tc.Scope != "" {
				return async.ErrDenied
			}
			return nil
		},
	}
	double, err := async.Register(r, "catalog.double", 1, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if n < -1000000 || n > 1000000 {
			return 0, errors.New("number outside demonstration range")
		}
		return n * 2, nil
	}, options)
	if err != nil {
		return nil, err
	}
	total, err := async.Register(r, "catalog.total", 1, func(ctx context.Context, _ async.TaskContext, values []int) (int, error) {
		if len(values) > 100 {
			return 0, async.ErrInvalid
		}
		sum := 0
		for _, n := range values {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if n < -2000000 || n > 2000000 {
				return 0, async.ErrInvalid
			}
			sum += n
		}
		return sum, nil
	}, options)
	if err != nil {
		return nil, err
	}
	return &Tasks{Registry: r, Double: double, Total: total}, nil
}

func (t *Tasks) Canvas(kind string) (async.Canvas, error) {
	one, err := t.Double.Signature(3)
	if err != nil {
		return async.Canvas{}, err
	}
	two, err := t.Double.Signature(5)
	if err != nil {
		return async.Canvas{}, err
	}
	switch kind {
	case "group":
		return async.Group(one, two), nil
	case "chain":
		return async.Chain(one, two.FromParent("")), nil
	case "chord":
		body, err := t.Total.Signature([]int{})
		if err != nil {
			return async.Canvas{}, err
		}
		return async.Chord(async.Group(one, two), body.FromParent(""))
	default:
		return async.Canvas{}, async.ErrInvalid
	}
}
