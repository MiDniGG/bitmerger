package dedupe

import (
	"errors"
	"fmt"
	"sort"
)

// Vault is what merging needs from the password manager.
type Vault interface {
	Get(id string) (Item, error)
	// Edit stores an item and returns the stored version. The item's
	// revisionDate must be the one of the latest stored version.
	Edit(it Item) (Item, error)
	Create(it Item) (Item, error)
	SetCollections(id, orgID string, collectionIDs []string) error
	CopyAttachment(sourceID string, attachment map[string]any, targetID string) error
	Delete(id string, permanent bool) error
}

// Problem codes for groups that are skipped.
const (
	ProblemMissing     = "missing"      // an item is gone
	ProblemChanged     = "changed"      // an item changed since the plan
	ProblemMultiOrg    = "multi_org"    // items in more than one organization
	ProblemHistoryFull = "history_full" // passwords do not fit into the history
)

// Step is what happens to one selected group.
type Step struct {
	No      int      `json:"no"`
	Title   string   `json:"title"`
	Problem string   `json:"problem,omitempty"`
	Names   []string `json:"names,omitempty"` // items the problem refers to

	KeeperName  string   `json:"keeperName,omitempty"`
	KeeperOwner string   `json:"keeperOwner,omitempty"`
	PW          string   `json:"pw,omitempty"`
	NewName     string   `json:"newName,omitempty"`
	Changes     []Change `json:"changes,omitempty"`
	Trash       []Trash  `json:"trash,omitempty"`
	Warning     string   `json:"warning,omitempty"` // "org_items_deleted"

	keeper      Item
	others      []Item
	merged      Item
	target      string
	chain       []string
	expected    []string
	collections []string
	attachments [][2]any // {sourceID string, attachment map}
}

// Change is one thing the keeper takes over, e.g. {"uris", 2}.
type Change struct {
	Kind string `json:"kind"`
	From int    `json:"from,omitempty"`
	N    int    `json:"n,omitempty"`
}

type Trash struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// BuildSteps checks the plan against the current vault content.
func BuildSteps(plan *Plan, items []Item) []*Step {
	byID := map[string]Item{}
	for _, it := range items {
		if !it.Deleted() {
			byID[it.ID()] = it
		}
	}
	var steps []*Step
	for _, g := range plan.Groups {
		if !g.Active {
			continue
		}
		var included []*PlanEntry
		entry := map[string]*PlanEntry{}
		for _, e := range g.Entries {
			entry[e.ID] = e
			if e.Include {
				included = append(included, e)
			}
		}
		if len(included) < 2 {
			continue
		}
		s := &Step{No: g.No, Title: g.Title}
		steps = append(steps, s)
		var missing, changed []string
		for _, e := range included {
			it, ok := byID[e.ID]
			switch {
			case !ok:
				missing = append(missing, e.Name)
			case it.RevisionDate() != e.Revision:
				changed = append(changed, e.Name)
			}
		}
		if len(missing) > 0 {
			s.Problem, s.Names = ProblemMissing, missing
			continue
		}
		if len(changed) > 0 {
			s.Problem, s.Names = ProblemChanged, changed
			continue
		}
		keeperEntry := entry[g.Keeper]
		if keeperEntry == nil || !keeperEntry.Include {
			keeperEntry = included[0]
		}
		keeper := byID[keeperEntry.ID]
		var others []Item
		orgs := map[string]bool{}
		if keeper.OrgID() != "" {
			orgs[keeper.OrgID()] = true
		}
		for _, e := range included {
			if e.ID == keeper.ID() {
				continue
			}
			o := byID[e.ID]
			others = append(others, o)
			if o.OrgID() != "" {
				orgs[o.OrgID()] = true
			}
		}
		if len(orgs) > 1 {
			s.Problem = ProblemMultiOrg
			continue
		}
		if keeper.OrgID() == "" && len(orgs) > 0 {
			s.Warning = "org_items_deleted"
		}

		merged, notes := Merge(keeper, others)
		if g.NewName != "" {
			merged["name"] = g.NewName
		}
		target := keeper.Password()
		if src, ok := byID[g.PasswordFrom]; ok && entry[g.PasswordFrom] != nil && entry[g.PasswordFrom].Include && src.Password() != "" {
			target = src.Password()
		}
		var chain []string
		var expected []string
		if keeper.Type() == TypeLogin {
			var required map[string]bool
			chain, required = PasswordChain(keeper, others, target)
			expected = SimulateHistory(keeper, chain, target)
			inHistory := map[string]bool{}
			for _, pw := range expected {
				inHistory[pw] = true
			}
			lost := 0
			for pw := range required {
				if !inHistory[pw] {
					lost++
				}
			}
			if lost > 0 {
				s.Problem, s.Names = ProblemHistoryFull, []string{fmt.Sprint(lost)}
				continue
			}
			dropped := 0
			for _, pw := range chain {
				if !inHistory[pw] {
					dropped++
				}
			}
			if dropped > 0 {
				notes = append(notes, fmt.Sprintf("history_dropped:%d", dropped))
			}
		}

		s.keeper, s.others, s.merged, s.target, s.chain, s.expected = keeper, others, merged, target, chain, expected
		s.KeeperName, s.KeeperOwner, s.PW = keeper.Name(), keeperEntry.Owner, ""
		if pe := entry[g.PasswordFrom]; pe != nil {
			s.PW = pe.PW
		}
		if merged.Name() != keeper.Name() {
			s.NewName = merged.Name()
		}
		if n := len(merged.URIs()) - len(keeper.URIs()); n > 0 {
			s.Changes = append(s.Changes, Change{Kind: "uris", N: n})
		}
		if before := len(keeper.History()); len(expected) != before && keeper.Type() == TypeLogin {
			s.Changes = append(s.Changes, Change{Kind: "history", From: before, N: len(expected)})
		}
		if merged.Notes() != keeper.Notes() {
			s.Changes = append(s.Changes, Change{Kind: "notes"})
		}
		if n := len(merged.Fields()) - len(keeper.Fields()); n > 0 {
			s.Changes = append(s.Changes, Change{Kind: "fields", N: n})
		}
		for _, o := range others {
			for _, a := range o.Attachments() {
				s.attachments = append(s.attachments, [2]any{o.ID(), a})
			}
		}
		if n := len(s.attachments); n > 0 {
			s.Changes = append(s.Changes, Change{Kind: "attachments", N: n})
		}
		if keeper.OrgID() != "" {
			set := map[string]bool{}
			for _, c := range keeper.CollectionIDs() {
				set[c] = true
			}
			for _, o := range others {
				if o.OrgID() == keeper.OrgID() {
					for _, c := range o.CollectionIDs() {
						set[c] = true
					}
				}
			}
			s.collections = sortedKeys(set)
			before := append([]string{}, keeper.CollectionIDs()...)
			sort.Strings(before)
			if fmt.Sprint(before) != fmt.Sprint(s.collections) {
				s.Changes = append(s.Changes, Change{Kind: "collections", From: len(before), N: len(s.collections)})
			}
		}
		for _, n := range notes {
			s.Changes = append(s.Changes, Change{Kind: n})
		}
		for _, o := range others {
			s.Trash = append(s.Trash, Trash{Name: o.Name(), Owner: entry[o.ID()].Owner})
		}
	}
	return steps
}

