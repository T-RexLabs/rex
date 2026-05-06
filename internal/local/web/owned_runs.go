package web

import (
	"context"
	"sync"
)

// ownedRuns tracks background runs launched by the current rex serve
// process. In v1 there is no daemon, so graceful shutdown must wait
// for these goroutines to observe context cancellation and append
// their terminal run.cancelled event before the process exits.
type ownedRuns struct {
	wg sync.WaitGroup
}

func (o *ownedRuns) start() func() {
	o.wg.Add(1)
	return o.wg.Done
}

func (o *ownedRuns) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
