package bw

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MiDniGG/bitmerger/internal/dedupe"
)

// Starting a fresh CLI process per command costs about a second. `bw serve`
// keeps one process warm and answers in milliseconds, which matters when a
// merge needs several edits per group. It listens on 127.0.0.1 only and,
// by default, rejects requests that carry a browser Origin header.

type serveConn struct {
	cmd  *exec.Cmd
	base string
	http *http.Client
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// StartServe launches `bw serve` for the unlocked session. On failure the
// client keeps working with one process per command.
func (c *Client) StartServe() error {
	if !c.Unlocked() {
		return errors.New("vault is locked")
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	cmd := exec.Command(c.bin, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	c.mu.Lock()
	cmd.Env = append(os.Environ(), "BITWARDENCLI_APPDATA_DIR="+c.dataDir, "BW_NOINTERACTION=true", "BW_SESSION="+c.session)
	c.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	conn := &serveConn{cmd: cmd, base: fmt.Sprintf("http://127.0.0.1:%d", port), http: &http.Client{Timeout: 5 * time.Minute}}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var st Status
		if err := conn.call("GET", "/status", nil, &st); err == nil {
			c.mu.Lock()
			c.serve = conn
			c.mu.Unlock()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return errors.New("bw serve did not start")
}

// StopServe ends `bw serve`; later calls start one CLI process each.
//
// Merging must not go through `bw serve`: on a real server its edits did not
// record the previous password in the history (selftest case 2 failed), while
// one process per edit does. Two processes writing the same data folder could
// also overwrite each other's cache, so serve is stopped for the whole merge.
func (c *Client) StopServe() {
	c.mu.Lock()
	s := c.serve
	c.serve = nil
	c.mu.Unlock()
	s.stop()
}

func (s *serveConn) stop() {
	if s != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
}

// call sends a request and unpacks {"success":..., "data":..., "message":...}.
func (s *serveConn) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("bw serve: %s", strings.TrimSpace(string(raw)))
	}
	if !env.Success {
		return &CLIError{Command: path, Message: env.Message}
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	// list endpoints wrap the array as {"object":"list","data":[...]}
	var list struct {
		Object string          `json:"object"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(env.Data, &list) == nil && list.Object == "list" {
		return json.Unmarshal(list.Data, out)
	}
	return json.Unmarshal(env.Data, out)
}

func (c *Client) conn() *serveConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serve
}

func (s *serveConn) snapshot() (*Snapshot, error) {
	snap := &Snapshot{}
	if err := s.call("GET", "/list/object/items", nil, &snap.Items); err != nil {
		return nil, err
	}
	if err := s.call("GET", "/list/object/organizations", nil, &snap.Orgs); err != nil {
		return nil, err
	}
	if err := s.call("GET", "/list/object/collections", nil, &snap.Collections); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *serveConn) get(id string) (dedupe.Item, error) {
	var it dedupe.Item
	return it, s.call("GET", "/object/item/"+id, nil, &it)
}

func (s *serveConn) edit(it dedupe.Item) (dedupe.Item, error) {
	var out dedupe.Item
	return out, s.call("PUT", "/object/item/"+it.ID(), it, &out)
}

func (s *serveConn) create(it dedupe.Item) (dedupe.Item, error) {
	var out dedupe.Item
	return out, s.call("POST", "/object/item", it, &out)
}

func (s *serveConn) del(id string) error {
	return s.call("DELETE", "/object/item/"+id, nil, nil)
}

func (s *serveConn) sync() error {
	return s.call("POST", "/sync", nil, nil)
}
