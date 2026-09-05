//go:build integration

// Package test contains end-to-end integration tests backed by real Postgres,
// Redis and object-storage containers (testcontainers-go). Run with:
// go test -tags=integration ./test
package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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
	// publicBaseURL is the browser-facing prefix of every stored object, i.e.
	// what the app hands out as an upload link.
	publicBaseURL string
)

// Object storage credentials for the throwaway MinIO container. MinIO is the
// S3-compatible server used here because it is S3-ready the moment it boots;
// the code under test only speaks S3 through minio-go.
const (
	s3Image    = "minio/minio:RELEASE.2025-09-07T16-13-09Z"
	s3RootUser = "liteend-test-user"
	s3RootPass = "liteend-test-pass"
	s3Bucket   = "uploads"
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
	s3C, err := testcontainers.Run(
		ctx, s3Image,
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     s3RootUser,
			"MINIO_ROOT_PASSWORD": s3RootPass,
		}),
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(60*time.Second),
		),
	)
	must(err)

	pgHost, _ := pgC.Host(ctx)
	pgPort, _ := pgC.MappedPort(ctx, "5432/tcp")
	rdHost, _ := rdC.Host(ctx)
	rdPort, _ := rdC.MappedPort(ctx, "6379/tcp")
	s3Host, _ := s3C.Host(ctx)
	s3Port, _ := s3C.MappedPort(ctx, "9000/tcp")
	s3Addr := net.JoinHostPort(s3Host, s3Port.Port())
	must(createPublicBucket(ctx, s3Addr))
	publicBaseURL = "http://" + s3Addr + "/" + s3Bucket

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
		// Uploads go to shared object storage, so every replica reads what any
		// other wrote. S3_ENDPOINT is the address the app uses; S3_PUBLIC_BASE_URL
		// is the one handed to the browser (here they coincide, in the stack the
		// public one goes through the proxy).
		"S3_ENDPOINT":          "http://" + s3Addr,
		"S3_ACCESS_KEY_ID":     s3RootUser,
		"S3_SECRET_ACCESS_KEY": s3RootPass,
		"S3_BUCKET":            s3Bucket,
		"S3_PUBLIC_BASE_URL":   publicBaseURL,
		// Explicit allowlist, as in production: it drives both CORS and the
		// WebSocket handshake authorization. Left empty, every origin is allowed
		// and the origin checks below would prove nothing.
		"CORS_ORIGIN": "http://localhost:3000",
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
	_ = s3C.Terminate(ctx)
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

// TestC5_UploadedFileIsReadableFromObjectStorage pins the replica-independent
// storage: POST /upload puts the file in the shared bucket and answers with an
// absolute link into that storage, which anyone fetches without this app in the
// path — so a file written by one replica is readable by all of them.
func TestC5_UploadedFileIsReadableFromObjectStorage(t *testing.T) {
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

	link := saved[0]["path"]
	require.True(t, strings.HasPrefix(link, publicBaseURL+"/"),
		"the link must address the storage, not this app: %q", link)
	require.NotContains(t, link, server.URL, "the app must not be in the download path")

	// Anonymous download, no credentials and no app involved.
	dl, err := http.Get(link)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode, "the stored object must be publicly readable")
	body, err := io.ReadAll(dl.Body)
	require.NoError(t, err)
	require.Equal(t, png, body, "the object must be byte-identical to the upload")
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

// createPublicBucket creates the uploads bucket and opens it for anonymous
// reads — the same state the storage init container leaves behind in the
// deployed stack, and what makes the handed-out links work in a browser.
func createPublicBucket(ctx context.Context, addr string) error {
	cl, err := minio.New(addr, &minio.Options{
		Creds:  credentials.NewStaticV4(s3RootUser, s3RootPass, ""),
		Secure: false,
	})
	if err != nil {
		return err
	}
	if err := cl.MakeBucket(ctx, s3Bucket, minio.MakeBucketOptions{}); err != nil {
		return err
	}
	return cl.SetBucketPolicy(ctx, s3Bucket, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::`+s3Bucket+`/*"]}]}`)
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

// TestWebsocket_OriginAuthorization pins the handshake allowlist introduced when
// gqlgen dropped the gorilla adapter: the transport now authorizes cross-origin
// WebSocket upgrades against CORS_ORIGIN instead of accepting any origin.
func TestWebsocket_OriginAuthorization(t *testing.T) {
	wsURL := "ws" + server.URL[len("http"):] + "/graphql"

	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		header := http.Header{"Sec-WebSocket-Protocol": {"graphql-transport-ws"}}
		if origin != "" {
			header.Set("Origin", origin)
		}
		return websocket.DefaultDialer.Dial(wsURL, header)
	}

	t.Run("allowed origin completes the handshake", func(t *testing.T) {
		conn, _, err := dial("http://localhost:3000")
		require.NoError(t, err, "the SPA origin is in CORS_ORIGIN and must connect")
		require.NoError(t, conn.Close())
	})

	t.Run("foreign origin is refused", func(t *testing.T) {
		conn, resp, err := dial("http://evil.example")
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "an origin outside CORS_ORIGIN must not get a socket")
		require.NotNil(t, resp)
		defer resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	// A non-browser client (no Origin header at all) is not subject to the check —
	// this is what the existing subscription test relies on.
	t.Run("absent origin still connects", func(t *testing.T) {
		conn, _, err := dial("")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	})
}
