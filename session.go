package audit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const defenseDuration = 10 * time.Minute

var errConflict = errors.New("audit is not active")

type Note struct {
	Text string    `json:"text"`
	Path string    `json:"path,omitempty"`
	Line int       `json:"line,omitempty"`
	At   time.Time `json:"at"`
}

type Event struct {
	At       time.Time `json:"at"`
	Type     string    `json:"type"`
	Path     string    `json:"path,omitempty"`
	Revision int       `json:"revision"`
	Passed   *bool     `json:"passed,omitempty"`
}

type auditSession struct {
	mu           sync.Mutex
	id           string
	auditorToken string
	studentToken string
	project      *Prepared
	sourceCommit string
	status       string
	created      time.Time
	started      time.Time
	deadline     time.Time
	ended        time.Time
	reason       string
	revision     int
	running      bool
	timer        *time.Timer
	edited       Files
	lastRun      *RunResult
	runError     string
	notes        []Note
	events       []Event
}

// record is called only while holding the session mutex. No source text or
// bearer credentials are stored in the activity history.
func (audit *auditSession) record(event Event) {
	audit.events = append(audit.events, event)
	if len(audit.events) > 100 {
		audit.events = audit.events[len(audit.events)-100:]
	}
}

type Server struct {
	mu        sync.Mutex
	sessions  map[string]*auditSession
	runner    Runner
	now       func() time.Time
	client    *http.Client
	preparing chan struct{}
}

func NewServer(runner Runner) *Server {
	return &Server{
		sessions: make(map[string]*auditSession), runner: &limitedRunner{inner: runner, slots: make(chan struct{}, 2)},
		now: time.Now, preparing: make(chan struct{}, 1),
	}
}

func secret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func (s *Server) newSession(p *Prepared, commit string) (*auditSession, error) {
	id, err := secret()
	if err != nil {
		return nil, err
	}
	auditor, err := secret()
	if err != nil {
		return nil, err
	}
	student, err := secret()
	if err != nil {
		return nil, err
	}
	audit := &auditSession{
		id: id, auditorToken: auditor, studentToken: student,
		project: p, sourceCommit: commit, edited: p.Modified.Clone(),
		created: s.now(), status: "prepared",
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.sessions {
		existing.mu.Lock()
		old := existing.status == "finished" && s.now().Sub(existing.ended) > time.Hour || existing.status == "prepared" && s.now().Sub(existing.created) > time.Hour
		existing.mu.Unlock()
		if old {
			delete(s.sessions, id)
		}
	}
	if len(s.sessions) >= 20 {
		return nil, errors.New("too many active audit sessions")
	}
	s.sessions[id] = audit
	return audit, nil
}

func (s *Server) authorize(id, header string) (*auditSession, string, int) {
	s.mu.Lock()
	audit := s.sessions[id]
	s.mu.Unlock()
	if audit == nil {
		return nil, "", http.StatusNotFound
	}
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(token) != 64 {
		return nil, "", http.StatusUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(audit.auditorToken)) == 1 {
		return audit, "auditor", 0
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(audit.studentToken)) == 1 {
		return audit, "student", 0
	}
	return nil, "", http.StatusUnauthorized
}

func (s *Server) ensureDeadline(audit *auditSession) {
	audit.mu.Lock()
	due := audit.status == "active" && !s.now().Before(audit.deadline)
	audit.mu.Unlock()
	if due {
		_ = s.finish(audit, "deadline")
	}
}

func (s *Server) start(audit *auditSession) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if audit.status != "prepared" {
		return errConflict
	}
	audit.status = "active"
	audit.started = s.now()
	audit.deadline = audit.started.Add(defenseDuration)
	audit.record(Event{At: audit.started, Type: "started", Revision: audit.revision})
	audit.timer = time.AfterFunc(defenseDuration, func() { _ = s.finish(audit, "deadline") })
	return nil
}

func (s *Server) finish(audit *auditSession, reason string) error {
	audit.mu.Lock()
	if audit.status != "active" {
		audit.mu.Unlock()
		return errConflict
	}
	if reason == "deadline" && s.now().Before(audit.deadline) {
		audit.mu.Unlock()
		return errConflict
	}
	audit.status = "finishing"
	audit.reason = reason
	audit.ended = s.now()
	if audit.timer != nil {
		audit.timer.Stop()
	}
	files := audit.edited.Clone()
	audit.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	result, err := s.runner.Run(ctx, files)
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if err != nil {
		audit.runError = fmt.Sprintf("final sandbox test failed: %v", err)
	} else {
		audit.lastRun = &result
		audit.runError = ""
	}
	audit.status = "finished"
	audit.record(Event{At: s.now(), Type: "finished", Revision: audit.revision})
	return nil
}

func (s *Server) runTests(ctx context.Context, audit *auditSession) (RunResult, error) {
	s.ensureDeadline(audit)
	audit.mu.Lock()
	if audit.status == "active" && !s.now().Before(audit.deadline) {
		audit.mu.Unlock()
		s.ensureDeadline(audit)
		return RunResult{}, errConflict
	}
	if audit.status != "active" || audit.running {
		audit.mu.Unlock()
		return RunResult{}, errConflict
	}
	audit.running = true
	revision := audit.revision
	files := audit.edited.Clone()
	audit.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	result, err := s.runner.Run(ctx, files)
	s.ensureDeadline(audit)
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.running = false
	if audit.status == "active" && !s.now().Before(audit.deadline) {
		// The timer callback may still be pending. Do not publish a late run.
		return RunResult{}, errConflict
	}
	if audit.status != "active" || revision != audit.revision {
		return RunResult{}, errors.New("code changed or defense ended during test run")
	}
	if err != nil {
		return RunResult{}, err
	}
	audit.lastRun = &result
	audit.runError = ""
	passed := result.Passed
	audit.record(Event{At: s.now(), Type: "tests", Revision: revision, Passed: &passed})
	return result, nil
}

func filesAsText(files Files) map[string]string {
	result := make(map[string]string, len(files))
	for name, contents := range files {
		result[name] = string(contents)
	}
	return result
}

type FileChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func (audit *auditSession) changes() []FileChange {
	changes := []FileChange{}
	for name, original := range audit.project.Original {
		if string(original) != string(audit.edited[name]) {
			changes = append(changes, FileChange{Path: name, Before: string(original), After: string(audit.edited[name])})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}