// Ready reports whether the step will be executed.
func (s *Step) Ready() bool { return s.Problem == "" && s.keeper != nil }

// ErrVerify means the keeper was not stored as expected; duplicates are kept.
var ErrVerify = errors.New("password or history not stored as expected, duplicates kept")

// Apply executes one step: write everything into the keeper, verify, then
// move the duplicates to the trash.
func Apply(v Vault, s *Step) error {
	if !s.Ready() {
		return fmt.Errorf("step %d not ready", s.No)
	}
	item := s.merged.Clone()
	delete(item, "passwordHistory")
	if item.Type() == TypeLogin {
		lg := item["login"].(map[string]any)
		for _, pw := range s.chain {
			lg["password"] = pw
			stored, err := v.Edit(item)
			if err != nil {
				return err
			}
			item["revisionDate"] = stored.RevisionDate()
		}
		lg["password"] = s.target
	}
	if _, err := v.Edit(item); err != nil {
		return err
	}
	actual, err := v.Get(s.keeper.ID())
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, h := range actual.History() {
		have[h.Password] = true
	}
	found := 0
	for _, pw := range s.expected {
		if have[pw] {
			found++
		}
	}
	if found < len(s.expected) {
		return fmt.Errorf("%w: %d of %d old passwords in the history", ErrVerify, found, len(s.expected))
	}
	if item.Type() == TypeLogin && actual.Password() != s.target {
		return fmt.Errorf("%w: password not set", ErrVerify)
	}
	if len(s.collections) > 0 {
		before := append([]string{}, s.keeper.CollectionIDs()...)
		sort.Strings(before)
		if fmt.Sprint(before) != fmt.Sprint(s.collections) {
			if err := v.SetCollections(s.keeper.ID(), s.keeper.OrgID(), s.collections); err != nil {
				return err
			}
		}
	}
	for _, a := range s.attachments {
		if err := v.CopyAttachment(a[0].(string), a[1].(map[string]any), s.keeper.ID()); err != nil {
			return err
		}
	}
	for _, o := range s.others {
		if err := v.Delete(o.ID(), false); err != nil {
			return err
		}
	}
	return nil
}
