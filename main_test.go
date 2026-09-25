package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOrTrims(t *testing.T) {
	t.Setenv("PLUGIN_SOCKET", "  /custom/sock  ")
	if got := envOr("PLUGIN_SOCKET", "/default"); got != "/custom/sock" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("PLUGIN_SOCKET", "   ")
	if got := envOr("PLUGIN_SOCKET", "/default"); got != "/default" {
		t.Fatalf("blank value should fall back to default, got %q", got)
	}
}

func TestNewTokenFuncFromEnv(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN_FILE", "")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "  ops_env_token  ")

	fn, err := newTokenFunc()
	if err != nil {
		t.Fatalf("newTokenFunc: %v", err)
	}
	tok, err := fn()
	if err != nil || tok != "ops_env_token" {
		t.Fatalf("got %q, %v", tok, err)
	}
}

func TestNewTokenFuncFromFileRereadsOnRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("ops_first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN_FILE", path)

	fn, err := newTokenFunc()
	if err != nil {
		t.Fatalf("newTokenFunc: %v", err)
	}
	if tok, err := fn(); err != nil || tok != "ops_first" {
		t.Fatalf("got %q, %v", tok, err)
	}

	// Simulate rotation: the file is rewritten in place without a restart.
	if err := os.WriteFile(path, []byte("ops_rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := fn(); err != nil || tok != "ops_rotated" {
		t.Fatalf("expected reread to see the rotated token, got %q, %v", tok, err)
	}
}

func TestNewTokenFuncFromFileFailsFastWhenMissing(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	if _, err := newTokenFunc(); err == nil {
		t.Fatal("expected an error for an unreadable token file at startup")
	}
}
