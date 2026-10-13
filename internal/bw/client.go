package bw

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MiDniGG/bitmerger/internal/dedupe"
)

// Client runs the CLI with its own, throwaway data directory, so an existing
// Bitwarden CLI setup of the user is never touched. Close() logs out and
// removes that directory.
type Client struct {
	bin     string
	dataDir string

	mu      sync.Mutex
	session string
	serve   *serveConn
}

// NewClient prepares a client with a fresh data directory.
func NewClient(bin string) (*Client, error) {
	base, err := BaseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "session-")
	if err != nil {
		return nil, err
	}
	seedDevice(base, dir)
	return &Client{bin: bin, dataDir: dir}, nil
}

// The CLI identifies itself to the server with an application id that it
// creates in its data folder. A fresh folder per session would look like a
// new device on every start, and servers mail a "new device" notice each
// time. So the id (and nothing else) is kept in device.json and put into
// every new data folder.

const deviceKey = "global_applicationId_appId"

func seedDevice(base, dir string) {
	b, err := os.ReadFile(filepath.Join(base, "device.json"))
	if err != nil {
		return
	}
	var dev struct {
		AppID string `json:"appId"`
	}
	if json.Unmarshal(b, &dev) != nil || dev.AppID == "" {
		return
	}
	data, _ := json.Marshal(map[string]string{deviceKey: dev.AppID})
	_ = os.WriteFile(filepath.Join(dir, "data.json"), data, 0o600)
}

// rememberDevice stores the application id the CLI uses for this session.
func (c *Client) rememberDevice() {
	b, err := os.ReadFile(filepath.Join(c.dataDir, "data.json"))
	if err != nil {
		return
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return
	}
	id, _ := data[deviceKey].(string)
	if id == "" {
		return
	}
	out, _ := json.Marshal(map[string]string{"appId": id})
	_ = os.WriteFile(filepath.Join(filepath.Dir(c.dataDir), "device.json"), out, 0o600)
}

// CLIError carries the CLI's own message.
type CLIError struct {
	Command string
	Message string
}

func (e *CLIError) Error() string { return e.Message }

func (c *Client) run(env []string, args ...string) (string, error) {
	cmd := exec.Command(c.bin, append(args, "--nointeraction")...)
	cmd.Env = append(os.Environ(),
		"BITWARDENCLI_APPDATA_DIR="+c.dataDir,
		"BW_NOINTERACTION=true",
		"NODE_OPTIONS=",
	)
	c.mu.Lock()
	if c.session != "" {
		cmd.Env = append(cmd.Env, "BW_SESSION="+c.session)
	}
	c.mu.Unlock()
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", &CLIError{Command: args[0], Message: msg}
	}
	return stdout.String(), nil
}

func (c *Client) runJSON(out any, args ...string) error {
	s, err := c.run(nil, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), out)
}

// Status is the output of `bw status`.
type Status struct {
	ServerURL string `json:"serverUrl"`
	UserEmail string `json:"userEmail"`
	Status    string `json:"status"` // unauthenticated, locked, unlocked
}

func (c *Client) Status() (Status, error) {
	var s Status
	err := c.runJSON(&s, "status")
	return s, err
}

// LoginRequest holds what the user typed. Secrets are passed to the CLI via
// environment variables, never as arguments.
type LoginRequest struct {
	Server       string `json:"server"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	Method       string `json:"method"` // two-step method: 0 authenticator, 1 email, 3 YubiKey
	Code         string `json:"code"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// ErrTwoStep means the server wants a two-step code.
var ErrTwoStep = errors.New("two-step login required")

// Login logs in and unlocks. With ClientID set it uses the personal API key.
func (c *Client) Login(r LoginRequest) error {
	if st, err := c.Status(); err == nil && st.Status != "unauthenticated" {
		_, _ = c.run(nil, "logout")
	}
	if r.Server != "" {
		if _, err := c.run(nil, "config", "server", r.Server); err != nil {
			return err
		}
	}
	pwEnv := []string{"BITMERGER_PW=" + r.Password}
	if r.ClientID != "" {
		env := []string{"BW_CLIENTID=" + r.ClientID, "BW_CLIENTSECRET=" + r.ClientSecret}
		if _, err := c.run(env, "login", "--apikey"); err != nil {
			return err
		}
		key, err := c.run(pwEnv, "unlock", "--passwordenv", "BITMERGER_PW", "--raw")
		if err != nil {
			return err
		}
		c.setSession(key)
		c.rememberDevice()
		return nil
	}
	args := []string{"login", r.Email, "--passwordenv", "BITMERGER_PW", "--raw"}
	if r.Method != "" {
		args = append(args, "--method", r.Method)
		if r.Code != "" {
			args = append(args, "--code", r.Code)
		}
	}
	key, err := c.run(pwEnv, args...)
	if err != nil {
		var ce *CLIError
		if errors.As(err, &ce) && r.Code == "" && looksLikeTwoStep(ce.Message) {
			return fmt.Errorf("%w: %s", ErrTwoStep, ce.Message)
		}
		return err
	}
	c.setSession(key)
	c.rememberDevice()
	return nil
}

func looksLikeTwoStep(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "two-step") || strings.Contains(m, "two step") ||
		strings.Contains(m, "verification code") || strings.Contains(m, "code is required")
}

func (c *Client) setSession(key string) {
	c.mu.Lock()
	c.session = strings.TrimSpace(key)
	c.mu.Unlock()
}

// Unlocked reports whether a session is active.
func (c *Client) Unlocked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session != ""
}

