package server

import (
	"sync"
	"time"
)

// Job is one piece of background work. Only one runs at a time.
type Job struct {
	mu *sync.Mutex

	Kind      string     `json:"kind"` // scan, dryrun, apply, selftest
	Running   bool       `json:"running"`
	Phase     string     `json:"phase"` // sync, read, merge ... (translated by the UI)
	Done      int        `json:"done"`
	Total     int        `json:"total"`
	Current   string     `json:"current"`
	Started   time.Time  `json:"started"`
	Finished  *time.Time `json:"finished,omitempty"`
	Error     string     `json:"error,omitempty"`
	Cancelled bool       `json:"cancelled"`
	Log       []LogEntry `json:"log"`
	Result    any        `json:"result,omitempty"`

	cancelRequested bool
	done            chan struct{}
}

// LogEntry is the outcome of one group during a merge.
type LogEntry struct {
	No    int    `json:"no"`
	Title string `json:"title"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func newJob(kind string) *Job {
	return &Job{mu: &sync.Mutex{}, Kind: kind, Running: true, Started: time.Now(), Log: []LogEntry{}, done: make(chan struct{})}
}

func (j *Job) set(f func(j *Job)) {
	j.mu.Lock()
	f(j)
	j.mu.Unlock()
}

func (j *Job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := Job{mu: &sync.Mutex{}, Kind: j.Kind, Running: j.Running, Phase: j.Phase, Done: j.Done, Total: j.Total,
		Current: j.Current, Started: j.Started, Finished: j.Finished, Error: j.Error,
		Cancelled: j.Cancelled, Result: j.Result}
	c.Log = append([]LogEntry{}, j.Log...)
	return c
}

func (j *Job) cancel() {
	j.mu.Lock()
	j.cancelRequested = true
	j.mu.Unlock()
}

func (j *Job) cancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelRequested
}

func (j *Job) finish(err error) {
	j.mu.Lock()
	now := time.Now()
	j.Running, j.Finished = false, &now
	if err != nil {
		j.Error = err.Error()
	}
	j.Cancelled = j.cancelRequested
	j.mu.Unlock()
	close(j.done)
}

func (j *Job) wait() { <-j.done }
