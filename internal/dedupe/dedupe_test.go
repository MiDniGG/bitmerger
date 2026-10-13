package dedupe

import (
	"strings"
	"testing"
)

func login(id, name, user, pw string, uris ...string) Item {
	var us []any
	for _, u := range uris {
		us = append(us, map[string]any{"uri": u, "match": nil})
	}
	return Item{"id": id, "type": float64(1), "name": name, "revisionDate": "2026-01-01T00:00:00Z",
		"login": map[string]any{"username": user, "password": pw, "uris": us}}
}

func withRev(it Item, rev string) Item { it["revisionDate"] = rev; return it }
func withOrg(it Item, org string, c ...string) Item {
	it["organizationId"] = org
	var l []any
	for _, x := range c {
		l = append(l, x)
	}
	it["collectionIds"] = l
	return it
}

func TestSiteKey(t *testing.T) {
	cases := []struct {
		uri   string
		match Match
		want  string
	}{
		{"https://audio.midnigg.de", MatchHost, "audio.midnigg.de"},
		{"https://audio.midnigg.de", MatchDomain, "midnigg.de"},
		{"https://www.midnigg.de", MatchHost, "midnigg.de"},
		{"https://login.bbc.co.uk", MatchHost, "bbc.co.uk"},
		{"https://shop.example.co.uk", MatchHost, "shop.example.co.uk"},
		{"https://login.live.com", MatchHost, "microsoft"},
		{"https://account.microsoft.com", MatchHost, "microsoft"},
		{"androidapp://de.amazon.mShop", MatchHost, "amazon"},
		{"http://localhost:13378", MatchDomain, "localhost:13378"},
		{"http://192.168.0.95:8080", MatchDomain, "192.168.0.95:8080"},
		{"http://192.168.178.1:80/", MatchHost, "192.168.178.1"},
		{"fritz.box", MatchHost, "fritz.box"},
		{"", MatchHost, ""},
	}
	for _, c := range cases {
		if got := SiteKey(c.uri, c.match); got != c.want {
			t.Errorf("SiteKey(%q, %s) = %q, want %q", c.uri, c.match, got, c.want)
		}
	}
}

func selfHosted() []Item {
	return []Item{
		login("a", "Audiobookshelf", "MiDniGG", "1", "https://audio.midnigg.de", "http://localhost:13378"),
		login("b", "Mail Archiver", "midnigg", "2", "http://192.168.0.95", "https://archiv.midnigg.de"),
		login("c", "Tools - Andy", "midnigg", "3", "https://tools.midnigg.de"),
		login("d", "Tools", "midnigg", "3", "https://tools.midnigg.de/login"),
		login("e", "Paperless", "midnigg", "4", "https://paperless.midnigg.de"),
		login("f", "Other user", "someone@t-online.de", "5", "https://tools.midnigg.de"),
	}
}

func TestFindGroupsByHost(t *testing.T) {
	groups := FindGroups(selfHosted(), MatchHost)
	if len(groups) != 1 || len(groups[0].Items) != 2 {
		t.Fatalf("want one group of two, got %d groups", len(groups))
	}
	if !strings.HasPrefix(groups[0].Items[0].Name(), "Tools") {
		t.Errorf("unexpected group %v", groups[0].Items[0].Name())
	}
}

func TestFindGroupsByDomainMarksSubdomains(t *testing.T) {
	plan := BuildPlan(selfHosted(), Directory{}, MatchDomain)
	if len(plan.Groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(plan.Groups))
	}
	g := plan.Groups[0]
	if len(g.Entries) != 5 {
		t.Errorf("want 5 entries (other user excluded), got %d", len(g.Entries))
	}
	if g.Active || len(g.Notes) != 1 || g.Notes[0] != NoteSubdomains {
		t.Errorf("subdomain group must be marked and not preselected: %+v", g)
	}
}

func TestDifferentUsernamesNeverGroup(t *testing.T) {
	items := []Item{
		login("1", "MS", "a@x.de", "p", "https://login.live.com"),
		login("2", "MS", "b@x.de", "p", "https://account.microsoft.com"),
	}
	if g := FindGroups(items, MatchDomain); len(g) != 0 {
		t.Fatalf("different usernames grouped: %d", len(g))
	}
}

