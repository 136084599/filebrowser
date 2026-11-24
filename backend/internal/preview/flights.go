package preview

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type previewFlight struct {
	done   chan struct{}
	cancel context.CancelFunc
	refs   int
	data   []byte
	err    error
}

type previewFlights struct {
	sync.Mutex
	jobs map[string]*previewFlight
}

// Share a cache miss while keeping each caller's cancellation independent.
// Work stops when the last viewer leaves, or after the normal preview deadline.
func (g *previewFlights) do(ctx context.Context, key string, generate func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.Lock()
	if g.jobs == nil {
		g.jobs = make(map[string]*previewFlight)
	}
	job := g.jobs[key]
	if job == nil {
		work, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		job = &previewFlight{done: make(chan struct{}), cancel: cancel}
		g.jobs[key] = job
		go func(j *previewFlight) {
			defer cancel()
			func() {
				defer func() {
					if value := recover(); value != nil {
						j.err = fmt.Errorf("preview generation failed: %v", value)
					}
				}()
				j.data, j.err = generate(work)
			}()
			g.Lock()
			if g.jobs[key] == j {
				delete(g.jobs, key)
			}
			close(j.done)
			g.Unlock()
		}(job)
	}
	job.refs++
	g.Unlock()
	defer func() {
		g.Lock()
		job.refs--
		if job.refs == 0 {
			if g.jobs[key] == job {
				delete(g.jobs, key)
			}
			job.cancel()
		}
		g.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-job.done:
		return job.data, job.err
	}
}
