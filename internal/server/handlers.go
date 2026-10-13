package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MiDniGG/bitmerger/internal/bw"
	"github.com/MiDniGG/bitmerger/internal/dedupe"
)

type stateResponse struct {
	Version   string         `json:"version"`
	BWVersion string         `json:"bwVersion"`
	BWReady   bool           `json:"bwReady"`
	Download  *downloadState `json:"download,omitempty"`
	Account   *account       `json:"account,omitempty"`
	HasPlan   bool           `json:"hasPlan"`
	LastLogin *lastLogin     `json:"lastLogin,omitempty"`
	Folders   []folderState  `json:"folders"`
	Job       *Job           `json:"job,omitempty"`
}

type folderState struct {
	dedupe.FolderInfo
	Skip bool `json:"skip"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	_, ready := bw.Installed()
	if s.newBackend != nil {
		ready = true
	}
	s.mu.Lock()
	resp := stateResponse{Version: s.Version, BWVersion: bw.Version, BWReady: ready,
		Download: s.download, Account: s.account, HasPlan: s.plan != nil && s.account != nil && s.plan.Account == s.account.Email,
		LastLogin: s.lastLogin, Folders: []folderState{}}
	if s.account != nil {
		skip := s.prefs.skipSet(s.folders)
		for _, f := range s.folders {
			resp.Folders = append(resp.Folders, folderState{FolderInfo: f, Skip: skip[f.ID]})
		}
	}
	if s.job != nil {
		j := s.job.snapshot()
		j.Result = nil // fetched separately; can be large
		resp.Job = &j
	}
	s.mu.Unlock()
	writeJSON(w, resp)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if _, ok := bw.Installed(); ok {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	s.mu.Lock()
	if s.download != nil && s.download.Running {
		s.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	d := &downloadState{Running: true}
	s.download = d
	s.mu.Unlock()
	go func() {
		_, err := bw.Download(func(done, total int64) {
			s.mu.Lock()
			d.Done, d.Total = done, total
			s.mu.Unlock()
		})
		s.mu.Lock()
		d.Running = false
		if err != nil {
			d.Error = err.Error()
		}
		s.mu.Unlock()
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req bw.LoginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "a job is running")
		return
	}
	if s.client == nil {
		c, err := s.backend()
		if err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.client = c
	}
	client := s.client
	s.mu.Unlock()

	err := client.Login(req)
	if errors.Is(err, bw.ErrTwoStep) {
		writeJSON(w, map[string]any{"twoStep": true, "message": strings.TrimPrefix(err.Error(), bw.ErrTwoStep.Error()+": ")})
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	fast := client.StartServe() == nil
	st, _ := client.Status()
	email := st.UserEmail
	if email == "" {
		email = req.Email
	}
	last := &lastLogin{Server: req.Server, Email: req.Email, ClientID: req.ClientID}
	if req.ClientID != "" {
		last.Email = ""
	}
	_ = saveJSON(s.loginFile, last)
	// The scan page lists the folders. A saved plan may come from an older
	// version or the vault may have changed since; refresh what the review
	// shows before it is opened.
	snap, _ := client.Snapshot()
	s.mu.Lock()
	s.folders, s.snap = nil, snap
	if snap != nil {
		dir := snap.Directory()
		s.folders = dedupe.Folders(snap.Items, dir)
		if s.plan != nil && s.plan.Account == email {
			dir.Skip = s.prefs.skipSet(s.folders)
			s.plan.Refresh(snap.Items, dir)
			_ = savePlan(s.planFile, s.plan)
		}
	}
	s.account = &account{Email: email, Server: st.ServerURL, Fast: fast}
	s.lastLogin = last
	s.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.mu.Lock()
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "a job is running")
		return
	}
	c := s.client
	s.client, s.account, s.folders, s.snap = nil, nil, nil, nil
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// startJob runs f in the background unless another job runs.
func (s *Server) startJob(w http.ResponseWriter, kind string, f func(c Backend, j *Job) error) {
	s.mu.Lock()
	if s.client == nil || !s.client.Unlocked() {
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "a job is running")
		return
	}
	j := newJob(kind)
	s.job = j
	c := s.client
	s.mu.Unlock()
	go func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				err = errors.New("internal error")
			}
			j.finish(err)
		}()
		err = f(c, j)
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

func syncAndRead(c Backend, j *Job) (*bw.Snapshot, error) {
	j.set(func(j *Job) { j.Phase = "sync" })
	if err := c.Sync(); err != nil {
		return nil, err
	}
	j.set(func(j *Job) { j.Phase = "read" })
	return c.Snapshot()
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Match       dedupe.Match `json:"match"`
		SkipFolders []string     `json:"skipFolders"`
		Reuse       bool         `json:"reuse"` // group the vault as last read, without reading again
	}
	_ = readJSON(r, &req)
	if req.Match != dedupe.MatchDomain {
		req.Match = dedupe.MatchHost
	}
	if req.SkipFolders != nil {
		s.mu.Lock()
		s.prefs.SkipFolders = req.SkipFolders
		_ = saveJSON(s.prefsFile, s.prefs)
		s.mu.Unlock()
	}
	s.startJob(w, "scan", func(c Backend, j *Job) error {
		s.mu.Lock()
		snap := s.snap
		s.mu.Unlock()
		if !req.Reuse || snap == nil {
			var err error
			if snap, err = syncAndRead(c, j); err != nil {
				return err
			}
		}
		j.set(func(j *Job) { j.Phase = "group" })
		dir := snap.Directory()
		folders := dedupe.Folders(snap.Items, dir)
		s.mu.Lock()
		dir.Skip = s.prefs.skipSet(folders)
		s.folders, s.snap = folders, snap
		s.mu.Unlock()
		plan := dedupe.BuildPlan(snap.Items, dir, req.Match)
		s.mu.Lock()
		if s.account != nil {
			plan.Account = s.account.Email
		}
		if old := s.plan; old != nil && old.Account == plan.Account {
			plan.CarryOver(old)
		}
		s.plan = plan
		s.mu.Unlock()
		return savePlan(s.planFile, plan)
	})
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		plan := s.plan
		ok := plan != nil && s.account != nil && plan.Account == s.account.Email
		s.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "no plan")
			return
		}
		writeJSON(w, plan)
	case http.MethodPut:
		var posted dedupe.Plan
		if err := readJSON(r, &posted); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		if s.plan == nil {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "no plan")
			return
		}
		if s.job != nil && s.job.Running && s.job.Kind == "apply" {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "merge is running")
			return
		}
		s.plan.ApplyDecisions(&posted)
		err := savePlan(s.planFile, s.plan)
		s.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or PUT")
	}
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.Lock()
	c, plan := s.client, s.plan
	s.mu.Unlock()
	if c == nil || plan == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	known := false
	for _, g := range plan.Groups {
		for _, e := range g.Entries {
			if e.ID == id {
				known = true
			}
		}
	}
	if !known {
		writeError(w, http.StatusNotFound, "unknown item")
		return
	}
	// The vault as last read is in memory anyway; answering from it is
	// instant, while the CLI needs seconds per item.
	if it, ok := s.cachedItem(id); ok {
		writeJSON(w, map[string]string{"password": it.Password()})
		return
	}
	pw, err := c.Password(id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]string{"password": pw})
}

// cachedItem finds an item in the vault as last read (kept in memory only).
func (s *Server) cachedItem(id string) (dedupe.Item, bool) {
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()
	if snap == nil {
		return nil, false
	}
	for _, it := range snap.Items {
		if it.ID() == id {
			return it, true
		}
	}
	return nil, false
}

func (s *Server) currentPlan() *dedupe.Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan
}

func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.startJob(w, "dryrun", func(c Backend, j *Job) error {
		plan := s.currentPlan()
		if plan == nil {
			return errors.New("no plan")
		}
		snap, err := syncAndRead(c, j)
		if err != nil {
			return err
		}
		steps := dedupe.BuildSteps(plan, snap.Items)
		j.set(func(j *Job) { j.Result = steps })
		return nil
	})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.startJob(w, "apply", func(c Backend, j *Job) error {
		// the vault changes now; the next scan must read it again
		s.mu.Lock()
		s.snap = nil
		s.mu.Unlock()
		plan := s.currentPlan()
		if plan == nil {
			return errors.New("no plan")
		}
		snap, err := syncAndRead(c, j)
		if err != nil {
			return err
		}
		var ready []*dedupe.Step
		for _, st := range dedupe.BuildSteps(plan, snap.Items) {
			if st.Ready() {
				ready = append(ready, st)
			}
		}
		c.StopServe()
		defer func() { _ = c.StartServe() }()
		j.set(func(j *Job) { j.Phase, j.Total = "merge", len(ready) })
		for _, st := range ready {
			if j.cancelled() {
				break
			}
			j.set(func(j *Job) { j.Current = st.Title })
			err := dedupe.Apply(c, st)
			entry := LogEntry{No: st.No, Title: st.Title, OK: err == nil}
			if err != nil {
				entry.Error = err.Error()
			}
			j.set(func(j *Job) { j.Done++; j.Log = append(j.Log, entry) })
		}
		j.set(func(j *Job) { j.Phase, j.Current = "sync", "" })
		return c.Sync()
	})
}

func (s *Server) handleSelftest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.startJob(w, "selftest", func(c Backend, j *Job) error {
		j.set(func(j *Job) { j.Phase = "selftest" })
		c.StopServe()
		defer func() { _ = c.StartServe() }()
		res := dedupe.Selftest(c, 1200*time.Millisecond)
		j.set(func(j *Job) { j.Result = res })
		return c.Sync()
	})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	if j != nil && j.Running {
		j.cancel()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleJob returns the full job including its result.
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	if j == nil {
		writeError(w, http.StatusNotFound, "no job")
		return
	}
	snap := j.snapshot()
	writeJSON(w, snap)
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.Shutdown()
	}()
}

func (s *Server) backend() (Backend, error) {
	if s.newBackend != nil {
		return s.newBackend()
	}
	bin, ok := bw.Installed()
	if !ok {
		return nil, errors.New("Bitwarden CLI not downloaded yet")
	}
	return bw.NewClient(bin)
}

// itemDetails is what the review page shows when a row is expanded. It is
// read live from the vault and never stored. Hidden custom fields stay hidden.
type itemDetails struct {
	Username        string        `json:"username"`
	URIs            []detailURI   `json:"uris"`
	Created         string        `json:"created"`
	Revised         string        `json:"revised"`
	PasswordRevised string        `json:"passwordRevised"`
	Notes           string        `json:"notes"`
	Fields          []detailField `json:"fields"`
	History         []string      `json:"history"` // dates only
	Attachments     []detailFile  `json:"attachments"`
	TOTP            bool          `json:"totp"`
	Passkey         bool          `json:"passkey"`
	Favorite        bool          `json:"favorite"`
	Reprompt        bool          `json:"reprompt"`
}

type detailURI struct {
	URI   string `json:"uri"`
	Match any    `json:"match"`
}

type detailField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Hidden bool   `json:"hidden"`
}

type detailFile struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

func (s *Server) handleDetails(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.Lock()
	c, plan := s.client, s.plan
	s.mu.Unlock()
	if c == nil || plan == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	known := false
	for _, g := range plan.Groups {
		for _, e := range g.Entries {
			if e.ID == id {
				known = true
			}
		}
	}
	if !known {
		writeError(w, http.StatusNotFound, "unknown item")
		return
	}
	it, ok := s.cachedItem(id)
	if !ok {
		var err error
		if it, err = c.Get(id); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	str := func(v any) string { s, _ := v.(string); return s }
	d := itemDetails{
		Username: it.Username(), Created: str(it["creationDate"]), Revised: it.RevisionDate(),
		PasswordRevised: str(it.Login()["passwordRevisionDate"]), Notes: it.Notes(),
		TOTP: it.TOTP() != "", Passkey: it.HasPasskey(), Favorite: it.Favorite(),
		URIs: []detailURI{}, Fields: []detailField{}, History: []string{}, Attachments: []detailFile{},
	}
	if rp, ok := it["reprompt"].(float64); ok && rp > 0 {
		d.Reprompt = true
	}
	if l, ok := it.Login()["uris"].([]any); ok {
		for _, u := range l {
			if m, ok := u.(map[string]any); ok && str(m["uri"]) != "" {
				d.URIs = append(d.URIs, detailURI{URI: str(m["uri"]), Match: m["match"]})
			}
		}
	}
	for _, f := range it.Fields() {
		hidden := false
		if t, ok := f["type"].(float64); ok && t == 1 {
			hidden = true
		}
		v := str(f["value"])
		if hidden {
			v = ""
		}
		d.Fields = append(d.Fields, detailField{Name: str(f["name"]), Value: v, Hidden: hidden})
	}
	for _, h := range it.History() {
		d.History = append(d.History, h.LastUsedDate)
	}
	for _, a := range it.Attachments() {
		d.Attachments = append(d.Attachments, detailFile{Name: str(a["fileName"]), Size: str(a["sizeName"])})
	}
	writeJSON(w, d)
}
