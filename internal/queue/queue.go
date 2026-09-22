// Package queue provides background jobs over asynq (Redis), mirroring the
// BullMQ "test" queue: dedup by message, retries, and bounded concurrency.
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/logger"
)

// TaskTypeTest is the asynq task type for the test queue.
const TaskTypeTest = "test:job"

// dedupTTL matches the BullMQ deduplication window (60s).
const dedupTTL = 60 * time.Second

// concurrency matches the BullMQ worker concurrency (5).
const concurrency = 5

// maxRetry matches the BullMQ retry policy (3 attempts).
const maxRetry = 3

// TestJobPayload is the job data {message, date}. RequestID carries the id of
// the request that enqueued the job: asynq has no task headers, so correlation
// has to travel in the payload. Every payload type should keep this field —
// jobLogger reads it back by name, whatever the rest of the payload looks like.
type TestJobPayload struct {
	Message   string `json:"message"`
	Date      string `json:"date"`
	RequestID string `json:"request_id,omitempty"`
}

// Client enqueues background jobs.
type Client struct {
	client *asynq.Client
}

// NewClient builds an enqueuer reusing the shared go-redis client. It takes no
// logger: enqueueing always happens in request scope, so it logs through
// logger.From(ctx) and keeps the request_id.
func NewClient(rdb redis.UniversalClient) *Client {
	return &Client{client: asynq.NewClientFromRedisClient(rdb)}
}

// Close releases the underlying asynq client.
func (c *Client) Close() error {
	if err := c.client.Close(); err != nil {
		return fmt.Errorf("close queue client: %w", err)
	}
	return nil
}

// AddTestJob enqueues a test job for userID, deduplicated per user and message
// for dedupTTL.
func (c *Client) AddTestJob(ctx context.Context, userID int32, message string) error {
	payload, err := json.Marshal(TestJobPayload{
		Message:   message,
		Date:      time.Now().UTC().Format(time.RFC3339),
		RequestID: chimw.GetReqID(ctx),
	})
	if err != nil {
		return fmt.Errorf("marshal test job: %w", err)
	}
	task := asynq.NewTask(TaskTypeTest, payload)
	taskID := testJobTaskID(userID, message)

	_, err = c.client.EnqueueContext(
		ctx, task,
		asynq.TaskID(taskID),      // dedup by user and message
		asynq.Retention(dedupTTL), // keep id ~60s after completion
		asynq.MaxRetry(maxRetry),
	)
	// A conflicting id means the same message is already queued/recent — that is
	// the intended dedup behaviour, so report success.
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		logger.From(ctx).Debug("test job deduplicated", "task_id", taskID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue test job: %w", err)
	}
	return nil
}

// testJobTaskID is the dedup id of a test job. It is scoped to the caller, so
// one user's message never swallows another user's identical one, and it is a
// digest, never the raw message: the task id rides on every job log line.
func testJobTaskID(userID int32, message string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(int64(userID), 10) + ":" + message))
	return "dedup:test:" + hex.EncodeToString(sum[:])
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
	mux.Use(w.jobLogger, w.recoverer, w.accessLog)
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
				logger.From(ctx).LogAttrs(
					ctx, slog.LevelError, "job_panic",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				err = fmt.Errorf("panic in job %s: %v", t.Type(), rec)
			}
		}()
		return next.ProcessTask(ctx, t)
	})
}

// jobLogger stores a job-scoped logger (type, task_id and — when the payload
// carries one — the request_id that enqueued the job) in ctx, so every line a
// handler writes via logger.From(ctx) is correlated. Background analog of
// middleware.ContextLogger; must be the outermost job middleware.
func (w *Worker) jobLogger(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		taskID, _ := asynq.GetTaskID(ctx)
		log := w.log.With(slog.String("type", t.Type()), slog.String("task_id", taskID))
		if reqID := payloadRequestID(t); reqID != "" {
			log = log.With(slog.String("request_id", reqID))
		}
		return next.ProcessTask(logger.Into(ctx, log), t)
	})
}

// payloadRequestID digs the enqueueing request's id out of any payload that has
// a "request_id" field, ignoring everything else in it. A payload without one
// (or one that is not a JSON object) simply yields "".
func payloadRequestID(t *asynq.Task) string {
	var meta struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(t.Payload(), &meta); err != nil {
		return ""
	}
	return meta.RequestID
}

// accessLog logs each job's start and completion with duration and outcome.
// A failed job logs at Error so `level=ERROR` selects it, matching the HTTP side.
func (w *Worker) accessLog(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		start := time.Now()
		log := logger.From(ctx)
		log.LogAttrs(ctx, slog.LevelInfo, "job_started")
		err := next.ProcessTask(ctx, t)
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
		}
		log.LogAttrs(
			ctx, level, "job_finished",
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
		// asynq calls the ErrorHandler outside the mux, so ctx never passed
		// through jobLogger — the correlation fields are rebuilt here by hand.
		log.LogAttrs(
			ctx, slog.LevelError, "job_failed",
			slog.String("type", t.Type()),
			slog.String("task_id", taskID),
			slog.String("request_id", payloadRequestID(t)),
			slog.Int("attempt", retried),
			slog.Int("max_retry", maxRetry),
			slog.String("error", err.Error()),
		)
	})
}

// Stop gracefully shuts the worker down.
func (w *Worker) Stop() { w.srv.Shutdown() }

func (w *Worker) handleTest(ctx context.Context, t *asynq.Task) error {
	var p TestJobPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmarshal test payload: %w", err)
	}
	// The message is user text: log its size, never its content (mutation
	// variables are kept out of the GraphQL log for the same reason).
	log := logger.From(ctx)
	log.Info("processing test job", "message_len", len(p.Message), "date", p.Date)
	time.Sleep(time.Second) // mirror the TS 1s simulated work
	log.Info("finished test job", "message_len", len(p.Message))
	return nil
}

// asynqLogger adapts slog to the asynq.Logger interface.
type asynqLogger struct{ log *slog.Logger }

func (l *asynqLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l *asynqLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l *asynqLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l *asynqLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }
func (l *asynqLogger) Fatal(args ...any) { l.log.Error(fmt.Sprint(args...)) }
