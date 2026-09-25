package onepassword

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	op "github.com/1password/onepassword-sdk-go"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeClient is a fake secretsResolver whose behavior for a given call is
// taken off the front of results; it records every reference it was asked
// to resolve.
type fakeClient struct {
	mu      sync.Mutex
	results []fakeResult
	calls   []string
}

type fakeResult struct {
	value string
	err   error
}

func (f *fakeClient) Resolve(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ref)
	if len(f.results) == 0 {
		return "", errors.New("fakeClient: no more results queued")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.value, r.err
}

// newTestResolver builds a Resolver whose token and SDK client are both
// faked, so tests never touch the real 1Password SDK.
func newTestResolver(token string, clients ...*fakeClient) (*Resolver, *[]string) {
	var newClientCalls []string
	i := 0
	r := NewResolver(func() (string, error) { return token, nil }, "test", quietLogger())
	r.validate = func(context.Context, string) error { return nil }
	r.newClient = func(_ context.Context, tok, _ string) (secretsResolver, error) {
		newClientCalls = append(newClientCalls, tok)
		if i >= len(clients) {
			return nil, errors.New("newTestResolver: no more clients queued")
		}
		c := clients[i]
		i++
		return c, nil
	}
	return r, &newClientCalls
}

func TestResolveSuccessCachesClient(t *testing.T) {
	c := &fakeClient{results: []fakeResult{{value: "s3cr3t"}, {value: "s3cr3t"}}}
	r, newClientCalls := newTestResolver("ops_token", c)

	for range 2 {
		v, err := r.Resolve(context.Background(), "op://V/i/password")
		if err != nil || v != "s3cr3t" {
			t.Fatalf("got %q, %v", v, err)
		}
	}
	if len(*newClientCalls) != 1 {
		t.Fatalf("expected the client to be created once and reused, got %d creations", len(*newClientCalls))
	}
}

func TestResolveInvalidReferenceSkipsClient(t *testing.T) {
	r, newClientCalls := newTestResolver("ops_token")
	r.validate = func(context.Context, string) error { return errors.New("bad syntax") }

	_, err := r.Resolve(context.Background(), "not-a-reference")
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(*newClientCalls) != 0 {
		t.Fatalf("expected no client to be created for an invalid reference, got %d", len(*newClientCalls))
	}
}

func TestResolveNoToken(t *testing.T) {
	r, _ := newTestResolver("")
	if _, err := r.Resolve(context.Background(), "op://V/i/password"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("expected ErrNoToken, got %v", err)
	}
}

func TestResolveTokenFuncError(t *testing.T) {
	wantErr := errors.New("reading token file")
	r := NewResolver(func() (string, error) { return "", wantErr }, "test", quietLogger())
	r.validate = func(context.Context, string) error { return nil }

	if _, err := r.Resolve(context.Background(), "op://V/i/password"); !errors.Is(err, wantErr) {
		t.Fatalf("expected token error to propagate, got %v", err)
	}
}

func TestResolveRetriesWithFreshClientAndRereadsToken(t *testing.T) {
	failing := &fakeClient{results: []fakeResult{{err: errors.New("session expired")}}}
	recovered := &fakeClient{results: []fakeResult{{value: "s3cr3t"}}}

	tokens := []string{"ops_old", "ops_rotated"}
	i := 0
	r := NewResolver(func() (string, error) {
		tok := tokens[i]
		if i < len(tokens)-1 {
			i++
		}
		return tok, nil
	}, "test", quietLogger())
	r.validate = func(context.Context, string) error { return nil }

	clients := []*fakeClient{failing, recovered}
	call := 0
	var seenTokens []string
	r.newClient = func(_ context.Context, tok, _ string) (secretsResolver, error) {
		seenTokens = append(seenTokens, tok)
		c := clients[call]
		call++
		return c, nil
	}

	v, err := r.Resolve(context.Background(), "op://V/i/password")
	if err != nil || v != "s3cr3t" {
		t.Fatalf("got %q, %v", v, err)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "ops_old" || seenTokens[1] != "ops_rotated" {
		t.Fatalf("expected retry to reread the (rotated) token, got %v", seenTokens)
	}
}

func TestResolveDoesNotRetryOnRateLimit(t *testing.T) {
	c := &fakeClient{results: []fakeResult{{err: &op.RateLimitExceededError{}}}}
	r, newClientCalls := newTestResolver("ops_token", c)

	var rl *op.RateLimitExceededError
	_, err := r.Resolve(context.Background(), "op://V/i/password")
	if !errors.As(err, &rl) {
		t.Fatalf("expected a RateLimitExceededError, got %v", err)
	}
	if len(*newClientCalls) != 1 {
		t.Fatalf("expected no retry (one client creation total), got %d", len(*newClientCalls))
	}
}

func TestResolveDoesNotRetryWhenContextCanceled(t *testing.T) {
	c := &fakeClient{results: []fakeResult{{err: errors.New("context canceled")}}}
	r, newClientCalls := newTestResolver("ops_token", c)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Resolve(ctx, "op://V/i/password"); err == nil {
		t.Fatal("expected an error")
	}
	if len(*newClientCalls) != 1 {
		t.Fatalf("expected no retry once the caller's context is canceled, got %d", len(*newClientCalls))
	}
}
