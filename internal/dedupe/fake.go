package dedupe

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrOutOfDate mirrors Vaultwarden's answer to an edit with a stale revisionDate.
var ErrOutOfDate = errors.New("The client copy of this cipher is out of date. Resync the client and try again.")

// FakeVault behaves like the Bitwarden CLI against Vaultwarden, in memory:
// a passwordHistory sent with an edit is ignored, a password change prepends
// the previous password (cut to HistoryLimit), and an edit with an outdated
// revisionDate is rejected.
type FakeVault struct {
	mu    sync.Mutex
	Items map[string]Item
	Log   []string
	clock time.Time
}

func NewFakeVault(items []Item) *FakeVault {
	f := &FakeVault{Items: map[string]Item{}, clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	for _, it := range items {
		f.Items[it.ID()] = it.Clone()
	}
	return f
}

func (f *FakeVault) tick() string {
	f.clock = f.clock.Add(time.Second)
	return f.clock.Format(time.RFC3339Nano)
}

func (f *FakeVault) List() []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Item
	for _, it := range f.Items {
		out = append(out, it.Clone())
	}
	return out
}

func (f *FakeVault) Get(id string) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.Items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return it.Clone(), nil
}

func (f *FakeVault) Edit(it Item) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.Items[it.ID()]
	if !ok {
		return nil, errors.New("not found")
	}
	if r := it.RevisionDate(); r != "" && r < old.RevisionDate() {
		return nil, ErrOutOfDate
	}
	f.Log = append(f.Log, "edit "+it.ID())
	n := it.Clone()
	var history []any
	if old.Password() != "" && old.Password() != n.Password() {
		history = append(history, map[string]any{"password": old.Password(), "lastUsedDate": f.tick()})
	}
	history = append(history, list(old["passwordHistory"])...)
	if len(history) > HistoryLimit {
		history = history[:HistoryLimit]
	}
	if len(history) > 0 {
		n["passwordHistory"] = history
	} else {
		n["passwordHistory"] = nil
	}
	n["revisionDate"] = f.tick()
	f.Items[n.ID()] = n
	return n.Clone(), nil
}

func (f *FakeVault) Create(it Item) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	n := it.Clone()
	n["id"] = hex.EncodeToString(b)
	n["revisionDate"] = f.tick()
	n["passwordHistory"] = nil
	f.Items[n.ID()] = n
	return n.Clone(), nil
}

func (f *FakeVault) SetCollections(id, orgID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Log = append(f.Log, "collections "+id)
	var l []any
	for _, c := range ids {
		l = append(l, c)
	}
	f.Items[id]["collectionIds"] = l
	return nil
}

func (f *FakeVault) CopyAttachment(sourceID string, a map[string]any, targetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Log = append(f.Log, "attachment "+sourceID+" -> "+targetID)
	f.Items[targetID]["attachments"] = append(list(f.Items[targetID]["attachments"]), a)
	return nil
}

func (f *FakeVault) Delete(id string, permanent bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Log = append(f.Log, "delete "+id)
	if permanent {
		delete(f.Items, id)
		return nil
	}
	f.Items[id]["deletedDate"] = f.tick()
	return nil
}
