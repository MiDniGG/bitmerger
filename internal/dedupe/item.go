// Package dedupe finds duplicate vault items and merges them.
//
// Items are kept as generic JSON maps so that fields this package does not
// know about (SSH keys, future additions) survive an edit unchanged.
package dedupe

import (
	"encoding/json"
	"strings"
)

// Item is a vault item exactly as `bw get item` returns it.
type Item map[string]any

// Item types as used by Bitwarden.
const (
	TypeLogin = 1
)

// Clone returns a deep copy.
func (it Item) Clone() Item {
	b, _ := json.Marshal(it)
	var out Item
	_ = json.Unmarshal(b, &out)
	return out
}

func (it Item) ID() string           { return str(it["id"]) }
func (it Item) Name() string         { return str(it["name"]) }
func (it Item) Notes() string        { return str(it["notes"]) }
func (it Item) OrgID() string        { return str(it["organizationId"]) }
func (it Item) FolderID() string     { return str(it["folderId"]) }
func (it Item) RevisionDate() string { return str(it["revisionDate"]) }
func (it Item) Deleted() bool        { return str(it["deletedDate"]) != "" }
func (it Item) Favorite() bool       { b, _ := it["favorite"].(bool); return b }

func (it Item) Type() int {
	f, _ := it["type"].(float64)
	if i, ok := it["type"].(int); ok {
		return i
	}
	return int(f)
}

// Login returns the login sub-object, or nil.
func (it Item) Login() map[string]any {
	m, _ := it["login"].(map[string]any)
	return m
}

func (it Item) Username() string {
	return str(it.Login()["username"])
}

func (it Item) Password() string {
	return str(it.Login()["password"])
}

func (it Item) TOTP() string {
	return str(it.Login()["totp"])
}

func (it Item) HasPasskey() bool {
	l, _ := it.Login()["fido2Credentials"].([]any)
	return len(l) > 0
}

// URIs returns the non-empty URIs of a login.
func (it Item) URIs() []string {
	var out []string
	for _, u := range list(it.Login()["uris"]) {
		if m, ok := u.(map[string]any); ok {
			if s := strings.TrimSpace(str(m["uri"])); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (it Item) CollectionIDs() []string {
	var out []string
	for _, c := range list(it["collectionIds"]) {
		out = append(out, str(c))
	}
	return out
}

// HistoryEntry is one entry of the password history.
type HistoryEntry struct {
	Password     string
	LastUsedDate string
}

func (it Item) History() []HistoryEntry {
	var out []HistoryEntry
	for _, h := range list(it["passwordHistory"]) {
		if m, ok := h.(map[string]any); ok && str(m["password"]) != "" {
			out = append(out, HistoryEntry{Password: str(m["password"]), LastUsedDate: str(m["lastUsedDate"])})
		}
	}
	return out
}

func (it Item) Fields() []map[string]any {
	var out []map[string]any
	for _, f := range list(it["fields"]) {
		if m, ok := f.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (it Item) Attachments() []map[string]any {
	var out []map[string]any
	for _, a := range list(it["attachments"]) {
		if m, ok := a.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}
