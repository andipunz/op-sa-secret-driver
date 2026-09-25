package driver

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Secret label keys understood by the driver.
//
// Either set LabelRef to a full secret reference (op://vault/item[/section]/field),
// or compose it from LabelVault / LabelItem / LabelSection / LabelField.
const (
	LabelRef       = "ref"       // full op:// secret reference; wins over the individual labels
	LabelVault     = "vault"     // vault name or ID (falls back to OP_DEFAULT_VAULT)
	LabelItem      = "item"      // item name or ID (falls back to the Docker secret name)
	LabelSection   = "section"   // optional section name or ID
	LabelField     = "field"     // field name or ID (defaults to "password")
	LabelAttribute = "attribute" // optional reference attribute, e.g. "otp" or "type"
	LabelEncoding  = "encoding"  // "base64" decodes the stored value before handing it to Docker
	LabelReuse     = "reuse"     // "false" asks Swarm to call the driver again for every task
)

const defaultField = "password"

// Spec is everything the driver needs to fulfil one secret request.
type Spec struct {
	Reference  string
	Base64     bool
	DoNotReuse bool
}

// BuildSpec turns the labels of a Docker secret into a resolvable Spec.
func BuildSpec(secretName string, labels map[string]string, defaultVault string) (Spec, error) {
	var s Spec

	get := func(k string) string { return strings.TrimSpace(labels[k]) }

	if ref := get(LabelRef); ref != "" {
		if !strings.HasPrefix(ref, "op://") {
			return s, fmt.Errorf("label %q must start with op:// (got %q)", LabelRef, ref)
		}
		s.Reference = ref
	} else {
		vault := firstNonEmpty(get(LabelVault), defaultVault)
		if vault == "" {
			return s, fmt.Errorf("secret %q has no %q label, no %q label and OP_DEFAULT_VAULT is not set", secretName, LabelRef, LabelVault)
		}
		item := firstNonEmpty(get(LabelItem), secretName)
		if item == "" {
			return s, errors.New("cannot determine item: neither an item label nor a secret name was given")
		}
		field := firstNonEmpty(get(LabelField), defaultField)

		parts := []string{vault, item}
		if sec := get(LabelSection); sec != "" {
			parts = append(parts, sec)
		}
		parts = append(parts, field)
		for _, p := range parts {
			if strings.Contains(p, "/") {
				return s, fmt.Errorf("vault/item/section/field names containing '/' must be addressed by ID or via the %q label (got %q)", LabelRef, p)
			}
		}
		s.Reference = "op://" + strings.Join(parts, "/")
	}

	if attr := get(LabelAttribute); attr != "" {
		if strings.Contains(s.Reference, "?") {
			return s, fmt.Errorf("use either an %q label or a query in the reference, not both", LabelAttribute)
		}
		s.Reference += "?attribute=" + url.QueryEscape(attr)
	}

	switch enc := strings.ToLower(get(LabelEncoding)); enc {
	case "", "raw", "plain", "text":
	case "base64":
		s.Base64 = true
	default:
		return s, fmt.Errorf("unsupported %q label %q (use \"base64\" or leave it empty)", LabelEncoding, enc)
	}

	if v := get(LabelReuse); v != "" {
		reuse, err := strconv.ParseBool(v)
		if err != nil {
			return s, fmt.Errorf("label %q must be true or false (got %q)", LabelReuse, v)
		}
		s.DoNotReuse = !reuse
	}

	return s, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