func (c *Client) Sync() error {
	if s := c.conn(); s != nil {
		if err := s.sync(); !c.serveBroken(err) {
			return err
		}
	}
	_, err := c.run(nil, "sync")
	return err
}

type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Collection struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
}

// Snapshot is the whole vault as needed for planning.
type Snapshot struct {
	Items       []dedupe.Item
	Orgs        []Org
	Collections []Collection
	Folders     []Folder
}

type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Snapshot() (*Snapshot, error) {
	if sc := c.conn(); sc != nil {
		if snap, err := sc.snapshot(); !c.serveBroken(err) {
			return snap, err
		}
	}
	s := &Snapshot{}
	if err := c.runJSON(&s.Items, "list", "items"); err != nil {
		return nil, err
	}
	if err := c.runJSON(&s.Orgs, "list", "organizations"); err != nil {
		return nil, err
	}
	if err := c.runJSON(&s.Collections, "list", "collections"); err != nil {
		return nil, err
	}
	if err := c.runJSON(&s.Folders, "list", "folders"); err != nil {
		return nil, err
	}
	return s, nil
}

// Directory maps IDs to names for the plan.
func (s *Snapshot) Directory() dedupe.Directory {
	d := dedupe.Directory{Orgs: map[string]string{}, Collections: map[string]string{}, Folders: map[string]string{}}
	for _, f := range s.Folders {
		if f.ID != "" {
			d.Folders[f.ID] = f.Name
		}
	}
	for _, o := range s.Orgs {
		d.Orgs[o.ID] = o.Name
	}
	for _, col := range s.Collections {
		d.Collections[col.ID] = col.Name
	}
	return d
}

func (c *Client) Get(id string) (dedupe.Item, error) {
	if s := c.conn(); s != nil {
		if it, err := s.get(id); !c.serveBroken(err) {
			return it, err
		}
	}
	var it dedupe.Item
	err := c.runJSON(&it, "get", "item", id)
	return it, err
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Edit stores an item. The CLI sends the item's revisionDate as "last known
// revision"; Vaultwarden rejects the edit when that is older than the server
// copy. On that answer: sync, take the current revisionDate, retry once.
func (c *Client) Edit(it dedupe.Item) (dedupe.Item, error) {
	send := func(item dedupe.Item) (dedupe.Item, error) {
		if s := c.conn(); s != nil {
			if out, err := s.edit(item); !c.serveBroken(err) {
				return out, err
			}
		}
		enc, err := encode(item)
		if err != nil {
			return nil, err
		}
		var out dedupe.Item
		return out, c.runJSON(&out, "edit", "item", item.ID(), enc)
	}
	out, err := send(it)
	var ce *CLIError
	if err == nil || !errors.As(err, &ce) || !strings.Contains(ce.Message, "out of date") {
		return out, err
	}
	if err := c.Sync(); err != nil {
		return nil, err
	}
	current, err := c.Get(it.ID())
	if err != nil {
		return nil, err
	}
	retry := it.Clone()
	retry["revisionDate"] = current.RevisionDate()
	return send(retry)
}

func (c *Client) Create(it dedupe.Item) (dedupe.Item, error) {
	if s := c.conn(); s != nil {
		if out, err := s.create(it); !c.serveBroken(err) {
			return out, err
		}
	}
	enc, err := encode(it)
	if err != nil {
		return nil, err
	}
	var out dedupe.Item
	return out, c.runJSON(&out, "create", "item", enc)
}

func (c *Client) SetCollections(id, orgID string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	enc, err := encode(ids)
	if err != nil {
		return err
	}
	_, err = c.run(nil, "edit", "item-collections", id, enc, "--organizationid", orgID)
	return err
}

func (c *Client) CopyAttachment(sourceID string, a map[string]any, targetID string) error {
	tmp, err := os.MkdirTemp(c.dataDir, "attachment-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	name, _ := a["fileName"].(string)
	id, _ := a["id"].(string)
	path := filepath.Join(tmp, filepath.Base(name))
	if _, err := c.run(nil, "get", "attachment", id, "--itemid", sourceID, "--output", path); err != nil {
		return err
	}
	_, err = c.run(nil, "create", "attachment", "--file", path, "--itemid", targetID)
	return err
}

func (c *Client) Delete(id string, permanent bool) error {
	if s := c.conn(); s != nil && !permanent {
		if err := s.del(id); !c.serveBroken(err) {
			return err
		}
	}
	args := []string{"delete", "item", id}
	if permanent {
		args = append(args, "--permanent")
	}
	_, err := c.run(nil, args...)
	return err
}

// Password reads one password live from the vault.
func (c *Client) Password(id string) (string, error) {
	it, err := c.Get(id)
	if err != nil {
		return "", err
	}
	return it.Password(), nil
}

// Close logs out and removes all local data of this session.
func (c *Client) Close() {
	c.mu.Lock()
	s := c.serve
	c.serve = nil
	c.mu.Unlock()
	s.stop()
	_, _ = c.run(nil, "logout")
	c.mu.Lock()
	c.session = ""
	c.mu.Unlock()
	_ = os.RemoveAll(c.dataDir)
}

// serveBroken reports whether a `bw serve` call failed for a reason other than
// a normal CLI error (unexpected answer, crashed process). Then serve is
// switched off and the caller repeats the call with one process per command.
func (c *Client) serveBroken(err error) bool {
	if err == nil {
		return false
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		return false
	}
	c.mu.Lock()
	s := c.serve
	c.serve = nil
	c.mu.Unlock()
	s.stop()
	return true
}

// Fast reports whether `bw serve` is in use.
func (c *Client) Fast() bool { return c.conn() != nil }
