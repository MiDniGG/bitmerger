package dedupe

import (
	"sort"
	"strings"
	"time"
)

// HistoryLimit is the number of entries Bitwarden clients keep in the password history.
const HistoryLimit = 5

func revisionOr(it Item) string {
	if r := it.RevisionDate(); r != "" {
		return r
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// PasswordChain returns the old passwords to set one after another (oldest
// first) before the target password, so that the Bitwarden CLI records them.
//
// The CLI ignores a passwordHistory sent with an edit. It only prepends the
// previous password when the password changes. Setting each old password in
// turn and the target last therefore fills the history.
//
// required are the current passwords of the group that must end up in the
// history; old history entries of the duplicates are best effort.
func PasswordChain(keeper Item, others []Item, target string) (chain []string, required map[string]bool) {
	known := map[string]bool{}
	for _, h := range keeper.History() {
		known[h.Password] = true
	}
	type dated struct{ date, pw string }
	var all []dated
	for _, it := range append([]Item{keeper}, others...) {
		if pw := it.Password(); pw != "" {
			all = append(all, dated{revisionOr(it), pw})
		}
	}
	for _, it := range others {
		for _, h := range it.History() {
			d := h.LastUsedDate
			if d == "" {
				d = revisionOr(it)
			}
			all = append(all, dated{d, h.Password})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].date < all[j].date })
	// The keeper's current password is recorded by the first change anyway.
	seen := map[string]bool{target: true, "": true, keeper.Password(): true}
	for _, d := range all {
		if !seen[d.pw] && !known[d.pw] {
			seen[d.pw] = true
			chain = append(chain, d.pw)
		}
	}
	required = map[string]bool{}
	for _, it := range append([]Item{keeper}, others...) {
		if pw := it.Password(); pw != "" && pw != target {
			required[pw] = true
		}
	}
	return chain, required
}

// SimulateHistory returns the history the CLI leaves after setting chain and target in turn.
func SimulateHistory(keeper Item, chain []string, target string) []string {
	var history []string
	for _, h := range keeper.History() {
		history = append(history, h.Password)
	}
	current := keeper.Password()
	for _, pw := range append(append([]string{}, chain...), target) {
		if current != "" && current != pw {
			history = append([]string{current}, history...)
			if len(history) > HistoryLimit {
				history = history[:HistoryLimit]
			}
		}
		current = pw
	}
	return history
}

func normURI(u string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/"))
}

// Merge builds the new content of the keeper without touching the password.
// It returns the item and change codes for the user (e.g. "totp_field").
func Merge(keeper Item, others []Item) (Item, []string) {
	out := keeper.Clone()
	var notes []string
	everyone := append([]Item{keeper}, others...)

	if keeper.Type() == TypeLogin {
		lg, _ := out["login"].(map[string]any)
		if lg == nil {
			lg = map[string]any{}
			out["login"] = lg
		}
		uris := list(lg["uris"])
		known := map[string]bool{}
		for _, u := range uris {
			if m, ok := u.(map[string]any); ok {
				known[normURI(str(m["uri"]))] = true
			}
		}
		for _, o := range others {
			for _, u := range list(o.Clone().Login()["uris"]) {
				m, ok := u.(map[string]any)
				if !ok {
					continue
				}
				k := normURI(str(m["uri"]))
				if k != "" && !known[k] {
					uris = append(uris, m)
					known[k] = true
				}
			}
		}
		lg["uris"] = uris

		if str(lg["username"]) == "" {
			for _, o := range others {
				if o.Username() != "" {
					lg["username"] = o.Username()
					break
				}
			}
		}

		var totps []string
		seenTOTP := map[string]bool{}
		for _, it := range everyone {
			if t := it.TOTP(); t != "" && !seenTOTP[t] {
				seenTOTP[t] = true
				totps = append(totps, t)
			}
		}
		if str(lg["totp"]) == "" && len(totps) > 0 {
			lg["totp"] = totps[0]
		}
		for _, t := range totps {
			if t != str(lg["totp"]) {
				out["fields"] = append(list(out["fields"]), map[string]any{
					"name": "TOTP (duplicate)", "value": t, "type": 1, "linkedId": nil})
				notes = append(notes, "totp_field")
			}
		}

		if pk, _ := lg["fido2Credentials"].([]any); len(pk) == 0 {
			for _, o := range others {
				if o.HasPasskey() {
					lg["fido2Credentials"] = o.Clone().Login()["fido2Credentials"]
					notes = append(notes, "passkey")
					break
				}
			}
		}
	}

	for _, o := range others {
		text := strings.TrimSpace(o.Notes())
		cur := str(out["notes"])
		if text != "" && !strings.Contains(cur, text) {
			sep := "\n\n--- " + o.Name() + " ---\n"
			out["notes"] = strings.TrimSpace(strings.TrimRight(cur, " \n") + sep + text)
		}
	}

	type fk struct{ name, value string }
	knownFields := map[fk]bool{}
	for _, f := range out.Fields() {
		knownFields[fk{str(f["name"]), str(f["value"])}] = true
	}
	for _, o := range others {
		for _, f := range o.Clone().Fields() {
			k := fk{str(f["name"]), str(f["value"])}
			if !knownFields[k] {
				out["fields"] = append(list(out["fields"]), f)
				knownFields[k] = true
			}
		}
	}

	for _, it := range everyone {
		if it.Favorite() {
			out["favorite"] = true
		}
	}
	if out.FolderID() == "" {
		for _, o := range others {
			if o.FolderID() != "" && o.OrgID() == "" {
				out["folderId"] = o.FolderID()
				break
			}
		}
	}
	return out, notes
}
