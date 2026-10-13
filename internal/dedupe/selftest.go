package dedupe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// SelftestName prefixes the throwaway items, so leftovers are easy to find.
const SelftestName = "ZZ BitMerger selftest"

// CheckResult is one check of a selftest case.
type CheckResult struct {
	Check string `json:"check"` // code, translated by the UI
	OK    bool   `json:"ok"`
}

// CaseResult is one selftest case.
type CaseResult struct {
	Case   string        `json:"case"`
	Checks []CheckResult `json:"checks"`
	Error  string        `json:"error,omitempty"`
}

// OK reports whether all checks of the case passed.
func (c CaseResult) OK() bool {
	if c.Error != "" {
		return false
	}
	for _, r := range c.Checks {
		if !r.OK {
			return false
		}
	}
	return true
}

// Selftest merges throwaway personal items on the real server and checks the
// result. It covers both password cases; the items are deleted permanently.
// pause waits between creations so revision dates differ.
func Selftest(v Vault, pause time.Duration) []CaseResult {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	tag := hex.EncodeToString(b)
	return []CaseResult{
		selftestCase(v, "newest_password", "old-"+tag, []string{"new-" + tag}, true, pause),
		selftestCase(v, "keeper_password", "keep-"+tag, []string{"x-" + tag, "y-" + tag}, false, pause),
	}
}

func selftestItem(suffix, pw, notes string) Item {
	return Item{
		"type": TypeLogin, "name": SelftestName + " " + suffix, "notes": notes, "favorite": false,
		"organizationId": nil, "folderId": nil, "fields": []any{}, "reprompt": 0,
		"login": map[string]any{
			"username": "selftest@example.invalid", "password": pw, "totp": nil,
			"uris": []any{map[string]any{"match": nil, "uri": "https://selftest.example.invalid/" + suffix}},
		},
	}
}

func selftestCase(v Vault, name, keeperPW string, otherPWs []string, takeNewest bool, pause time.Duration) (res CaseResult) {
	res.Case = name
	var created []string
	defer func() {
		for _, id := range created {
			_ = v.Delete(id, true)
		}
	}()
	keeper, err := v.Create(selftestItem("a", keeperPW, "note a"))
	if err != nil {
		res.Error = err.Error()
		return
	}
	created = append(created, keeper.ID())
	var others []Item
	for i, pw := range otherPWs {
		time.Sleep(pause)
		suffix := string(rune('b' + i))
		o, err := v.Create(selftestItem(suffix, pw, "note "+suffix))
		if err != nil {
			res.Error = err.Error()
			return
		}
		created = append(created, o.ID())
		others = append(others, o)
	}
	if keeper, err = v.Get(keeper.ID()); err != nil {
		res.Error = err.Error()
		return
	}
	for i := range others {
		if others[i], err = v.Get(others[i].ID()); err != nil {
			res.Error = err.Error()
			return
		}
	}

	pg := &PlanGroup{No: 1, Title: "selftest", Active: true, Keeper: keeper.ID(), PasswordFrom: keeper.ID()}
	if takeNewest {
		pg.PasswordFrom = others[len(others)-1].ID()
	}
	all := append([]Item{keeper}, others...)
	for _, it := range all {
		pg.Entries = append(pg.Entries, &PlanEntry{ID: it.ID(), Include: true, Name: it.Name(), Revision: it.RevisionDate()})
	}
	steps := BuildSteps(&Plan{Groups: []*PlanGroup{pg}}, all)
	if len(steps) != 1 || !steps[0].Ready() {
		res.Error = "could not build step"
		if len(steps) == 1 {
			res.Error += ": " + steps[0].Problem
		}
		return
	}
	target := steps[0].target
	if err := Apply(v, steps[0]); err != nil {
		res.Error = err.Error()
		return
	}
	actual, err := v.Get(keeper.ID())
	if err != nil {
		res.Error = err.Error()
		return
	}
	history := map[string]int{}
	for _, h := range actual.History() {
		history[h.Password]++
	}
	allInHistory, noDupes := true, true
	for _, it := range all {
		if pw := it.Password(); pw != target && history[pw] == 0 {
			allInHistory = false
		}
	}
	for _, n := range history {
		if n > 1 {
			noDupes = false
		}
	}
	notesOK := true
	for i := range others {
		if !strings.Contains(actual.Notes(), fmt.Sprintf("note %c", 'b'+i)) {
			notesOK = false
		}
	}
	trashed := true
	for _, o := range others {
		if it, err := v.Get(o.ID()); err == nil && !it.Deleted() {
			trashed = false
		}
	}
	res.Checks = []CheckResult{
		{"password_chosen", actual.Password() == target},
		{"others_in_history", allInHistory},
		{"history_no_duplicates", noDupes},
		{"all_uris", len(actual.URIs()) == len(all)},
		{"notes_appended", notesOK},
		{"duplicates_trashed", trashed},
	}
	return
}
