package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/logger"
	appmw "github.com/uxname/liteend-go/internal/middleware"
)

// captureWriteErr runs writeErr with a capturing logger and returns the line.
func captureWriteErr(t *testing.T, code int, msg string, cause error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx := logger.Into(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))

	rec := httptest.NewRecorder()
	writeErr(ctx, rec, code, msg, cause)
	require.Equal(t, code, rec.Code)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

// The client-facing message is deliberately vague, so the reason has to reach
// the log — a 500 here used to leave nothing but a status code.
func TestWriteErr_ServerFaultLogsTheCause(t *testing.T) {
	t.Parallel()
	line := captureWriteErr(t, http.StatusInternalServerError, "Failed to save metadata",
		errors.New("connection refused"))

	require.Equal(t, "upload_error", line["msg"])
	require.Equal(t, slog.LevelError.String(), line["level"])
	require.Equal(t, "Failed to save metadata", line["reason"])
	require.Equal(t, "connection refused", line["error"])
}

// Rejected input is the caller's fault, not ours: Warn, and there may be no
// underlying error at all.
func TestWriteErr_RejectedInputWarnsWithoutACause(t *testing.T) {
	t.Parallel()
	line := captureWriteErr(t, http.StatusBadRequest, "Too many files", nil)

	require.Equal(t, slog.LevelWarn.String(), line["level"])
	require.Equal(t, "Too many files", line["reason"])
	require.NotContains(t, line, "error")
}

// ipCapturingWriter records the uploader_ip a committed batch is stored with.
type ipCapturingWriter struct {
	storedUploads
	ip    string
	owner *int32
}

func (w *ipCapturingWriter) CreateUpload(_ context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error) {
	w.ip = arg.UploaderIp
	w.owner = arg.UploaderProfileID
	return w.remember(arg, 1), nil
}

// pngUpload builds a single-file multipart body the handler will accept.
func pngUpload(t *testing.T) (body *bytes.Buffer, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="pic.png"`)
	h.Set("Content-Type", "image/png")
	part, err := mw.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write([]byte(pngMagic + "data"))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

// C8: uploader_ip is written from the address the proxy chain vouches for —
// RemoteAddr as middleware.RealIP resolved it against TRUSTED_PROXY_HOPS — and
// never from the raw X-Forwarded-For this handler can read. The handler used to
// take the leftmost entry of that header, so any caller could pick the address
// recorded against its own upload by typing one line.
func TestC8_UploaderIPComesFromTheTrustedSourceNotTheHeader(t *testing.T) {
	t.Parallel()
	const forged = "9.9.9.9"
	cases := []struct {
		name   string
		hops   int
		xff    string
		remote string
		want   string
	}{
		{
			name: "no proxy in front: the forged header is ignored entirely",
			hops: 0, xff: forged, remote: "203.0.113.9:41234", want: "203.0.113.9",
		},
		{
			name: "one proxy: the entry our proxy appended wins over the forged prefix",
			hops: 1, xff: forged + ", 203.0.113.9", remote: "10.0.0.2:5555", want: "203.0.113.9",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			svc, store := newSvc(t)
			writer := &ipCapturingWriter{}
			svc.q = writer

			body, contentType := pngUpload(t)
			req := httptest.NewRequest(http.MethodPost, "/upload", body)
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("X-Forwarded-For", c.xff)
			req.RemoteAddr = c.remote
			// The real route sits behind requireAuth, which is what puts the
			// profile in the context; this test drives the handler directly.
			req = req.WithContext(auth.WithUser(req.Context(), &sqlc.Profile{ID: testOwnerID}))

			rec := httptest.NewRecorder()
			appmw.RealIP(c.hops)(http.HandlerFunc(NewHandler(svc).upload)).ServeHTTP(rec, req)

			require.Equal(t, http.StatusCreated, rec.Code, "the upload itself must succeed")
			require.Len(t, store.objects, 1)
			require.Equal(t, c.want, writer.ip, "uploader_ip must come from the resolved address")
			require.NotEqual(t, forged, writer.ip, "a client-supplied address must never reach the database")
		})
	}
}
