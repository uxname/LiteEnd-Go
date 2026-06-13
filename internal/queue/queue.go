// Package queue provides background jobs over asynq (Redis), mirroring the
// BullMQ "test" queue: dedup by message, retries, and bounded concurrency.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// TaskTypeTest is the asynq task type for the test queue.
const TaskTypeTest = "test:job"

// dedupTTL matches the BullMQ deduplication window (60s).
const dedupTTL = 60 * time.Second

// concurrency matches the BullMQ worker concurrency (5).
const concurrency = 5

// maxRetry matches the BullMQ retry policy (3 attempts).
const maxRetry = 3

// TestJobPayload is the job data {message, date}.
type TestJobPayload struct {
	Message string `json:"message"`
	Date    string `json:"date"`
}

// Client enqueues background jobs.
type Client struct {
	client *asynq.Client
	log    *slog.Logger
}

// NewClient builds an enqueuer reusing the shared go-redis client.
func NewClient(rdb redis.UniversalClient, log *slog.Logger) *Client {
	return &Client{client: asynq.NewClientFromRedisClient(rdb), log: log}
}

// Close releases the underlying asynq client.
func (c *Client) Close() error {
	if err := c.client.Close(); err != nil {
		return fmt.Errorf("close queue client: %w", err)
	}
	return nil
}

// AddTestJob enqueues a test job, deduplicated by message for dedupTTL.
func (c *Client) AddTestJob(ctx context.Context, message string) error {
	payload, err := json.Marshal(TestJobPayload{
		Message: message,
		Date:    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal test job: %w", err)
	}
	task := asynq.NewTask(TaskTypeTest, payload)

	_, err = c.client.EnqueueContext(
		ctx, task,
		asynq.TaskID("dedup:test:"+message), // dedup by message
		asynq.Retention(dedupTTL),           // keep id ~60s after completion
		asynq.MaxRetry(maxRetry),
	)
	// A conflicting id means the same message is already queued/recent — that is
	// the intended dedup behaviour, so report success.
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		c.log.Debug("test job deduplicated", "message", message)
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue test job: %w", err)
	}
	return nil
}

// Worker runs the background job processor.
type Worker struct {
	srv *asynq.Server
	log *slog.Logger
}

// NewWorker builds the asynq server (processor) reusing the shared redis client.
func NewWorker(rdb redis.UniversalClient, log *slog.Logger) *Worker {
	srv := asynq.NewServerFromRedisClient(rdb, asynq.Config{
		Concurrency: concurrency,
		Logger:      &asynqLogger{log},
	})
	return &Worker{srv: srv, log: log}
}

// Start runs the worker in the background (non-blocking).
func (w *Worker) Start() error {
	mux := asynq.NewServeMux()
	mux.HandleFunc(TaskTypeTest, w.handleTest)
	if err := w.srv.Start(mux); err != nil {
		return fmt.Errorf("start queue worker: %w", err)
	}
	return nil
}

// Stop gracefully shuts the worker down.
func (w *Worker) Stop() { w.srv.Shutdown() }

func (w *Worker) handleTest(_ context.Context, t *asynq.Task) error {
	var p TestJobPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmarshal test payload: %w", err)
	}
	w.log.Info("processing test job", "message", p.Message, "date", p.Date)
	time.Sleep(time.Second) // mirror the TS 1s simulated work
	w.log.Info("finished test job", "message", p.Message)
	return nil
}

// asynqLogger adapts slog to the asynq.Logger interface.
type asynqLogger struct{ log *slog.Logger }

func (l *asynqLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l *asynqLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l *asynqLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l *asynqLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }
func (l *asynqLogger) Fatal(args ...any) { l.log.Error(fmt.Sprint(args...)) }
