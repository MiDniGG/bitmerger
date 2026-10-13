// Package demo is a backend with made-up data and realistic delays. It lets
// people try BitMerger, and lets us test the UI, without a real vault.
package demo

import (
	"errors"
	"fmt"
	"time"

	"github.com/MiDniGG/bitmerger/internal/bw"
	"github.com/MiDniGG/bitmerger/internal/dedupe"
)

// Backend implements server.Backend in memory.
type Backend struct {
	*dedupe.FakeVault
	delay    time.Duration
	unlocked bool
	email    string
}

// New returns a demo backend. delay is the simulated latency of one CLI call.
func New(delay time.Duration) *Backend {
	return &Backend{FakeVault: dedupe.NewFakeVault(sampleItems()), delay: delay}
}

func (b *Backend) wait(n int) { time.Sleep(time.Duration(n) * b.delay) }

func (b *Backend) Login(r bw.LoginRequest) error {
	b.wait(3)
	if r.Password == "" && r.ClientID == "" {
		return errors.New("Master password is required.")
	}
	if r.Password == "2fa" && r.Code == "" {
		return fmt.Errorf("%w: Two-step login code is required.", bw.ErrTwoStep)
	}
	b.unlocked, b.email = true, r.Email
	if b.email == "" {
		b.email = "demo@example.com"
	}
	return nil
}

func (b *Backend) StartServe() error { return nil }
func (b *Backend) StopServe()        {}

func (b *Backend) Status() (bw.Status, error) {
	st := "locked"
	if b.unlocked {
		st = "unlocked"
	}
	return bw.Status{ServerURL: "https://demo.invalid", UserEmail: b.email, Status: st}, nil
}

func (b *Backend) Unlocked() bool { return b.unlocked }
func (b *Backend) Sync() error    { b.wait(4); return nil }

func (b *Backend) Snapshot() (*bw.Snapshot, error) {
	b.wait(2)
	return &bw.Snapshot{
		Items:       b.List(),
		Orgs:        []bw.Org{{ID: "o1", Name: "Family"}, {ID: "o2", Name: "Work"}},
		Collections: []bw.Collection{{ID: "c1", OrganizationID: "o1", Name: "Shopping"}, {ID: "c2", OrganizationID: "o2", Name: "Apps"}, {ID: "c3", OrganizationID: "o1", Name: "Finance"}},
	}, nil
}

func (b *Backend) Password(id string) (string, error) {
	b.wait(4)
	it, err := b.Get(id)
	if err != nil {
		return "", err
	}
	return it.Password(), nil
}

func (b *Backend) Get(id string) (dedupe.Item, error) { b.wait(1); return b.FakeVault.Get(id) }
func (b *Backend) Edit(it dedupe.Item) (dedupe.Item, error) {
	b.wait(2)
	return b.FakeVault.Edit(it)
}
func (b *Backend) Delete(id string, permanent bool) error {
	b.wait(1)
	return b.FakeVault.Delete(id, permanent)
}
func (b *Backend) Close() { b.unlocked = false }

func login(id, name, user, pw, rev string, uris ...string) dedupe.Item {
	var us []any
	for _, u := range uris {
		us = append(us, map[string]any{"uri": u, "match": nil})
	}
	return dedupe.Item{"id": id, "type": float64(1), "name": name, "revisionDate": rev,
		"login": map[string]any{"username": user, "password": pw, "uris": us}}
}

func inOrg(it dedupe.Item, org string, colls ...string) dedupe.Item {
	it["organizationId"] = org
	var l []any
	for _, c := range colls {
		l = append(l, c)
	}
	it["collectionIds"] = l
	return it
}

func sampleItems() []dedupe.Item {
	amazonPersonal := login("1", "Amazon", "jane@example.com", "Sunny-Day-42", "2026-05-01T10:00:00Z", "https://www.amazon.de")
	amazonPersonal["notes"] = "Prime until 2027"
	amazonPersonal["passwordHistory"] = []any{map[string]any{"password": "OldOne-1", "lastUsedDate": "2025-01-01T00:00:00Z"}}
	amazonFamily := inOrg(login("2", "amazon.de", "jane@example.com", "Winter-Is-7", "2026-01-01T10:00:00Z", "amazon.de/login"), "o1", "c1")
	amazonFamily["login"].(map[string]any)["totp"] = "JBSWY3DPEHPK3PXP"
	amazonApp := inOrg(login("3", "Amazon App", "jane@example.com", "Sunny-Day-42", "2026-01-02T10:00:00Z", "androidapp://de.amazon.mShop"), "o2", "c2")
	return []dedupe.Item{
		amazonPersonal, amazonFamily, amazonApp,
		login("4", "Router", "", "admin-pw-1", "2026-01-01T10:00:00Z", "http://192.168.178.1"),
		login("5", "Router old", "", "admin-pw-1", "2025-06-01T10:00:00Z", "http://192.168.178.1:80/"),
		login("6", "Microsoft", "jane@example.com", "Blue-Sky-9", "2026-02-01T10:00:00Z", "https://login.live.com"),
		login("7", "MS account", "jane@example.com", "Blue-Sky-9", "2026-02-01T09:00:00Z", "https://account.microsoft.com"),
		login("8", "Microsoft work", "jane@company.example", "Work-123", "2026-02-01T09:00:00Z", "https://login.microsoftonline.com"),
		login("9", "Photos", "jane", "Pic-1", "2026-03-20T10:00:00Z", "https://photos.example.org"),
		login("10", "Files", "jane", "File-2", "2026-03-20T10:00:00Z", "https://files.example.org"),
		login("11", "Tools", "jane", "Tool-3", "2026-03-20T10:00:00Z", "https://tools.example.org"),
		login("12", "Tools (old)", "jane", "Tool-2", "2025-11-20T10:00:00Z", "https://tools.example.org/login"),
		inOrg(login("13", "Bank", "customer-1", "Money-1", "2026-01-01T10:00:00Z"), "o1", "c3"),
		login("14", "bank", "Customer-1", "Money-0", "2025-01-01T10:00:00Z"),
	}
}
