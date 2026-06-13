// Command server is the LiteEnd-Go application entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/uxname/liteend-go/internal/app"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
	"github.com/uxname/liteend-go/internal/version"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "liteend-go server (version %s, commit %s)\n\n", version.AppVersion, version.Commit)
		fmt.Fprintf(os.Stderr, "Usage: server [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Configuration is read from environment variables / .env (see .env.example).\n\nFlags:\n")
		flag.PrintDefaults()
	}
	healthFlag := flag.Bool("healthcheck", false, "probe /health and exit (for container HEALTHCHECK)")
	flag.Parse()
	if *healthFlag {
		os.Exit(healthcheck())
	}

	if err := run(); err != nil {
		slog.Error("application failed to start", "error", err)
		os.Exit(1)
	}
}

// healthcheck probes the local /health endpoint; returns 0 if status is ok.
func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%s/health", port)
	//nolint:gosec // G704: fixed loopback probe; port comes from our own env, not user input
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: loopback probe, see above
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"ok"`) {
		return 0
	}
	fmt.Fprintln(os.Stderr, "healthcheck failed: status", resp.StatusCode)
	return 1
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("starting liteend-go",
		"version", version.AppVersion, "commit", version.Commit, "env", cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Server.Run(ctx)
}
