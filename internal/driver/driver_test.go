package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBuildSpec(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		labels   map[string]string
		defVault string
		want     Spec
		wantErr  bool
	}{
		{name: "full ref", labels: map[string]string{"ref": "op://Prod/db/password"},
			want: Spec{Reference: "op://Prod/db/password"}},
		{name: "ref with section", labels: map[string]string{"ref": "op://Prod/db/admin/password"},
			want: Spec{Reference: "op://Prod/db/admin/password"}},
		{name: "ref must be op://", labels: map[string]string{"ref": "Prod/db/password"}, wantErr: true},
		{name: "parts default field", labels: map[string]string{"vault": "Prod", "item": "db"},
			want: Spec{Reference: "op://Prod/db/password"}},
		{name: "parts with section and field", labels: map[string]string{"vault": "Prod", "item": "db", "section": "admin", "field": "username"},
			want: Spec{Reference: "op://Prod/db/admin/username"}},
		{name: "item falls back to secret name, vault to default", secret: "db_password", labels: nil, defVault: "Swarm",
			want: Spec{Reference: "op://Swarm/db_password/password"}},
		{name: "no vault anywhere", secret: "x", labels: map[string]string{"item": "db"}, wantErr: true},
		{name: "slash in part", labels: map[string]string{"vault": "Prod", "item": "a/b"}, wantErr: true},
		{name: "question mark in part", labels: map[string]string{"vault": "Prod", "item": "a?b"}, wantErr: true},
		{name: "attribute", labels: map[string]string{"ref": "op://Prod/github/one-time password", "attribute": "otp"},
			want: Spec{Reference: "op://Prod/github/one-time password?attribute=otp"}},
		{name: "attribute plus query", labels: map[string]string{"ref": "op://Prod/x/y?attribute=otp", "attribute": "otp"}, wantErr: true},
		{name: "base64 + no reuse", labels: map[string]string{"ref": "op://Prod/tls/key", "encoding": "BASE64", "reuse": "false"},
			want: Spec{Reference: "op://Prod/tls/key", Base64: true, DoNotReuse: true}},
		{name: "bad encoding", labels: map[string]string{"ref": "op://a/b/c", "encoding": "hex"}, wantErr: true},
		{name: "bad reuse", labels: map[string]string{"ref": "op://a/b/c", "reuse": "maybe"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildSpec(tt.secret, tt.labels, tt.defVault)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

type fakeResolver struct {
	values map[string]string
	calls  int
}

func (f *fakeResolver) Resolve(_ context.Context, ref string) (string, error) {
	f.calls++
	v, ok := f.values[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func post(t *testing.T, h http.Handler, path string, body any) (*httptest.ResponseRecorder, Response) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b)))
	var res Response
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func TestActivate(t *testing.T) {
	d := New(&fakeResolver{}, Options{}, quietLogger())
	rec, _ := post(t, d.Handler(), "/Plugin.Activate", nil)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"secretprovider"`)) {
		t.Fatalf("unexpected activate response %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentType {
		t.Fatalf("content type %q", ct)
	}
}

func TestGetSecret(t *testing.T) {
	f := &fakeResolver{values: map[string]string{
		"op://Prod/db/password": "s3cr3t",
		"op://Prod/tls/key":     "aGVsbG8K",
	}}
	d := New(f, Options{}, quietLogger())
	h := d.Handler()

	rec, res := post(t, h, "/SecretProvider.GetSecret", Request{SecretName: "db", SecretLabels: map[string]string{"ref": "op://Prod/db/password"}})
	if rec.Code != 200 || string(res.Value) != "s3cr3t" || res.Err != "" {
		t.Fatalf("got %d %+v", rec.Code, res)
	}

	_, res = post(t, h, "/SecretProvider.GetSecret", Request{SecretName: "tls", SecretLabels: map[string]string{"ref": "op://Prod/tls/key", "encoding": "base64"}})
	if string(res.Value) != "hello\n" {
		t.Fatalf("base64 decode: %q", res.Value)
	}

	rec, res = post(t, h, "/SecretProvider.GetSecret", Request{SecretName: "nope", SecretLabels: map[string]string{"ref": "op://Prod/nope/password"}})
	if rec.Code != 500 || res.Err == "" || len(res.Value) != 0 {
		t.Fatalf("expected error, got %d %+v", rec.Code, res)
	}
}

func TestCache(t *testing.T) {
	f := &fakeResolver{values: map[string]string{"op://V/i/password": "v"}}
	d := New(f, Options{CacheTTL: time.Minute}, quietLogger())
	now := time.Unix(0, 0)
	d.now = func() time.Time { return now }
	req := Request{SecretName: "i", SecretLabels: map[string]string{"vault": "V"}}

	d.Get(context.Background(), req)
	d.Get(context.Background(), req)
	if f.calls != 1 {
		t.Fatalf("expected 1 call with cache, got %d", f.calls)
	}
	now = now.Add(2 * time.Minute)
	d.Get(context.Background(), req)
	if f.calls != 2 {
		t.Fatalf("expected refresh after TTL, got %d", f.calls)
	}

	// reuse=false bypasses the cache and is passed through to Docker.
	req.SecretLabels["reuse"] = "false"
	res := d.Get(context.Background(), req)
	if f.calls != 3 || !res.DoNotReuse {
		t.Fatalf("expected uncached DoNotReuse call, calls=%d res=%+v", f.calls, res)
	}
}

// blockingResolver holds every caller until proceed is closed, so a burst of
// concurrent requests reliably overlaps instead of racing to complete first.
type blockingResolver struct {
	mu      sync.Mutex
	calls   int
	proceed chan struct{}
}

func (b *blockingResolver) Resolve(_ context.Context, ref string) (string, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-b.proceed
	return "v-" + ref, nil
}

func TestConcurrentGetCoalesces(t *testing.T) {
	r := &blockingResolver{proceed: make(chan struct{})}
	d := New(r, Options{}, quietLogger())
	req := Request{SecretName: "i", SecretLabels: map[string]string{"ref": "op://V/i/password"}}

	const n = 20
	var wg sync.WaitGroup
	results := make([]Response, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = d.Get(context.Background(), req)
		}()
	}
	time.Sleep(50 * time.Millisecond) // let all goroutines reach the shared wait point
	close(r.proceed)
	wg.Wait()

	r.mu.Lock()
	calls := r.calls
	r.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected concurrent identical requests to share one resolver call, got %d", calls)
	}
	for i, res := range results {
		if res.Err != "" || string(res.Value) != "v-op://V/i/password" {
			t.Fatalf("goroutine %d: unexpected result %+v", i, res)
		}
	}
}

type versionedResolver struct {
	fakeResolver
	versions map[string]string
}

func (v *versionedResolver) Versions(_ context.Context, refs []string) (map[string]string, map[string]error) {
	res, errs := map[string]string{}, map[string]error{}
	for _, ref := range refs {
		if ver, ok := v.versions[ref]; ok {
			res[ref] = ver
		} else {
			errs[ref] = errors.New("not found")
		}
	}
	return res, errs
}

func TestVersions(t *testing.T) {
	r := &versionedResolver{
		fakeResolver: fakeResolver{values: map[string]string{"op://V/db/password": "old"}},
		versions:     map[string]string{"op://V/db/password": "i1@1"},
	}
	d := New(r, Options{CacheTTL: time.Minute}, quietLogger())
	h := d.Handler()
	req := VersionsRequest{Secrets: []VersionsSecret{
		{Name: "app_db", Labels: map[string]string{"ref": "op://V/db/password"}},
		{Name: "app_missing", Labels: map[string]string{"ref": "op://V/missing/password"}},
		{Name: "app_bad", Labels: map[string]string{"ref": "nope"}},
	}}

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(req)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/SecretProvider.Versions", bytes.NewReader(b)))
	var res VersionsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res.Versions["app_db"] != "i1@1" || res.CacheTTLSeconds != 60 {
		t.Fatalf("got %d %+v", rec.Code, res)
	}
	if res.Errors["app_missing"] == "" || res.Errors["app_bad"] == "" || len(res.Versions) != 1 {
		t.Fatalf("expected per-secret errors, got %+v", res)
	}

	// A cached value survives an unchanged version...
	get := Request{SecretName: "app_db", SecretLabels: map[string]string{"ref": "op://V/db/password"}}
	d.Get(context.Background(), get)
	d.Versions(context.Background(), req)
	r.values["op://V/db/password"] = "new"
	if got := d.Get(context.Background(), get); string(got.Value) != "old" {
		t.Fatalf("expected the cached value while the version is unchanged, got %q", got.Value)
	}
	// ...and is dropped once the item changed.
	r.versions["op://V/db/password"] = "i1@2"
	d.Versions(context.Background(), req)
	if got := d.Get(context.Background(), get); string(got.Value) != "new" {
		t.Fatalf("expected a fresh value after the version changed, got %q", got.Value)
	}
}

func TestVersionsUnsupported(t *testing.T) {
	d := New(&fakeResolver{}, Options{}, quietLogger())
	if res := d.Versions(context.Background(), VersionsRequest{}); res.Err == "" {
		t.Fatal("expected an error for a resolver without versions")
	}
}
