package dedupe

import (
	"sort"
	"strings"
	"time"
)

// Scope codes describe where the items of a group live.
const (
	ScopePersonal    = "personal"
	ScopeOrg         = "org"
	ScopePersonalOrg = "personal_org"
	ScopeMultiOrg    = "multi_org"
)

// Note and block codes. The UI translates them.
const (
	NoteSubdomains = "subdomains"
	BlockPasskeys  = "passkeys"
	BlockTypes     = "types"
)

// Plan is the proposal the user reviews. It never contains secrets.
type Plan struct {
	Created string       `json:"created"`
	Account string       `json:"account"`
	Match   Match        `json:"match"`
	Groups  []*PlanGroup `json:"groups"`
}

type PlanGroup struct {
	No           int          `json:"no"`
	Title        string       `json:"title"`
	Scope        string       `json:"scope"`
	Reasons      []string     `json:"reasons"`
	Notes        []string     `json:"notes"`
	Blocked      []string     `json:"blocked"`
	Active       bool         `json:"active"`
	Keeper       string       `json:"keeper"`
	NewName      string       `json:"newName"`
	PasswordFrom string       `json:"passwordFrom"`
	Entries      []*PlanEntry `json:"entries"`
}

type PlanEntry struct {
	ID          string    `json:"id"`
	Include     bool      `json:"include"`
	Name        string    `json:"name"`
	Owner       string    `json:"owner"` // organization name, empty = personal
	Folder      string    `json:"folder"`
	Username    string    `json:"username"`
	Collections []string  `json:"collections"`
	PW          string    `json:"pw"` // A, B ... or "-"
	PWChanged   string    `json:"pwChanged,omitempty"`
	Hosts       []string  `json:"hosts"`
	Features    []Feature `json:"features"`
	Revision    string    `json:"revision"`
}

// Feature is something worth knowing about an item, e.g. {"totp"} or {"attachments", 2}.
type Feature struct {
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
}

// Directory resolves IDs to display names.
type Directory struct {
	Orgs        map[string]string
	Collections map[string]string
	Folders     map[string]string
	Skip        map[string]bool // folder IDs whose items are left out
}

// Scope tells where the items of a group live.
func Scope(items []Item) string {
	orgs := map[string]bool{}
	personal := false
	for _, it := range items {
		if it.OrgID() == "" {
			personal = true
		} else {
			orgs[it.OrgID()] = true
		}
	}
	switch {
	case len(orgs) == 0:
		return ScopePersonal
	case len(orgs) > 1:
		return ScopeMultiOrg
	case personal:
		return ScopePersonalOrg
	default:
		return ScopeOrg
	}
}

// pwLetter turns 0, 1, ... into A, B, ..., Z, AA, AB, ...
func pwLetter(n int) string {
	s := ""
	for n >= 0 {
		s = string(rune('A'+n%26)) + s
		n = n/26 - 1
	}
	return s
}

// PWLabels gives each distinct password of a group a label (A, B, ...).
func PWLabels(items []Item) []string {
	labels := map[string]string{}
	out := make([]string, len(items))
	for i, it := range items {
		h := pwHash(it.Password())
		if h == "" {
			out[i] = "-"
			continue
		}
		if _, ok := labels[h]; !ok {
			labels[h] = pwLetter(len(labels))
		}
		out[i] = labels[h]
	}
	return out
}

func features(it Item) []Feature {
	var f []Feature
	if it.TOTP() != "" {
		f = append(f, Feature{Kind: "totp"})
	}
	if it.HasPasskey() {
		f = append(f, Feature{Kind: "passkey"})
	}
	if n := len(it.Attachments()); n > 0 {
		f = append(f, Feature{Kind: "attachments", Count: n})
	}
	if it.Notes() != "" {
		f = append(f, Feature{Kind: "notes"})
	}
	if n := len(it.Fields()); n > 0 {
		f = append(f, Feature{Kind: "fields", Count: n})
	}
	if n := len(it.History()); n > 0 {
		f = append(f, Feature{Kind: "history", Count: n})
	}
	if it.Favorite() {
		f = append(f, Feature{Kind: "favorite"})
	}
	return f
}

