package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloseCancelsAndWaitsForRunningExecution(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	var running atomic.Bool
	task := &Task{
		Name:     "close-wait-test",
		Interval: time.Hour,
		Execute: func(ctx context.Context) error {
			running.Store(true)
			close(started)
			<-ctx.Done()
			running.Store(false)
			close(finished)
			return ctx.Err()
		},
	}
	if err := task.Start(true); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	<-started

	task.Close()

	select {
	case <-finished:
	default:
		t.Fatal("Close() returned before the execution finished")
	}
	if running.Load() {
		t.Fatal("execution is still running after Close()")
	}
}