func TestNoUsernameNeedsSamePassword(t *testing.T) {
	items := []Item{
		login("1", "Router", "", "pw1", "http://192.168.178.1"),
		login("2", "Router old", "", "pw1", "http://192.168.178.1:80/"),
		login("3", "Router 2", "", "pw2", "http://192.168.178.1"),
	}
	g := FindGroups(items, MatchHost)
	if len(g) != 1 || len(g[0].Items) != 2 {
		t.Fatalf("want one group of two, got %+v", g)
	}
}

func TestPasswordChainKeeperKeepsPassword(t *testing.T) {
	keeper := withRev(login("k", "K", "u", "keep", "https://x.example"), "2026-03-01T00:00:00Z")
	o1 := withRev(login("o1", "O1", "u", "x", "https://x.example/a"), "2026-01-01T00:00:00Z")
	o2 := withRev(login("o2", "O2", "u", "y", "https://x.example/b"), "2026-02-01T00:00:00Z")
	chain, required := PasswordChain(keeper, []Item{o1, o2}, "keep")
	if strings.Join(chain, ",") != "x,y" {
		t.Errorf("chain = %v", chain)
	}
	if !required["x"] || !required["y"] || required["keep"] {
		t.Errorf("required = %v", required)
	}
	hist := SimulateHistory(keeper, chain, "keep")
	if strings.Join(hist, ",") != "y,x,keep" {
		t.Errorf("history = %v", hist)
	}
}

func TestApplyFillsHistoryAndTrashes(t *testing.T) {
	keeper := withOrg(withRev(login("k", "Amazon", "andy@x.de", "b", "amazon.de/login"), "2026-01-01T00:00:00Z"), "o1", "c1")
	keeper["notes"] = "N2"
	other := withRev(login("p", "Amazon personal", "Andy@x.de", "a", "https://www.amazon.de"), "2026-05-01T00:00:00Z")
	other["notes"] = "N1"
	other["passwordHistory"] = []any{map[string]any{"password": "ancient", "lastUsedDate": "2025-01-01T00:00:00Z"}}
	v := NewFakeVault([]Item{keeper, other})

	plan := BuildPlan(v.List(), Directory{Orgs: map[string]string{"o1": "Family"}}, MatchHost)
	if len(plan.Groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(plan.Groups))
	}
	g := plan.Groups[0]
	g.Active = true
	if g.Keeper != "k" {
		t.Errorf("org item should be suggested as keeper, got %s", g.Keeper)
	}
	g.PasswordFrom = "p"
	g.NewName = "Amazon"
	steps := BuildSteps(plan, v.List())
	if len(steps) != 1 || !steps[0].Ready() {
		t.Fatalf("step not ready: %+v", steps)
	}
	if err := Apply(v, steps[0]); err != nil {
		t.Fatal(err)
	}
	got, _ := v.Get("k")
	if got.Password() != "a" {
		t.Errorf("password = %q", got.Password())
	}
	var hist []string
	for _, h := range got.History() {
		hist = append(hist, h.Password)
	}
	for _, want := range []string{"b", "ancient"} {
		if !strings.Contains(strings.Join(hist, ","), want) {
			t.Errorf("history %v misses %q", hist, want)
		}
	}
	seen := map[string]bool{}
	for _, pw := range hist {
		if seen[pw] {
			t.Errorf("history has %q twice: %v", pw, hist)
		}
		seen[pw] = true
	}
	if !strings.Contains(got.Notes(), "N1") || !strings.Contains(got.Notes(), "N2") {
		t.Errorf("notes not merged: %q", got.Notes())
	}
	if len(got.URIs()) != 2 {
		t.Errorf("uris = %v", got.URIs())
	}
	if p, _ := v.Get("p"); !p.Deleted() {
		t.Error("duplicate not trashed")
	}
}

func TestStaleRevisionIsRejectedByFake(t *testing.T) {
	v := NewFakeVault([]Item{login("k", "K", "u", "a", "https://x.example")})
	it, _ := v.Get("k")
	if _, err := v.Edit(it); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Edit(it); err != ErrOutOfDate {
		t.Fatalf("stale edit must be rejected, got %v", err)
	}
}

