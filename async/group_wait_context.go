package async

import (
	"context"
	"time"
)

// Call only after freezing the operation. Context methods are caller code;
// guard them locally without catching an iterator consumer's panic or break.
func groupWaitAllowed(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if contextErr := groupContextError(ctx); contextErr != nil {
			err = contextErr
		}
	}()
	if err := groupContextError(ctx); err != nil {
		return err
	}
	if ctx.Value(workerContextKey{}) != nil {
		return ErrWorkerJoin
	}
	return nil
}

func groupWaitPoll(ctx context.Context, ticks <-chan time.Time) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if contextErr := groupContextError(ctx); contextErr != nil {
			err = contextErr
		}
	}()
	if err := groupContextError(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		if err := groupContextError(ctx); err != nil {
			return err
		}
		// Closed Done with nil Err is not successful workflow completion.
		return ErrUnavailable
	case <-ticks:
		return nil
	}
}
