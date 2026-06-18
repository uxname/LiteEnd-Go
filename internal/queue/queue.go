// Package queue provides background jobs over asynq (Redis), mirroring the
// BullMQ "test" queue: dedup by message, retries, and bounded concurrency.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
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
		Concurrency:  concurrency,
		Logger:       &asynqLogger{log},
		ErrorHandler: errorHandler(log),
	})
	return &Worker{srv: srv, log: log}
}

// Start runs the worker in the background (non-blocking). Every handler is
// wrapped with panic recovery and an access log (the background-job analog of
// the HTTP middleware), so failures are never silent.
func (w *Worker) Start() error {
	mux := asynq.NewServeMux()
	mux.Use(w.recoverer, w.accessLog)
	mux.HandleFunc(TaskTypeTest, w.handleTest)
	if err := w.srv.Start(mux); err != nil {
		return fmt.Errorf("start queue worker: %w", err)
	}
	return nil
}

// recoverer turns a panic in a job handler into a logged error (with stack) and
// a returned error, so asynq retries the task instead of crashing the worker.
func (w *Worker) recoverer(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				w.log.LogAttrs(
					ctx, slog.LevelError, "job_panic",
					slog.String("type", t.Type()),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				err = fmt.Errorf("panic in job %s: %v", t.Type(), rec)
			}
		}()
		return next.ProcessTask(ctx, t)
	})
}

// accessLog logs each job's start and completion with type, id and duration.
func (w *Worker) accessLog(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		start := time.Now()
		taskID, _ := asynq.GetTaskID(ctx)
		w.log.LogAttrs(
			ctx, slog.LevelInfo, "job_started",
			slog.String("type", t.Type()),
			slog.String("task_id", taskID),
		)
		err := next.ProcessTask(ctx, t)
		w.log.LogAttrs(
			ctx, slog.LevelInfo, "job_finished",
			slog.String("type", t.Type()),
			slog.String("task_id", taskID),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.Bool("ok", err == nil),
		)
		return err //nolint:wrapcheck // pass the handler error through unchanged for asynq retry semantics
	})
}

// errorHandler logs every failed task with its type, id, attempt and error, so
// background failures are visible even though there is no HTTP response.
func errorHandler(log *slog.Logger) asynq.ErrorHandler {
	return asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, err error) {
		taskID, _ := asynq.GetTaskID(ctx)
		retried, _ := asynq.GetRetryCount(ctx)
		maxRetry, _ := asynq.GetMaxRetry(ctx)
		log.LogAttrs(
			ctx, slog.LevelError, "job_failed",
			slog.String("type", t.Type()),
			slog.String("task_id", taskID),
			slog.Int("attempt", retried),
			slog.Int("max_retry", maxRetry),
			slog.String("error", err.Error()),
		)
	})
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
