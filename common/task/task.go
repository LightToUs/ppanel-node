package task

import (
	"context"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type Task struct {
	Name       string
	Interval   time.Duration
	Execute    func(context.Context) error
	Access     sync.RWMutex
	Running    bool
	ReloadCh   chan struct{}
	Stop       chan struct{}
	Done       chan struct{}
	cancel     context.CancelFunc
	executions sync.WaitGroup
}

func (t *Task) Start(first bool) error {
	t.Access.Lock()
	if t.Running {
		t.Access.Unlock()
		return nil
	}
	t.Running = true
	t.Stop = make(chan struct{})
	t.Done = make(chan struct{})
	t.Access.Unlock()
	go func() {
		defer close(t.Done)
		timer := time.NewTimer(t.Interval)
		defer timer.Stop()
		if first {
			if err := t.ExecuteWithTimeout(); err != nil {
				return
			}
		}

		for {
			timer.Reset(t.Interval)
			select {
			case <-timer.C:
				// continue
			case <-t.Stop:
				return
			}

			if err := t.ExecuteWithTimeout(); err != nil {
				log.Errorf("Task %s execution error: %v", t.Name, err)
				return
			}
		}
	}()

	return nil
}

func (t *Task) ExecuteWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), min(5*t.Interval, 5*time.Minute))
	t.Access.Lock()
	if !t.Running {
		t.Access.Unlock()
		cancel()
		return nil
	}
	t.cancel = cancel
	t.Access.Unlock()
	defer func() {
		cancel()
		t.Access.Lock()
		t.cancel = nil
		t.Access.Unlock()
	}()
	done := make(chan error, 1)

	t.executions.Go(func() {
		done <- t.Execute(ctx)
	})

	select {
	case <-ctx.Done():
		t.Access.RLock()
		running := t.Running
		t.Access.RUnlock()
		if !running {
			return <-done
		}
		log.Errorf("Task %s execution timed out, reloading", t.Name)
		if t.ReloadCh != nil {
			select {
			case t.ReloadCh <- struct{}{}:
			default:
			}
		} else {
			log.Panic("Reload failed")
		}
		return nil
	case err := <-done:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
}

func (t *Task) safeStop() (context.CancelFunc, <-chan struct{}) {
	t.Access.Lock()
	cancel := t.cancel
	done := t.Done
	if t.Running {
		t.Running = false
		close(t.Stop)
	}
	t.Access.Unlock()
	return cancel, done
}

func (t *Task) Close() {
	cancel, done := t.safeStop()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	t.executions.Wait()
	log.Warningf("Task %s stopped", t.Name)
}
