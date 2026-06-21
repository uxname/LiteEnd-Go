//go:build integration

// Package test contains end-to-end integration tests backed by real Postgres
// and Redis containers (testcontainers-go). Run with: go test -tags=integration ./test
package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/uxname/liteend-go/internal/app"
	"github.com/uxname/liteend-go/internal/config"
)

var (
	server  *httptest.Server
	appInst *app.App
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgC, err := tcpostgres.Run(
		ctx, "postgres:18.1-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second),
		),
	)
	must(err)
	rdC, err := tcredis.Run(ctx, "redis:8-alpine")
	must(err)

	pgHost, _ := pgC.Host(ctx)
	pgPort, _ := pgC.MappedPort(ctx, "5432/tcp")
	rdHost, _ := rdC.Host(ctx)
	rdPort, _ := rdC.MappedPort(ctx, "6379/tcp")

	setenv(map[string]string{
		"PORT":              "4000",
		"DATABASE_HOST":     pgHost,
		"DATABASE_PORT":     pgPort.Port(),
		"DATABASE_USER":     "postgres",
		"DATABASE_PASSWORD": "postgres",
		"DATABASE_NAME":     "postgres",
		"REDIS_HOST":        rdHost,
		"REDIS_PORT":        rdPort.Port(),
		"REDIS_PASSWORD":    "",
		"OIDC_ISSUER":       "http://localhost/oidc",
		"OIDC_AUDIENCE":     "test",
		"OIDC_JWKS_URI":     "http://localhost/oidc/jwks",
		"OIDC_MOCK_ENABLED": "true",
	})

	cfg, err := config.Load()
	must(err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	appInst, err = app.Build(ctx, cfg, log)
	must(err)
	server = httptest.NewServer(appInst.Server.Router())

	code := m.Run()

	server.Close()
	appInst.Close()
	_ = pgC.Terminate(ctx)
	_ = rdC.Terminate(ctx)
	os.Exit(code)
}

func TestHealth(t *testing.T) {
	resp, err := http.Get(server.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), `"status":"ok"`)
}

func TestGraphQL_Me(t *testing.T) {
	data := gql(t, `{ me { id oidcSub roles } }`, nil, nil)
	me := data["me"].(map[string]any)
	require.Equal(t, "mock-oidc-sub", me["oidcSub"])
	require.Contains(t, fmt.Sprint(me["roles"]), "ADMIN")
}

func TestGraphQL_UpdateProfileAndCacheRefresh(t *testing.T) {
	gql(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){displayName}}`,
		map[string]any{"i": map[string]any{"displayName": "IntTest", "bio": "b"}}, nil)

	data := gql(t, `{ me { displayName bio } }`, nil, nil)
	me := data["me"].(map[string]any)
	require.Equal(t, "IntTest", me["displayName"], "cache must reflect update, not be stale")
	require.Equal(t, "b", me["bio"])
}

func TestGraphQL_DebugAdminOnly(t *testing.T) {
	data := gql(t, `{ debug }`, nil, nil)
	debug := data["debug"].(map[string]any)
	require.Contains(t, debug, "uptime")
	require.Contains(t, debug, "totalUsers")
}

func TestGraphQL_TestTranslation_RU(t *testing.T) {
	data := gql(t, `{ testTranslation(username:"Иван") }`, nil,
		map[string]string{"Accept-Language": "ru"})
	require.Equal(t, "Привет Иван!", data["testTranslation"])
}

func TestGraphQL_AddTestJob(t *testing.T) {
	data := gql(t, `mutation{ addTestJob(message:"int-job") }`, nil, nil)
	require.Equal(t, true, data["addTestJob"])
}

func TestUploadAndServe(t *testing.T) {
	// minimal PNG
	png := []byte("\x89PNG\r\n\x1a\nfakepngdata")
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := createImagePart(w, "file", "pic.png")
	_, _ = part.Write(png)
	_ = w.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var saved []map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&saved))
	require.Len(t, saved, 1)

	// download it back
	dl, err := http.Get(server.URL + saved[0]["path"])
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode)
}

func TestUpload_RejectsNonImage(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "doc.txt")
	_, _ = part.Write([]byte("hello"))
	_ = w.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestSubscription_ProfileUpdated verifies the graphql-transport-ws flow:
// connect → init → subscribe → trigger updateProfile → receive event.
func TestSubscription_ProfileUpdated(t *testing.T) {
	wsURL := "ws" + server.URL[len("http"):] + "/graphql"
	header := http.Header{"Sec-WebSocket-Protocol": {"graphql-transport-ws"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	send := func(v any) { require.NoError(t, conn.WriteJSON(v)) }
	send(map[string]any{"type": "connection_init", "payload": map[string]any{"x-mock-sub": ""}})

	// expect connection_ack
	require.Eventually(t, func() bool {
		var msg map[string]any
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.ReadJSON(&msg); err != nil {
			return false
		}
		return msg["type"] == "connection_ack"
	}, 6*time.Second, 10*time.Millisecond)

	send(map[string]any{
		"id":   "1",
		"type": "subscribe",
		"payload": map[string]any{
			"query": `subscription{ profileUpdated { displayName } }`,
		},
	})

	// The subscription registers asynchronously on the server. Instead of a fixed
	// sleep (flaky on slow machines), retry the update until the subscription
	// delivers a "next" event. Re-publishing the same value is harmless.
	require.Eventually(t, func() bool {
		gql(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){displayName}}`,
			map[string]any{"i": map[string]any{"displayName": "WSName"}}, nil)
		var msg map[string]any
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&msg); err != nil {
			return false
		}
		return msg["type"] == "next"
	}, 10*time.Second, 100*time.Millisecond, "should receive a subscription event after profile update")
}

// --- helpers ---

func gql(t *testing.T, query string, vars map[string]any, headers map[string]string) map[string]any {
	t.Helper()
	reqBody, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/graphql", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var out struct {
		Data   map[string]any   `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Empty(t, out.Errors, "graphql errors: %v", out.Errors)
	return out.Data
}

func createImagePart(w *multipart.Writer, field, filename string) (io.Writer, error) {
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename)}
	h["Content-Type"] = []string{"image/png"}
	return w.CreatePart(h)
}

func setenv(m map[string]string) {
	for k, v := range m {
		_ = os.Setenv(k, v)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
