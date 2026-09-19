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

	coderws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/uxname/liteend-go/internal/app"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db"
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

	// pgPassword carries every character that breaks a connection URL assembled
	// by hand ("/", "#", "?", "%", "@"). The whole suite boots through it, so both
	// database paths — the pgx pool and the database/sql handle goose migrates
	// with — prove they survive a password a generator would actually produce.
	pgPassword = "p@ss/w#rd?%41"
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgC, err := tcpostgres.Run(
		ctx, "postgres:18.1-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword(pgPassword),
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
	must(createBucket(ctx, s3Addr))
	publicBaseURL = "http://" + s3Addr + "/" + s3Bucket

	setenv(map[string]string{
		"PORT":              "4000",
		"DATABASE_HOST":     pgHost,
		"DATABASE_PORT":     pgPort.Port(),
		"DATABASE_USER":     "postgres",
		"DATABASE_PASSWORD": pgPassword,
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
		// Files are private (the default): the bucket refuses anonymous readers
		// and the API hands out links signed for this long.
		"FILE_LINK_TTL_MINUTES": "5",
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

// CreateProfile is an upsert: asked twice for the same subject it answers with
// the same row instead of a unique violation. That is what lets the profile
// service do without its own "insert, catch 23505, select again" — two replicas
// racing to create a first-time user both get the profile, straight from SQL.
func TestCreateProfile_IsAnUpsert(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)
	database, err := db.New(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(database.Close)

	first, err := database.Queries.CreateProfile(t.Context(), "upsert-sub")
	require.NoError(t, err)
	second, err := database.Queries.CreateProfile(t.Context(), "upsert-sub")
	require.NoError(t, err, "a second create for the same subject must not be a unique violation")
	require.Equal(t, first.ID, second.ID, "both calls answer with the one row")
}

// C9: liveness and readiness are separate endpoints on the assembled app. With
// the real Postgres and Redis containers up, both report ok.
func TestHealth(t *testing.T) {
	for _, path := range []string{"/livez", "/readyz"} {
		resp, err := http.Get(server.URL + path)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equalf(t, http.StatusOK, resp.StatusCode, "GET %s", path)
		require.Containsf(t, string(body), `"status":"ok"`, "GET %s", path)
	}
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
// absolute SIGNED link into that storage, which anyone fetches without this app
// in the path — so a file written by one replica is readable by all of them.
// The door on that same file is tested right below.
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
	require.Contains(t, link, "X-Amz-Signature=", "private files are handed out as signed links")
	require.Contains(t, link, "X-Amz-Expires=300", "the link expires per FILE_LINK_TTL_MINUTES")
	require.Equal(t, strings.TrimPrefix(strings.Split(link, "?")[0], publicBaseURL+"/"), saved[0]["key"],
		"the response also carries the permanent key the link was signed for")

	// Download with no credentials of our own — the signature in the URL is the
	// whole authorisation, and no copy of the app is in the path.
	dl, err := http.Get(link)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode, "a signed link must serve the object")
	body, err := io.ReadAll(dl.Body)
	require.NoError(t, err)
	require.Equal(t, png, body, "the object must be byte-identical to the upload")
}

// The door test: in private mode (the default) the SAME file, asked for without
// the signature, is refused by the storage. If a future change opens the bucket
// to anonymous reads — or drops the signing and hands out permanent links — this
// goes red, which is the only way that regression is ever noticed.
func TestFilesArePrivate_UnsignedRequestIsRefused(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfakepngdata")
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := createImagePart(w, "file", "private.png")
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

	// The permanent address of the very file just uploaded: same URL, signature
	// removed. This is what a leaked link looks like once it has expired, and
	// what someone guessing keys would try.
	unsigned := publicBaseURL + "/" + saved[0]["key"]
	dl, err := http.Get(unsigned)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusForbidden, dl.StatusCode,
		"an unauthenticated, unsigned request for a private file must be refused")

	// And the signed link to the same object still works, so the refusal above
	// is the missing signature — not a broken upload.
	ok, err := http.Get(saved[0]["path"])
	require.NoError(t, err)
	defer ok.Body.Close()
	require.Equal(t, http.StatusOK, ok.StatusCode)
}

// The file a client names as its avatar has to be its own. A key is not a
// secret — it travels in every link the API hands out — so without this check
// anyone could name a key read off someone else's expired link and have a
// working one signed for it.
func TestFilesArePrivate_RefusesAFileTheCallerDidNotUpload(t *testing.T) {
	// A key in the right shape that no upload ever recorded: exactly what an
	// attacker has after reading one off a screenshot, since the row is what
	// carries the owner.
	stranger := publicBaseURL + "/2026/01/02/03-04/00000000-0000-4000-8000-000000000000.png"
	errs := gqlErr(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){avatarUrl}}`,
		map[string]any{"i": map[string]any{"avatarUrl": stranger}})
	require.NotEmpty(t, errs, "a file the caller did not upload must be refused")
	require.Contains(t, fmt.Sprint(errs[0]["message"]), "file you uploaded")

	// The profile is untouched: the mutation failed before anything was written.
	data := gql(t, `{ me { avatarUrl } }`, nil, nil)
	avatar, _ := data["me"].(map[string]any)["avatarUrl"].(string)
	require.NotContains(t, avatar, "00000000-0000-4000-8000-000000000000")
}

// The avatar a client stores is the permanent key form, and every read hands
// back a freshly signed link for it — a stored signature would expire.
func TestFilesArePrivate_AvatarIsReSignedOnEveryRead(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfakepngdata")
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := createImagePart(w, "file", "avatar.png")
	_, _ = part.Write(png)
	_ = w.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var saved []map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&saved))

	// Hand the signed link straight back, the way a browser client does.
	data := gql(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){avatarUrl}}`,
		map[string]any{"i": map[string]any{"avatarUrl": saved[0]["path"]}}, nil)
	updated := data["updateProfile"].(map[string]any)["avatarUrl"].(string)
	require.Contains(t, updated, "X-Amz-Signature=")

	// Read it back through a later mutation (the mock user's `me` always
	// re-stamps its own avatar, so it cannot answer this question). The value
	// that survived the round trip is the permanent one, signed again on the way
	// out — a stored signature would have come back stale instead.
	again := gql(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){avatarUrl}}`,
		map[string]any{"i": map[string]any{"bio": "unrelated"}}, nil)
	served := again["updateProfile"].(map[string]any)["avatarUrl"].(string)
	require.Contains(t, served, "X-Amz-Signature=", "every read signs the link again")
	require.Equal(t,
		strings.Split(updated, "?")[0], strings.Split(served, "?")[0],
		"…for the same object that was uploaded")

	dl, err := http.Get(served)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode, "the link served with the profile opens the file")
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
	opts := &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := coderws.Dial(context.Background(), wsURL, opts)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	send := func(v any) { require.NoError(t, wsjson.Write(context.Background(), conn, v)) }
	send(map[string]any{"type": "connection_init", "payload": map[string]any{"x-mock-sub": ""}})

	// One context covers the whole wait for the ack. A read that runs out of
	// budget kills the connection, so a per-read deadline re-armed in a retry
	// loop cannot recover — it only spins on a dead socket.
	ackCtx, cancelAck := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAck()
	for {
		var msg map[string]any
		require.NoError(t, wsjson.Read(ackCtx, conn, &msg), "waiting for connection_ack")
		if msg["type"] == "connection_ack" {
			break
		}
	}

	send(map[string]any{
		"id":   "1",
		"type": "subscribe",
		"payload": map[string]any{
			"query": `subscription{ profileUpdated { displayName } }`,
		},
	})

	// The subscription registers asynchronously on the server, so the very first
	// publish can be dropped; re-publishing the same value is harmless, so the
	// mutation is retried. The socket, however, is read exactly once through, by
	// one goroutine under one deadline — retrying the read instead (a short
	// deadline re-armed on every attempt) is what made this test flaky: the first
	// expired deadline poisoned Conn.readErr and every later attempt returned that
	// cached error within microseconds, so the retry loop could never recover.
	event := make(chan error, 1)
	eventCtx, cancelEvent := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEvent()
	go func() {
		for {
			var msg map[string]any
			if err := wsjson.Read(eventCtx, conn, &msg); err != nil {
				event <- err
				return
			}
			switch msg["type"] {
			case "next":
				event <- nil
				return
			case "error", "complete":
				event <- fmt.Errorf("subscription ended with %q: %v", msg["type"], msg["payload"])
				return
			}
		}
	}()

	// Publishing stays on the test goroutine: gql asserts through require, which
	// is only legal here, and a rejected mutation (429, a GraphQL error) must fail
	// the test where it happens instead of being swallowed as a missing event.
	giveUp := time.After(10 * time.Second)
	for {
		gql(t, `mutation($i:ProfileUpdateInput!){updateProfile(input:$i){displayName}}`,
			map[string]any{"i": map[string]any{"displayName": "WSName"}}, nil)
		select {
		case err := <-event:
			require.NoError(t, err, "the subscription must deliver the profile update")
			return
		case <-time.After(300 * time.Millisecond):
		case <-giveUp:
			t.Fatal("no subscription event after repeated profile updates")
		}
	}
}

// --- helpers ---

// gqlErr is gql for the cases where the error IS the assertion: it returns the
// GraphQL errors instead of failing on them.
func gqlErr(t *testing.T, query string, vars map[string]any) []map[string]any {
	t.Helper()
	reqBody, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/graphql", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "POST /graphql answered %d: %s", resp.StatusCode, body)

	var out struct {
		Errors []map[string]any `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	return out.Errors
}

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
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// A rejected request (429 from the rate limiter, 5xx) carries the
	// {"statusCode","message"} envelope of internal/httperr, which has no
	// "errors" key — without this check it would decode into an empty struct and
	// pass silently.
	require.Equalf(t, http.StatusOK, resp.StatusCode, "POST /graphql answered %d: %s", resp.StatusCode, body)

	var out struct {
		Data   map[string]any   `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Empty(t, out.Errors, "graphql errors: %v", out.Errors)
	return out.Data
}

func createImagePart(w *multipart.Writer, field, filename string) (io.Writer, error) {
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename)}
	h["Content-Type"] = []string{"image/png"}
	return w.CreatePart(h)
}

// createBucket creates the uploads bucket and leaves it CLOSED to anonymous
// readers — the state the storage init container leaves behind in the deployed
// stack when FILE_VISIBILITY is private, which is the default. The door test
// below is what makes that closed door a fact rather than an assumption.
func createBucket(ctx context.Context, addr string) error {
	cl, err := minio.New(addr, &minio.Options{
		Creds:  credentials.NewStaticV4(s3RootUser, s3RootPass, ""),
		Secure: false,
	})
	if err != nil {
		return err
	}
	return cl.MakeBucket(ctx, s3Bucket, minio.MakeBucketOptions{})
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

	dial := func(origin string) (*coderws.Conn, *http.Response, error) {
		opts := &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}}
		if origin != "" {
			opts.HTTPHeader = http.Header{"Origin": {origin}}
		}
		return coderws.Dial(context.Background(), wsURL, opts)
	}

	t.Run("allowed origin completes the handshake", func(t *testing.T) {
		conn, _, err := dial("http://localhost:3000")
		require.NoError(t, err, "the SPA origin is in CORS_ORIGIN and must connect")
		require.NoError(t, conn.Close(coderws.StatusNormalClosure, ""))
	})

	t.Run("foreign origin is refused", func(t *testing.T) {
		conn, resp, err := dial("http://evil.example")
		if conn != nil {
			_ = conn.CloseNow()
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
		require.NoError(t, conn.Close(coderws.StatusNormalClosure, ""))
	})
}
