package probeagent

import (
	"context"
	"errors"
	"time"

	log "github.com/sirupsen/logrus"
)

type Runner struct {
	client       *Client
	pollInterval time.Duration
}

func NewRunner(client *Client, pollInterval time.Duration) *Runner {
	if pollInterval < time.Second {
		pollInterval = 5 * time.Second
	}
	return &Runner{client: client, pollInterval: pollInterval}
}

func (r *Runner) Run(ctx context.Context) error {
	if r.client == nil {
		return errors.New("probe client is required")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		task, err := r.client.Claim(ctx)
		if err != nil {
			log.WithError(err).Warn("claim connectivity probe task failed")
			timer.Reset(r.pollInterval)
			continue
		}
		if task == nil {
			timer.Reset(r.pollInterval)
			continue
		}
		log.WithFields(log.Fields{
			"task_id":   task.TaskID,
			"endpoints": len(task.Endpoints),
		}).Info("running connectivity probe task")
		checks := Check(ctx, task)
		if err := r.client.Complete(ctx, task, checks); err != nil {
			log.WithError(err).WithField("task_id", task.TaskID).
				Error("complete connectivity probe task failed")
			timer.Reset(r.pollInterval)
			continue
		}
		log.WithField("task_id", task.TaskID).Info("connectivity probe task completed")
		timer.Reset(0)
	}
}
