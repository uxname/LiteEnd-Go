package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/logger"
)

func testWorker() *Worker {
	return &Worker{log: slog.New(slog.DiscardHandler)}
}

func TestHandleTest_RejectsInvalidPayload(t *testing.T) {
	t.Parallel()
	w := testWorker()
	task := asynq.NewTask(TaskTypeTest, []byte("not-json"))
	err := w.handleTest(context.Background(), task)
	require.Error(t, err)
}

func TestHandleTest_ProcessesValidPayload(t *testing.T) {
	t.Parallel()
	w := testWorker()
	task := asynq.NewTask(TaskTypeTest, []byte(`{"message":"hi","date":"2026-01-01T00:00:00Z"}`))
	err := w.handleTest(context.Background(), task)
	require.NoError(t, err)
}

// The task id deduplicates per caller (one user's message must not silently
// swallow another user's) and never carries the raw message, which would
// otherwise land in every job log line as task_id.
func TestTestJobTaskID_IsPerUserAndOpaque(t *testing.T) {
	t.Parallel()
	a := testJobTaskID(1, "secret message")

	require.Equal(t, a, testJobTaskID(1, "secret message"), "same user and message dedup")
	require.NotEqual(t, a, testJobTaskID(2, "secret message"), "another user gets their own job")
	require.NotContains(t, a, "secret")
	require.LessOrEqual(t, len(a), 80)
}

// The worker logs the job, not the user's text.
func TestHandleTest_DoesNotLogTheMessage(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	w := &Worker{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	ctx := logger.Into(context.Background(), w.log)
	task := asynq.NewTask(TaskTypeTest, []byte(`{"message":"secret message","date":"2026-01-01T00:00:00Z"}`))

	require.NoError(t, w.handleTest(ctx, task))
	require.NotContains(t, logs.String(), "secret message")
	require.Contains(t, logs.String(), `"message_len":14`)
}

func TestRecoverer_TurnsPanicIntoError(t *testing.T) {
	t.Parallel()
	w := testWorker()
	wrapped := w.recoverer(asynq.HandlerFunc(func(_ context.Context, _ *asynq.Task) error {
		panic("boom")
	}))
	task := asynq.NewTask(TaskTypeTest, []byte("{}"))

	require.NotPanics(t, func() {
		err := wrapped.ProcessTask(context.Background(), task)
		require.Error(t, err, "a panicking handler must surface an error so asynq retries")
	})
}

func TestRecoverer_PassesThroughSuccess(t *testing.T) {
	t.Parallel()
	w := testWorker()
	wrapped := w.recoverer(asynq.HandlerFunc(func(_ context.Context, _ *asynq.Task) error {
		return nil
	}))
	err := wrapped.ProcessTask(context.Background(), asynq.NewTask(TaskTypeTest, []byte("{}")))
	require.NoError(t, err)
}

func TestAccessLog_PassesHandlerErrorUnchanged(t *testing.T) {
	t.Parallel()
	w := testWorker()
	sentinel := errors.New("handler failed")
	wrapped := w.accessLog(asynq.HandlerFunc(func(_ context.Context, _ *asynq.Task) error {
		return sentinel
	}))
	err := wrapped.ProcessTask(context.Background(), asynq.NewTask(TaskTypeTest, []byte("{}")))
	require.ErrorIs(t, err, sentinel)
}

func TestErrorHandler_DoesNotPanic(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	h := errorHandler(log)
	require.NotPanics(t, func() {
		h.HandleError(
			context.Background(),
			asynq.NewTask(TaskTypeTest, []byte("{}")),
			errors.New("job blew up"),
		)
	})
}

func TestAsynqLogger_AllLevelsAreSafe(t *testing.T) {
	t.Parallel()
	l := &asynqLogger{log: slog.New(slog.DiscardHandler)}
	require.NotPanics(t, func() {
		l.Debug("d")
		l.Info("i")
		l.Warn("w")
		l.Error("e")
		l.Fatal("f")
	})
}

func TestPayloadRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "req-42", payloadRequestID(
		asynq.NewTask(TaskTypeTest, []byte(`{"message":"hi","request_id":"req-42"}`))))
	require.Empty(t, payloadRequestID(asynq.NewTask(TaskTypeTest, []byte(`{"message":"hi"}`))),
		"a payload without the field is fine, just uncorrelated")
	require.Empty(t, payloadRequestID(asynq.NewTask(TaskTypeTest, []byte("not-json"))))
}

// A job enqueued by a request must be traceable back to it: asynq carries no
// headers, so the id travels in the payload and jobLogger puts it back on every
// line the handler writes.
func TestJobLogger_CorrelatesToTheEnqueueingRequest(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := &Worker{log: slog.New(slog.NewJSONHandler(&buf, nil))}

	handler := w.jobLogger(asynq.HandlerFunc(func(ctx context.Context, _ *asynq.Task) error {
		logger.From(ctx).Info("doing work")
		return nil
	}))
	task := asynq.NewTask(TaskTypeTest, []byte(`{"message":"hi","request_id":"req-7"}`))
	require.NoError(t, handler.ProcessTask(context.Background(), task))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "req-7", line["request_id"])
	require.Equal(t, TaskTypeTest, line["type"])
}