func hostList(it Item) []string {
	seen := map[string]bool{}
	for _, u := range it.URIs() {
		seen[Host(u)] = true
	}
	return sortedKeys(seen)
}

// suggestKeeper prefers items with a passkey, then org items, then the newest.
func suggestKeeper(items []Item) string {
	candidates := items
	var withPasskey []Item
	for _, it := range items {
		if it.HasPasskey() {
			withPasskey = append(withPasskey, it)
		}
	}
	if len(withPasskey) > 0 {
		candidates = withPasskey
	}
	for _, it := range candidates {
		if it.OrgID() != "" {
			return it.ID()
		}
	}
	return candidates[0].ID()
}

// BuildPlan turns the groups into a reviewable proposal.
func BuildPlan(items []Item, dir Directory, match Match) *Plan {
	var live []Item
	for _, it := range items {
		if !it.Deleted() && !dir.Skip[it.FolderID()] {
			live = append(live, it)
		}
	}
	plan := &Plan{Created: time.Now().UTC().Format(time.RFC3339), Match: match, Groups: []*PlanGroup{}}
	for n, g := range FindGroups(live, match) {
		scope := Scope(g.Items)
		pg := &PlanGroup{
			No:      n + 1,
			Title:   Title(g.Items, match),
			Scope:   scope,
			Reasons: sortedKeys(g.Reasons),
			Notes:   []string{},
			Blocked: []string{},
			Keeper:  suggestKeeper(g.Items),
		}
		passkeys, types := 0, map[int]bool{}
		for _, it := range g.Items {
			if it.HasPasskey() {
				passkeys++
			}
			types[it.Type()] = true
		}
		if passkeys > 1 {
			pg.Blocked = append(pg.Blocked, BlockPasskeys)
		}
		if len(types) > 1 {
			pg.Blocked = append(pg.Blocked, BlockTypes)
		}
		if match == MatchDomain && distinctHosts(g.Items) > 1 {
			pg.Notes = append(pg.Notes, NoteSubdomains)
		}
		pg.Active = len(pg.Blocked) == 0 && len(pg.Notes) == 0 && !g.Reasons[ReasonName] && scope != ScopeMultiOrg
		pg.PasswordFrom = g.Items[0].ID()
		for _, it := range g.Items {
			if it.Password() != "" {
				pg.PasswordFrom = it.ID()
				break
			}
		}
		labels := PWLabels(g.Items)
		for i, it := range g.Items {
			e := &PlanEntry{ID: it.ID(), Include: true, PW: labels[i], Revision: it.RevisionDate()}
			e.describe(it, dir)
			pg.Entries = append(pg.Entries, e)
		}
		plan.Groups = append(plan.Groups, pg)
	}
	return plan
}

// describe fills the display fields of an entry from the item.
func (e *PlanEntry) describe(it Item, dir Directory) {
	var colls []string
	for _, c := range it.CollectionIDs() {
		name := dir.Collections[c]
		if name == "" {
			name = "?"
		}
		colls = append(colls, name)
	}
	owner := ""
	if it.OrgID() != "" {
		owner = dir.Orgs[it.OrgID()]
		if owner == "" {
			owner = "?"
		}
	}
	e.Name, e.Owner, e.Folder, e.Username = it.Name(), owner, dir.Folders[it.FolderID()], it.Username()
	e.Collections, e.Hosts, e.Features = nonNil(colls), nonNil(hostList(it)), features(it)
	e.PWChanged, _ = it.Login()["passwordRevisionDate"].(string)
}

// FolderInfo is a folder as the scan page lists it.
type FolderInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Trash bool   `json:"trash"`
}

