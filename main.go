// Command op-sa-secret-driver is a Docker Swarm secret driver plugin that reads
// secrets from 1Password using a Service Account token (no Connect server).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andipunz/op-sa-secret-driver/internal/driver"
	"github.com/andipunz/op-sa-secret-driver/internal/onepassword"
)

var version = "dev"

const defaultSocket = "/run/docker/plugins/op.sock"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := newLogger(os.Getenv("OP_LOG_LEVEL"))
	slog.SetDefault(log)

	token, err := readToken()
	if err != nil {
		return err
	}
	cacheTTL, err := durationEnv("OP_CACHE_TTL", 0)
	if err != nil {
		return err
	}
	timeout, err := durationEnv("OP_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	socket := envOr("PLUGIN_SOCKET", defaultSocket)

	resolver := onepassword.NewResolver(token, version, log)
	d := driver.New(resolver, driver.Options{
		DefaultVault: strings.TrimSpace(os.Getenv("OP_DEFAULT_VAULT")),
		CacheTTL:     cacheTTL,
		Timeout:      timeout,
	}, log)

	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}

	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		wctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := resolver.Warmup(wctx); err != nil {
			log.Warn("service account not ready yet", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Info("1Password service account secret driver listening",
		"version", version, "socket", socket, "cacheTTL", cacheTTL.String(), "tokenSet", token != "")
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// readToken reads the token from OP_SERVICE_ACCOUNT_TOKEN or the file named by
// OP_SERVICE_ACCOUNT_TOKEN_FILE. An empty token is allowed at startup; requests
// then fail with a clear message until the token is set.
func readToken() (string, error) {
	if f := strings.TrimSpace(os.Getenv("OP_SERVICE_ACCOUNT_TOKEN_FILE")); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("reading OP_SERVICE_ACCOUNT_TOKEN_FILE: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")), nil
}

func durationEnv(name string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: invalid duration %q (examples: 0, 30s, 5m)", name, v)
	}
	return d, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func newLogger(level string) *slog.Logger {
	l := slog.LevelInfo
	if level != "" {
		_ = l.UnmarshalText([]byte(level)) // DEBUG, INFO, WARN, ERROR; falls back to INFO
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