func TestChangedSinceplanIsSkipped(t *testing.T) {
	items := []Item{login("1", "A", "u", "p", "https://x.example"), login("2", "B", "u", "p", "https://x.example")}
	plan := BuildPlan(items, Directory{}, MatchHost)
	items[1]["revisionDate"] = "2027-01-01T00:00:00Z"
	steps := BuildSteps(plan, items)
	if len(steps) != 1 || steps[0].Problem != ProblemChanged {
		t.Fatalf("want changed problem, got %+v", steps)
	}
}

func TestMultiOrgIsSkipped(t *testing.T) {
	items := []Item{
		withOrg(login("1", "A", "u", "p", "https://x.example"), "o1"),
		withOrg(login("2", "B", "u", "p", "https://x.example"), "o2"),
	}
	plan := BuildPlan(items, Directory{}, MatchHost)
	if plan.Groups[0].Active {
		t.Error("multi-org group must not be preselected")
	}
	plan.Groups[0].Active = true
	steps := BuildSteps(plan, items)
	if steps[0].Problem != ProblemMultiOrg {
		t.Fatalf("want multi_org, got %q", steps[0].Problem)
	}
}

func TestSelftestOnFake(t *testing.T) {
	for _, c := range Selftest(NewFakeVault(nil), 0) {
		if !c.OK() {
			t.Errorf("case %s failed: %+v", c.Case, c)
		}
	}
}

func TestRefreshFillsDisplayFieldsKeepsRevision(t *testing.T) {
	items := []Item{
		withRev(login("a", "Shop", "me", "x", "https://shop.example"), "r1"),
		withRev(login("b", "Shop", "me", "x", "https://shop.example"), "r1"),
	}
	items[0]["folderId"] = "f1"
	plan := BuildPlan(items, Directory{}, MatchHost)
	e := plan.Groups[0].Entries[0]
	e.Folder, e.Username = "", "" // like a plan saved by an older version
	items[0]["revisionDate"] = "r2"
	plan.Refresh(items, Directory{Folders: map[string]string{"f1": "Backup"}})
	if e.Folder != "Backup" || e.Username != "me" {
		t.Fatalf("folder %q username %q", e.Folder, e.Username)
	}
	if e.Revision != "r1" {
		t.Fatalf("revision changed to %q", e.Revision)
	}
}

func TestSkippedFolderIsIgnored(t *testing.T) {
	items := []Item{
		login("a", "Shop", "me", "x", "https://shop.example"),
		login("b", "Shop", "me", "x", "https://shop.example"),
	}
	items[1]["folderId"] = "bin"
	dir := Directory{Folders: map[string]string{"bin": "Import/Papierkorb"}, Skip: map[string]bool{"bin": true}}
	if f := Folders(items, dir); len(f) != 1 || !f[0].Trash || f[0].Count != 1 {
		t.Fatalf("folders %+v", f)
	}
	if plan := BuildPlan(items, dir, MatchHost); len(plan.Groups) != 0 {
		t.Fatalf("got %d groups, want 0", len(plan.Groups))
	}
	plan := BuildPlan(items, Directory{}, MatchHost)
	plan.Refresh(items, dir)
	if len(plan.Groups) != 0 {
		t.Fatalf("group with one entry left must be dropped, got %d groups", len(plan.Groups))
	}
}

func TestCarryOverKeepsChoicesOfUnchangedGroups(t *testing.T) {
	items := []Item{
		login("a", "Shop", "me", "x", "https://shop.example"),
		login("b", "Shop", "me", "y", "https://shop.example"),
		login("c", "Mail", "me", "z", "https://mail.example"),
		login("d", "Mail", "me", "z", "https://mail.example"),
	}
	old := BuildPlan(items, Directory{}, MatchHost)
	for _, g := range old.Groups {
		g.Active = false
		g.NewName = "renamed " + g.Title
	}
	items = append(items, login("e", "Mail 2", "me", "z", "https://mail.example"))
	plan := BuildPlan(items, Directory{}, MatchHost)
	plan.CarryOver(old)
	for _, g := range plan.Groups {
		changed := len(g.Entries) == 3
		if changed && (!g.Active || g.NewName != "") {
			t.Errorf("changed group %q must keep the proposal", g.Title)
		}
		if !changed && (g.Active || g.NewName == "") {
			t.Errorf("unchanged group %q lost its choices", g.Title)
		}
	}
}
