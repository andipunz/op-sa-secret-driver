package onepassword

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	op "github.com/1password/onepassword-sdk-go"
)

// Versions returns an opaque version per op:// reference: the item's ID and
// the time it was last changed. It costs one request for the vault list plus
// one item list per vault involved, however many references there are, so a
// caller can poll it to notice changed items without resolving (and spending
// one request on) every single secret. Errors are per reference.
func (r *Resolver) Versions(ctx context.Context, refs []string) (map[string]string, map[string]error) {
	versions, errs := map[string]string{}, map[string]error{}
	type location struct{ vault, item string }
	locs := map[string]location{}
	for _, ref := range refs {
		vault, item, err := splitReference(ref)
		if err != nil {
			errs[ref] = err
			continue
		}
		locs[ref] = location{vault, item}
	}
	if len(locs) == 0 {
		return versions, errs
	}

	c, vaults, err := r.listVaults(ctx)
	if err != nil {
		for ref := range locs {
			errs[ref] = err
		}
		return versions, errs
	}
	items := map[string][]op.ItemOverview{} // vault ID -> active items
	itemErrs := map[string]error{}
	for ref, l := range locs {
		v, err := match(vaults, l.vault, func(v op.VaultOverview) (string, string) { return v.ID, v.Title })
		if err != nil {
			errs[ref] = fmt.Errorf("vault %q: %w", l.vault, err)
			continue
		}
		list, listed := items[v.ID]
		if err := itemErrs[v.ID]; err != nil {
			errs[ref] = err
			continue
		}
		if !listed {
			if list, err = c.ListItems(ctx, v.ID); err != nil {
				itemErrs[v.ID] = fmt.Errorf("listing items of vault %q: %w", l.vault, err)
				errs[ref] = itemErrs[v.ID]
				continue
			}
			items[v.ID] = list
		}
		it, err := match(list, l.item, func(i op.ItemOverview) (string, string) { return i.ID, i.Title })
		if err != nil {
			errs[ref] = fmt.Errorf("item %q in vault %q: %w", l.item, l.vault, err)
			continue
		}
		versions[ref] = it.ID + "@" + it.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return versions, errs
}

// listVaults lists the vaults the service account can see, retrying once
// with a fresh client like Resolve does.
func (r *Resolver) listVaults(ctx context.Context) (sdkClient, []op.VaultOverview, error) {
	c, err := r.getClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	vaults, err := c.ListVaults(ctx)
	if err == nil {
		return c, vaults, nil
	}
	var rl *op.RateLimitExceededError
	if errors.As(err, &rl) || ctx.Err() != nil {
		return nil, nil, err
	}
	r.log.Warn("listing vaults failed, retrying with a fresh client", "err", err)
	r.reset(c)
	if c, err = r.getClient(ctx); err != nil {
		return nil, nil, err
	}
	vaults, err = c.ListVaults(ctx)
	return c, vaults, err
}

// splitReference returns the vault and item part of op://vault/item[/section]/field[?query].
func splitReference(ref string) (vault, item string, err error) {
	rest, ok := strings.CutPrefix(ref, "op://")
	if !ok {
		return "", "", fmt.Errorf("invalid secret reference %q: must start with op://", ref)
	}
	rest, _, _ = strings.Cut(rest, "?")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid secret reference %q: want op://vault/item[/section]/field", ref)
	}
	return parts[0], parts[1], nil
}

// match finds the one entry whose ID or title is name, the way secret
// references address vaults and items. An ID always wins over titles.
func match[T any](list []T, name string, key func(T) (id, title string)) (T, error) {
	var found []T
	for _, e := range list {
		id, title := key(e)
		if id == name {
			return e, nil
		}
		if title == name {
			found = append(found, e)
		}
	}
	var zero T
	switch len(found) {
	case 0:
		return zero, errors.New("not found")
	case 1:
		return found[0], nil
	default:
		return zero, fmt.Errorf("%d matches, address it by ID", len(found))
	}
}
