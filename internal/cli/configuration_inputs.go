package cli

import (
	"context"
	"errors"
	"sync"
)

// Configuration reads are independent, but no remote mutation may begin until
// all have completed successfully. Drain siblings on error/cancellation.
func gatherConfigurationInputs(ctx context.Context, jobs ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make([]error, len(jobs))
	var workers sync.WaitGroup
	for i, job := range jobs {
		workers.Add(1)
		go func(i int, job func(context.Context) error) {
			defer workers.Done()
			failures[i] = job(ctx)
			if failures[i] != nil {
				cancel()
			}
		}(i, job)
	}
	workers.Wait()
	return errors.Join(append(failures, ctx.Err())...)
}
