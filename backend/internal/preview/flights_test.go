package preview

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitFlightRefs(t *testing.T, g *previewFlights, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.Lock()
		job := g.jobs[key]
		refs := 0
		if job != nil {
			refs = job.refs
		}
		g.Unlock()
		if refs == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("viewers did not join shared preview")
}

func TestPreviewFlightGeneratesOnceForConcurrentViewers(t *testing.T) {
	var group previewFlights
	var calls atomic.Int32
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := group.do(context.Background(), "frame", func(context.Context) ([]byte, error) { calls.Add(1); <-ready; return []byte("frame"), nil })
			if err != nil || string(data) != "frame" {
				t.Error(data, err)
			}
		}()
	}
	waitFlightRefs(t, &group, "frame", 16)
	close(ready)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate preview generation", calls.Load())
	}
}

func TestLeavingViewerDoesNotCancelOtherViewers(t *testing.T) {
	var group previewFlights
	ctx, cancel := context.WithCancel(context.Background())
	finish := make(chan struct{})
	first := make(chan error, 1)
	second := make(chan error, 1)
	work := func(ctx context.Context) ([]byte, error) {
		select {
		case <-finish:
			return []byte("ok"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	go func() { _, err := group.do(ctx, "frame", work); first <- err }()
	waitFlightRefs(t, &group, "frame", 1)
	go func() { _, err := group.do(context.Background(), "frame", work); second <- err }()
	waitFlightRefs(t, &group, "frame", 2)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(finish)
	if err := <-second; err != nil {
		t.Fatal("another viewer was cancelled", err)
	}
}

func TestLastViewerCancelsWorkAndNewRequestCanRestart(t *testing.T) {
	var group previewFlights
	ctx, cancel := context.WithCancel(context.Background())
	exit := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := group.do(ctx, "frame", func(work context.Context) ([]byte, error) { <-work.Done(); close(exit); return nil, work.Err() })
		done <- err
	}()
	waitFlightRefs(t, &group, "frame", 1)
	cancel()
	<-done
	select {
	case <-exit:
	case <-time.After(time.Second):
		t.Fatal("abandoned preview kept running")
	}
	data, err := group.do(context.Background(), "frame", func(context.Context) ([]byte, error) { return []byte("new"), nil })
	if err != nil || string(data) != "new" {
		t.Fatal(data, err)
	}
}

func TestPreviewFlightRecoversGeneratorPanic(t *testing.T) {
	var group previewFlights
	_, err := group.do(context.Background(), "frame", func(context.Context) ([]byte, error) { panic("fixture") })
	if err == nil {
		t.Fatal("panic was not reported")
	}
}

func TestVideoJobsShareConfiguredProcessorBudget(t *testing.T) {
	for _, limit := range []int{1, 2, 4, 7, 32} {
		s := NewPreviewGenerator(limit, t.TempDir())
		budget := cap(s.imageSem)
		if s.imageLargeSem != nil {
			budget += cap(s.imageLargeSem)
		}
		if budget != limit {
			t.Fatal("processor budget changed", limit, budget)
		}
		if s.videoSem != s.imageSem && s.videoSem != s.imageLargeSem {
			t.Fatal("video jobs bypass processor budget")
		}
	}
}
