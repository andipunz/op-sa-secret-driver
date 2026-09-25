// Package driver implements the Docker secretprovider plugin protocol
// on top of a 1Password secret resolver.
package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Resolver resolves op:// secret references to their values.
type Resolver interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

// Request is the payload Docker sends to /SecretProvider.GetSecret.
type Request struct {
	SecretName      string            `json:",omitempty"`
	SecretLabels    map[string]string `json:",omitempty"`
	ServiceHostname string            `json:",omitempty"`
	ServiceName     string            `json:",omitempty"`
	ServiceID       string            `json:",omitempty"`
	ServiceLabels   map[string]string `json:",omitempty"`
	TaskID          string            `json:",omitempty"`
	TaskName        string            `json:",omitempty"`
	TaskImage       string            `json:",omitempty"`
}

// Response is what the plugin returns to Docker.
type Response struct {
	Value      []byte `json:",omitempty"`
	Err        string `json:",omitempty"`
	DoNotReuse bool   `json:",omitempty"`
}

// Options configures a Driver.
type Options struct {
	DefaultVault string
	CacheTTL     time.Duration // 0 disables caching
	Timeout      time.Duration // per-request timeout towards 1Password
}

// Driver fulfils secret requests.
type Driver struct {
	resolver Resolver
	opts     Options
	log      *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry
	now   func() time.Time
}

type cacheEntry struct {
	value   string
	expires time.Time
}

// New creates a Driver.
func New(r Resolver, opts Options, log *slog.Logger) *Driver {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	return &Driver{
		resolver: r,
		opts:     opts,
		log:      log,
		cache:    map[string]cacheEntry{},
		now:      time.Now,
	}
}

// Get resolves one Docker secret request.
func (d *Driver) Get(ctx context.Context, req Request) Response {
	log := d.log.With("secret", req.SecretName, "service", req.ServiceName, "task", req.TaskName)

	spec, err := BuildSpec(req.SecretName, req.SecretLabels, d.opts.DefaultVault)
	if err != nil {
		log.Warn("invalid secret labels", "err", err)
		return Response{Err: err.Error()}
	}
	log = log.With("ref", spec.Reference)

	value, cached, err := d.lookup(ctx, spec)
	if err != nil {
		log.Error("resolving secret failed", "err", err)
		return Response{Err: "1password: " + err.Error()}
	}

	out := []byte(value)
	if spec.Base64 {
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			log.Error("value is not valid base64", "err", err)
			return Response{Err: "value of " + spec.Reference + " is not valid base64"}
		}
		out = dec
	}

	log.Info("secret resolved", "cached", cached, "bytes", len(out), "doNotReuse", spec.DoNotReuse)
	return Response{Value: out, DoNotReuse: spec.DoNotReuse}
}

func (d *Driver) lookup(ctx context.Context, spec Spec) (string, bool, error) {
	// Never serve per-task secrets from cache: the caller explicitly asked for a fresh value.
	useCache := d.opts.CacheTTL > 0 && !spec.DoNotReuse

	if useCache {
		d.mu.Lock()
		e, ok := d.cache[spec.Reference]
		d.mu.Unlock()
		if ok && d.now().Before(e.expires) {
			return e.value, true, nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()
	v, err := d.resolver.Resolve(ctx, spec.Reference)
	if err != nil {
		return "", false, err
	}

	if useCache {
		d.mu.Lock()
		d.cache[spec.Reference] = cacheEntry{value: v, expires: d.now().Add(d.opts.CacheTTL)}
		d.mu.Unlock()
	}
	return v, false, nil
}

// --- Docker plugin HTTP protocol -------------------------------------------

const contentType = "application/vnd.docker.plugins.v1.2+json"

// Handler returns the HTTP handler Docker talks to over the plugin socket.
func (d *Driver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /Plugin.Activate", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string][]string{"Implements": {"secretprovider"}})
	})
	mux.HandleFunc("POST /SecretProvider.GetSecret", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, Response{Err: "decoding request: " + err.Error()})
			return
		}
		res := d.Get(r.Context(), req)
		status := http.StatusOK
		if res.Err != "" {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, res)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		slog.Default().Warn("writing response failed", "err", err)
	}
}
