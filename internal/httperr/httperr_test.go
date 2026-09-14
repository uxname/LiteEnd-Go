package httperr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// C3: httperr.Write sends consistent JSON error envelope with status code and message.
func TestC3_HttperrWrite(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Write(rec, http.StatusBadRequest, "invalid parameter")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var payload body
	err := json.Unmarshal(rec.Body.Bytes(), &payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, payload.StatusCode)
	require.Equal(t, "invalid parameter", payload.Message)
}
