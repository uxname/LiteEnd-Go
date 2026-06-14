// Package httperr writes the single REST error envelope used across the HTTP
// surface, so the {"statusCode","message"} shape stays defined in one place.
package httperr

import (
	"encoding/json"
	"net/http"
)

// body is the JSON error envelope. It holds only int+string fields so
// json.Marshal cannot fail (keeps errchkjson satisfied).
type body struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
}

// Write sends a JSON error response: {"statusCode":code,"message":msg}.
// Callers that need extra headers (e.g. WWW-Authenticate) must set them before
// calling Write, since this writes the status code and body.
func Write(w http.ResponseWriter, code int, msg string) {
	payload, err := json.Marshal(body{StatusCode: code, Message: msg})
	if err != nil {
		// Unreachable for a scalar-only struct, but keeps the encoding honest.
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(payload)
}
