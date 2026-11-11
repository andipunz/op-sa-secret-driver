package onepassword

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	op "github.com/1password/onepassword-sdk-go"
)

func TestVersions(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	c := &fakeClient{
		vaults: []op.VaultOverview{{ID: "v1", Title: "Companion Cluster"}, {ID: "v2", Title: "Other"}},
		items: map[string][]op.ItemOverview{
			"v1": {
				{ID: "i1", Title: "db_password", UpdatedAt: t1},
				{ID: "i2", Title: "api key", UpdatedAt: t2},
				{ID: "i3", Title: "dup", UpdatedAt: t1},
				{ID: "i4", Title: "dup", UpdatedAt: t1},
			},
		},
	}
	r, _ := newTestResolver("ops_token", c)

	refs := []string{
		"op://Companion Cluster/db_password/password",
		"op://Companion Cluster/db_password/username", // same item, no extra request
		"op://v1/api key/section/field?attribute=otp", // vault by ID, section and query
		"op://Companion Cluster/i3/password",          // item by ID despite a duplicate title
		"op://Companion Cluster/dup/password",
		"op://Companion Cluster/missing/password",
		"op://Nope/x/password",
		"op://only-vault",
	}
	versions, errs := r.Versions(context.Background(), refs)

	want := map[string]string{
		refs[0]: "i1@2026-11-01T10:00:00Z",
		refs[1]: "i1@2026-11-01T10:00:00Z",
		refs[2]: "i2@2026-11-02T10:00:00Z",
		refs[3]: "i3@2026-11-01T10:00:00Z",
	}
	for ref, v := range want {
		if versions[ref] != v {
			t.Errorf("%s: got %q, want %q (err %v)", ref, versions[ref], v, errs[ref])
		}
	}
	for _, ref := range refs[4:] {
		if errs[ref] == nil || versions[ref] != "" {
			t.Errorf("%s: expected an error, got version %q", ref, versions[ref])
		}
	}
	if !slices.Equal(c.listCalls, []string{"vaults", "v1"}) {
		t.Errorf("expected one vault list and one item list, got %v", c.listCalls)
	}
}

func TestVersionsVaultListError(t *testing.T) {
	failing := &fakeClient{vaultsErr: errors.New("session expired")}
	recovered := &fakeClient{vaultsErr: errors.New("still broken")}
	r, newClientCalls := newTestResolver("ops_token", failing, recovered)

	_, errs := r.Versions(context.Background(), []string{"op://V/i/password", "op://V/j/password"})
	if len(errs) != 2 {
		t.Fatalf("expected the error on every reference, got %v", errs)
	}
	if len(*newClientCalls) != 2 {
		t.Fatalf("expected one retry with a fresh client, got %d creations", len(*newClientCalls))
	}
}
