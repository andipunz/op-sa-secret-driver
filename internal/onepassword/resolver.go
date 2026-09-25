// Package onepassword wraps the official 1Password Go SDK, authenticated
// with a Service Account token (no 1Password Connect server involved).
package onepassword

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	op "github.com/1password/onepassword-sdk-go"
)

const integrationName = "Docker Swarm Secret Driver"

// Resolver resolves op:// references through a lazily created SDK client.
type Resolver struct {
	// tokenFunc returns the current service account token. It is called
	// again each time the client is (re)created, so a token backed by a
	// file (OP_SERVICE_ACCOUNT_TOKEN_FILE) picks up rotation without a
	// process restart.
	tokenFunc func() (string, error)
	version   string
	log       *slog.Logger

	mu     sync.Mutex
	client *op.Client
}

// NewResolver creates a resolver. The SDK client is created on first use, so
// the plugin starts (and can be configured) even before a token is set.
func NewResolver(tokenFunc func() (string, error), version string, log *slog.Logger) *Resolver {
	return &Resolver{tokenFunc: tokenFunc, version: version, log: log}
}

// ErrNoToken is returned when no service account token is configured.
var ErrNoToken = errors.New("no service account token configured: run `docker plugin set <plugin> OP_SERVICE_ACCOUNT_TOKEN=ops_...`")

func (r *Resolver) getClient(ctx context.Context) (*op.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	token, err := r.tokenFunc()
	if err != nil {
		return nil, fmt.Errorf("reading service account token: %w", err)
	}
	if token == "" {
		return nil, ErrNoToken
	}
	c, err := op.NewClient(ctx,
		op.WithServiceAccountToken(token),
		op.WithIntegrationInfo(integrationName, r.version),
	)
	if err != nil {
		return nil, fmt.Errorf("authenticating service account: %w", err)
	}
	r.log.Info("1Password service account client ready")
	r.client = c
	return c, nil
}

func (r *Resolver) reset(bad *op.Client) {
	r.mu.Lock()
	if r.client == bad {
		r.client = nil
	}
	r.mu.Unlock()
}

// Warmup authenticates early so the first real secret request is fast
// (creating the client loads and compiles the SDK's WebAssembly core).
func (r *Resolver) Warmup(ctx context.Context) error {
	_, err := r.getClient(ctx)
	return err
}

// Resolve returns the value behind a secret reference.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	if err := op.Secrets.ValidateSecretReference(ctx, ref); err != nil {
		return "", fmt.Errorf("invalid secret reference %q: %w", ref, err)
	}

	c, err := r.getClient(ctx)
	if err != nil {
		return "", err
	}
	v, err := c.Secrets().Resolve(ctx, ref)
	if err == nil {
		return v, nil
	}

	var rl *op.RateLimitExceededError
	if errors.As(err, &rl) || ctx.Err() != nil {
		return "", err
	}

	// The session may have expired or the token been rotated behind our back:
	// drop the client and try once more with a fresh one.
	r.log.Warn("resolve failed, retrying with a fresh client", "ref", ref, "err", err)
	r.reset(c)
	c, err2 := r.getClient(ctx)
	if err2 != nil {
		return "", fmt.Errorf("%w (reconnect failed: %v)", err, err2)
	}
	return c.Secrets().Resolve(ctx, ref)
}