// Folders lists the folders that hold items, sorted by name.
func Folders(items []Item, dir Directory) []FolderInfo {
	count := map[string]int{}
	for _, it := range items {
		if !it.Deleted() && it.FolderID() != "" {
			count[it.FolderID()]++
		}
	}
	out := []FolderInfo{}
	for id, n := range count {
		name := dir.Folders[id]
		if name == "" {
			name = "?"
		}
		out = append(out, FolderInfo{ID: id, Name: name, Count: n, Trash: IsTrashFolder(name)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Refresh updates what the review shows (name, folder, username, ...) from
// the current vault. Decisions, password labels and the revision the plan
// was made from stay, so a changed item is still detected before merging.
// It also fills fields that plans of older versions did not have. Entries
// in left-out folders are dropped, like a new scan would, and so are groups
// with fewer than two entries left.
func (p *Plan) Refresh(items []Item, dir Directory) {
	byID := map[string]Item{}
	for _, it := range items {
		if !it.Deleted() {
			byID[it.ID()] = it
		}
	}
	groups := p.Groups[:0]
	for _, g := range p.Groups {
		entries := g.Entries[:0]
		for _, e := range g.Entries {
			if it, ok := byID[e.ID]; ok {
				if dir.Skip[it.FolderID()] {
					continue
				}
				e.describe(it, dir)
			}
			entries = append(entries, e)
		}
		g.Entries = entries
		if len(g.Entries) < 2 {
			continue
		}
		included, keeper := 0, false
		for _, e := range g.Entries {
			if e.Include {
				included++
			}
			keeper = keeper || e.ID == g.Keeper
		}
		if !keeper {
			g.Keeper = g.Entries[0].ID
		}
		if included < 2 {
			g.Active = false
		}
		groups = append(groups, g)
	}
	p.Groups = groups
}

// trashFolders are the names password managers give their recycle bin.
// Imports (KeePass in particular) bring it along as an ordinary folder.
var trashFolders = map[string]bool{
	"papierkorb": true, "recycle bin": true, "trash": true, "deleted items": true,
	"gelöschte objekte": true, "gelöschte elemente": true, "corbeille": true, "papelera": true, "cestino": true, "prullenbak": true,
}

// IsTrashFolder reports whether a folder (or nested folder "a/b") is a
// recycle bin. Such folders are left out unless the user includes them.
func IsTrashFolder(name string) bool {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return trashFolders[strings.ToLower(strings.TrimSpace(name))]
}

// CarryOver keeps the user's choices for groups that are unchanged: same
// entries as a group of the old plan. Other groups keep their proposal.
func (p *Plan) CarryOver(old *Plan) {
	key := func(g *PlanGroup) string {
		ids := make([]string, len(g.Entries))
		for i, e := range g.Entries {
			ids[i] = e.ID
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	before := map[string]*PlanGroup{}
	for _, g := range old.Groups {
		before[key(g)] = g
	}
	for _, g := range p.Groups {
		o := before[key(g)]
		if o == nil {
			continue
		}
		include := map[string]bool{}
		for _, e := range o.Entries {
			include[e.ID] = e.Include
		}
		for _, e := range g.Entries {
			e.Include = include[e.ID]
		}
		g.Active, g.Keeper, g.NewName, g.PasswordFrom = o.Active, o.Keeper, o.NewName, o.PasswordFrom
	}
}

// ApplyDecisions copies the user's choices from a posted plan onto the
// server-side plan. Groups and entries themselves cannot be changed this way.
func (p *Plan) ApplyDecisions(posted *Plan) {
	byNo := map[int]*PlanGroup{}
	for _, g := range posted.Groups {
		byNo[g.No] = g
	}
	for _, g := range p.Groups {
		in := byNo[g.No]
		if in == nil {
			continue
		}
		ids := map[string]bool{}
		for _, e := range g.Entries {
			ids[e.ID] = true
		}
		g.Active = in.Active && len(g.Blocked) == 0
		if ids[in.Keeper] {
			g.Keeper = in.Keeper
		}
		if ids[in.PasswordFrom] {
			g.PasswordFrom = in.PasswordFrom
		}
		name := []rune(in.NewName)
		if len(name) > 200 {
			name = name[:200]
		}
		g.NewName = string(name)
		include := map[string]bool{}
		for _, e := range in.Entries {
			include[e.ID] = e.Include
		}
		for _, e := range g.Entries {
			if v, ok := include[e.ID]; ok {
				e.Include = v
			}
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
