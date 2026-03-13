package automation

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
)

// doSync executes fn on the Fyne main thread via fyne.Do and blocks until
// it completes, returning the result. All automation handlers MUST use this
// to interact with Fyne objects.
func doSync[T any](ctx context.Context, fn func() T) (T, error) {
	type result struct {
		val T
	}
	ch := make(chan result, 1)
	fyne.Do(func() {
		ch <- result{val: fn()}
	})
	select {
	case r := <-ch:
		return r.val, nil
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("fyne dispatch timed out: %w", ctx.Err())
	}
}

// doSyncVoid executes a side-effect-only function on the Fyne main thread.
func doSyncVoid(ctx context.Context, fn func()) error {
	_, err := doSync(ctx, func() struct{} {
		fn()
		return struct{}{}
	})
	return err
}
