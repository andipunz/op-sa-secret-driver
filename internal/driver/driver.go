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

// Versioner is optionally implemented by a Resolver: it reports an opaque
// version per reference that changes whenever the referenced value may have
// changed, without resolving the values themselves.
type Versioner interface {
	Versions(ctx context.Context, refs []string) (map[string]string, map[string]error)
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

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*inflightCall
	versions map[string]string // reference -> version last reported by Versions
	now      func() time.Time
}

type cacheEntry struct {
	value   string
	expires time.Time
}

// inflightCall coalesces concurrent resolves of the same reference into one
// call to the resolver, so a burst of tasks starting at once (e.g. a large
// service scaled up, or a stack redeploy) doesn't spend one API call and one
// rate-limit slot per task.
type inflightCall struct {
	done  chan struct{}
	value string
	err   error
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
		inflight: map[string]*inflightCall{},
		versions: map[string]string{},
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

	value, cached, err := d.lookup(spec)
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

func (d *Driver) lookup(spec Spec) (string, bool, error) {
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

	v, err := d.resolveShared(spec.Reference)
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

// resolveShared resolves reference through d.resolver, coalescing concurrent
// callers asking for the same reference into a single call. It is
// independent of any one caller's request context, since a slow or canceled
// caller must not abort a fetch that other callers are waiting on.
func (d *Driver) resolveShared(reference string) (string, error) {
	d.mu.Lock()
	if c, ok := d.inflight[reference]; ok {
		d.mu.Unlock()
		<-c.done
		return c.value, c.err
	}
	c := &inflightCall{done: make(chan struct{})}
	d.inflight[reference] = c
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), d.opts.Timeout)
	c.value, c.err = d.resolver.Resolve(ctx, reference)
	cancel()

	d.mu.Lock()
	delete(d.inflight, reference)
	d.mu.Unlock()
	close(c.done)

	return c.value, c.err
}

// VersionsSecret is one Docker secret in a VersionsRequest.
type VersionsSecret struct {
	Name   string
	Labels map[string]string `json:",omitempty"`
}

// VersionsRequest is the payload of /SecretProvider.Versions. This endpoint is
// not part of Docker's plugin protocol: Docker never calls it. Tools with
// access to the plugin socket (swarm-gitops) use it to notice secrets that
// changed in 1Password and restart the services using them.
type VersionsRequest struct {
	Secrets []VersionsSecret
}

// VersionsResponse maps secret names to an opaque version, or to an error.
// CacheTTLSeconds tells the caller how long a manager may still hand out an
// old value after a change (OP_CACHE_TTL), so it can wait that long before
// restarting anything.
type VersionsResponse struct {
	Versions        map[string]string `json:",omitempty"`
	Errors          map[string]string `json:",omitempty"`
	CacheTTLSeconds int               `json:",omitempty"`
	Err             string            `json:",omitempty"`
}

// Versions reports the version of every secret's 1Password item. When an
// item's version differs from the last one seen, its cached value is dropped,
// so tasks restarted because of the change get the new value right away (on
// this manager; others expire theirs after CacheTTLSeconds).
func (d *Driver) Versions(ctx context.Context, req VersionsRequest) VersionsResponse {
	res := VersionsResponse{Versions: map[string]string{}, Errors: map[string]string{}, CacheTTLSeconds: int(d.opts.CacheTTL / time.Second)}
	v, ok := d.resolver.(Versioner)
	if !ok {
		res.Err = "versions are not supported by this resolver"
		return res
	}
	refs := map[string]string{} // secret name -> reference
	var list []string
	for _, s := range req.Secrets {
		spec, err := BuildSpec(s.Name, s.Labels, d.opts.DefaultVault)
		if err != nil {
			res.Errors[s.Name] = err.Error()
			continue
		}
		refs[s.Name] = spec.Reference
		list = append(list, spec.Reference)
	}
	ctx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()
	versions, errs := v.Versions(ctx, list)

	d.mu.Lock()
	defer d.mu.Unlock()
	for name, ref := range refs {
		if err := errs[ref]; err != nil {
			res.Errors[name] = err.Error()
			continue
		}
		version := versions[ref]
		if prev, seen := d.versions[ref]; seen && prev != version {
			delete(d.cache, ref)
			d.log.Info("secret changed in 1Password", "ref", ref)
		}
		d.versions[ref] = version
		res.Versions[name] = version
	}
	return res
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
	mux.HandleFunc("POST /SecretProvider.Versions", func(w http.ResponseWriter, r *http.Request) {
		var req VersionsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, VersionsResponse{Err: "decoding request: " + err.Error()})
			return
		}
		res := d.Versions(r.Context(), req)
		status := http.StatusOK
		if res.Err != "" {
			status = http.StatusNotImplemented
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
