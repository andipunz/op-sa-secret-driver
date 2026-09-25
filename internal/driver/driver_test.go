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
