// Package server is BitMerger's local web interface.
//
// It listens on 127.0.0.1 only. A random token from the start URL is turned
// into a cookie; every API call needs that cookie, a custom header (against
// cross-site requests) and the exact Host (against DNS rebinding).
//
// Long work (download, scan, dry run, merge, selftest) runs as a background
// job in this process, independent of the browser. Closing the tab does not
// stop a merge; reopening the page shows its progress.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MiDniGG/bitmerger/internal/bw"
	"github.com/MiDniGG/bitmerger/internal/dedupe"
)

//go:embed web
var webFiles embed.FS

const cookieName = "bitmerger"

// IdleTimeout ends the program when no page has asked anything for this long
// and no job runs. The session is logged out and wiped.
const IdleTimeout = 20 * time.Minute

// Server holds the whole application state.
type Server struct {
	Version string
	token   string
	addr    string
	quit    chan struct{}

	mu         sync.Mutex
	client     Backend
	newBackend func() (Backend, error)
	planFile   string
	loginFile  string
	lastLogin  *lastLogin
	account    *account
	plan       *dedupe.Plan
	job        *Job
	download   *downloadState
	lastSeen   time.Time
}

type account struct {
	Email  string `json:"email"`
	Server string `json:"server"`
	Fast   bool   `json:"fast"` // bw serve running
}

type downloadState struct {
	Running bool   `json:"running"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
}

// New creates a server bound to a free port on 127.0.0.1.
// Backend is the password manager behind the UI: the Bitwarden CLI, or the demo.
type Backend interface {
	dedupe.Vault
	Login(r bw.LoginRequest) error
	StartServe() error
	StopServe()
	Status() (bw.Status, error)
	Unlocked() bool
	Sync() error
	Snapshot() (*bw.Snapshot, error)
	Password(id string) (string, error)
	Close()
}

// New creates a server bound to a free port on 127.0.0.1. newBackend is nil
// for the real Bitwarden CLI.
func New(version string, newBackend func() (Backend, error)) (*Server, net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, nil, err
	}
	s := &Server{Version: version, token: hex.EncodeToString(b), addr: l.Addr().String(),
		quit: make(chan struct{}), lastSeen: time.Now(), newBackend: newBackend}
	s.planFile, s.loginFile = "plan.json", "login.json"
	if newBackend != nil {
		s.planFile, s.loginFile = "plan-demo.json", "login-demo.json"
	}
	s.plan = loadPlan(s.planFile)
	s.lastLogin = loadLastLogin(s.loginFile)
	return s, l, nil
}

// StartURL is the URL the browser opens; it carries the one-time token.
func (s *Server) StartURL() string { return fmt.Sprintf("http://%s/?t=%s", s.addr, s.token) }

// Done is closed when the user quits or the idle timeout hits.
func (s *Server) Done() <-chan struct{} { return s.quit }

// Serve runs the HTTP server until Done.
func (s *Server) Serve(l net.Listener) error {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		if !s.cookieOK(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("BitMerger: please open the link printed in the terminal window."))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		w.Header().Set("X-Frame-Options", "DENY")
		files.ServeHTTP(w, r)
	})
	api := func(path string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !s.hostOK(r) || !s.cookieOK(r) || r.Header.Get("X-BitMerger") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			s.mu.Lock()
			s.lastSeen = time.Now()
			s.mu.Unlock()
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		})
	}
	api("/api/state", s.handleState)
	api("/api/setup", s.handleSetup)
	api("/api/login", s.handleLogin)
	api("/api/logout", s.handleLogout)
	api("/api/scan", s.handleScan)
	api("/api/plan", s.handlePlan)
	api("/api/password", s.handlePassword)
	api("/api/details", s.handleDetails)
	api("/api/dryrun", s.handleDryRun)
	api("/api/apply", s.handleApply)
	api("/api/selftest", s.handleSelftest)
	api("/api/cancel", s.handleCancel)
	api("/api/job", s.handleJob)
	api("/api/quit", s.handleQuit)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go s.watchIdle()
	go func() {
		<-s.quit
		_ = srv.Close()
	}()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) hostOK(r *http.Request) bool { return r.Host == s.addr }

func (s *Server) cookieOK(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

func (s *Server) watchIdle() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			s.mu.Lock()
			idle := time.Since(s.lastSeen) > IdleTimeout && (s.job == nil || !s.job.Running)
			s.mu.Unlock()
			if idle {
				s.Shutdown()
				return
			}
		}
	}
}

// Shutdown waits for a running job to stop at a safe point, logs out and
// removes all local session data.
func (s *Server) Shutdown() {
	s.mu.Lock()
	job := s.job
	s.mu.Unlock()
	if job != nil && job.Running {
		job.cancel()
		job.wait()
	}
	s.mu.Lock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	select {
	case <-s.quit:
	default:
		close(s.quit)
	}
	s.mu.Unlock()
}

// JobRunning reports whether a job is active (used by the signal handler).
func (s *Server) JobRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job != nil && s.job.Running
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 8<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// planPath keeps the plan between runs. It never contains secrets.
func planPath(name string) string {
	base, err := bw.BaseDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, name)
}

func loadPlan(name string) *dedupe.Plan {
	p := planPath(name)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var plan dedupe.Plan
	if json.Unmarshal(b, &plan) != nil {
		return nil
	}
	return &plan
}

func savePlan(name string, plan *dedupe.Plan) error {
	return saveJSON(name, plan)
}

// lastLogin is what the login form is filled with next time. Never secrets:
// no password, no API key secret, no two-step code.
type lastLogin struct {
	Server   string `json:"server"`
	Email    string `json:"email,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

func loadLastLogin(name string) *lastLogin {
	p := planPath(name)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var l lastLogin
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	return &l
}

func saveJSON(name string, v any) error {
	p := planPath(name)
	if p == "" {
		return errors.New("no cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
