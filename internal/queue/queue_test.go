package queue

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
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
