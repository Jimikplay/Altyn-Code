package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/audits", s.createFromZIP)
	mux.HandleFunc("POST /v1/audits/demo", s.createDemo)
	mux.HandleFunc("POST /v1/audits/github", s.createFromGitHub)
	mux.HandleFunc("GET /v1/audits/{id}", s.getAudit)
	mux.HandleFunc("POST /v1/audits/{id}/start", s.startAudit)
	mux.HandleFunc("PUT /v1/audits/{id}/files", s.editFile)
	mux.HandleFunc("POST /v1/audits/{id}/tests", s.testAudit)
	mux.HandleFunc("POST /v1/audits/{id}/notes", s.addNote)
	mux.HandleFunc("POST /v1/audits/{id}/finish", s.finishAudit)
	mux.HandleFunc("GET /v1/audits/{id}/report", s.report)
	mux.Handle("GET /", webHandler())
	mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("JSON must contain exactly one object")
	}
	return nil
}

func (s *Server) reserve(w http.ResponseWriter) bool {
	select {
	case s.preparing <- struct{}{}:
		return true
	default:
		fail(w, http.StatusTooManyRequests, "an audit is already being prepared")
		return false
	}
}

func (s *Server) createFromZIP(w http.ResponseWriter, r *http.Request) {
	if !s.reserve(w) {
		return
	}
	defer func() { <-s.preparing }()
	if r.Header.Get("Content-Type") != "application/zip" {
		fail(w, http.StatusUnsupportedMediaType, "Content-Type must be application/zip")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxArchiveBytes)
	data, err := readLimited(r.Body, MaxArchiveBytes)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "archive exceeds 8 MiB")
		return
	}
	files, err := LoadZIP(data)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.create(w, r, files, "")
}

func (s *Server) createFromGitHub(w http.ResponseWriter, r *http.Request) {
	if !s.reserve(w) {
		return
	}
	defer func() { <-s.preparing }()
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, 512, &input); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	files, commit, err := FetchGitHub(ctx, input.URL, s.client)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.create(w, r, files, commit)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, files Files, commit string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()
	prepared, err := Prepare(ctx, files, s.runner)
	if err != nil {
		if errors.Is(err, ErrBaseline) || errors.Is(err, ErrNotEnoughMutations) {
			fail(w, http.StatusUnprocessableEntity, err.Error())
		} else {
			fail(w, http.StatusServiceUnavailable, fmt.Sprintf("could not prepare audit in sandbox: %v", err))
		}
		return
	}
	audit, err := s.newSession(prepared, commit)
	if err != nil {
		fail(w, http.StatusTooManyRequests, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"id": audit.id, "auditorToken": audit.auditorToken,
		"studentToken": audit.studentToken, "sourceCommit": commit,
	})
}

func (s *Server) authenticated(w http.ResponseWriter, r *http.Request) (*auditSession, string, bool) {
	audit, role, status := s.authorize(r.PathValue("id"), r.Header.Get("Authorization"))
	if status != 0 {
		fail(w, status, http.StatusText(status))
		return nil, "", false
	}
	s.ensureDeadline(audit)
	return audit, role, true
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	view := map[string]any{
		"id": audit.id, "status": audit.status, "revision": audit.revision,
		"deadline": audit.deadline, "files": filesAsText(audit.edited),
		"lastRun": audit.lastRun, "notes": audit.notes,
	}
	if role == "auditor" {
		view["mutations"] = audit.project.Mutations
		view["original"] = filesAsText(audit.project.Original)
		view["sourceCommit"] = audit.sourceCommit
		view["events"] = audit.events
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) startAudit(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if role != "auditor" {
		fail(w, http.StatusForbidden, "auditor token required")
		return
	}
	if err := s.start(audit); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	audit.mu.Lock()
	deadline := audit.deadline
	audit.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "active", "deadline": deadline})
}

func (s *Server) editFile(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if role != "student" {
		fail(w, http.StatusForbidden, "student token required")
		return
	}
	var input struct {
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := decodeJSON(w, r, 2*maxFileBytes+1024, &input); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(input.Source) > maxFileBytes || input.Path == "" || !strings.HasSuffix(input.Path, ".go") || strings.HasSuffix(input.Path, "_test.go") || strings.HasPrefix(input.Path, "vendor/") {
		fail(w, http.StatusForbidden, "only existing non-test Go source files up to 256 KiB may be edited")
		return
	}
	audit.mu.Lock()
	if audit.status == "active" && !s.now().Before(audit.deadline) {
		audit.mu.Unlock()
		s.ensureDeadline(audit)
		fail(w, http.StatusConflict, "defense deadline has passed")
		return
	}
	if audit.status != "active" {
		audit.mu.Unlock()
		fail(w, http.StatusConflict, errConflict.Error())
		return
	}
	if _, exists := audit.edited[input.Path]; !exists {
		audit.mu.Unlock()
		fail(w, http.StatusNotFound, "unknown source file")
		return
	}
	audit.edited[input.Path] = []byte(input.Source)
	audit.revision++
	revision := audit.revision
	audit.lastRun = nil
	audit.record(Event{At: s.now(), Type: "edited", Path: input.Path, Revision: revision})
	audit.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (s *Server) testAudit(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if role != "student" {
		fail(w, http.StatusForbidden, "student token required")
		return
	}
	result, err := s.runTests(r.Context(), audit)
	if err != nil {
		if errors.Is(err, errConflict) || strings.Contains(err.Error(), "code changed or defense ended") {
			fail(w, http.StatusConflict, err.Error())
		} else {
			fail(w, http.StatusServiceUnavailable, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if role != "student" {
		fail(w, http.StatusForbidden, "student token required")
		return
	}
	var input struct {
		Text string `json:"text"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	if input.Text == "" || len(input.Text) > 1000 {
		fail(w, http.StatusBadRequest, "note must contain 1 to 1000 bytes")
		return
	}
	if input.Line < 0 || (input.Path == "" && input.Line != 0) {
		fail(w, http.StatusBadRequest, "note location must have a source file and a positive line")
		return
	}
	audit.mu.Lock()
	if audit.status == "active" && !s.now().Before(audit.deadline) {
		audit.mu.Unlock()
		s.ensureDeadline(audit)
		fail(w, http.StatusConflict, "defense deadline has passed")
		return
	}
	if audit.status != "active" || len(audit.notes) >= 20 {
		audit.mu.Unlock()
		fail(w, http.StatusConflict, "defense ended or note limit reached")
		return
	}
	if input.Path != "" {
		code, exists := audit.edited[input.Path]
		if !exists || input.Line < 1 || input.Line > strings.Count(string(code), "\n")+1 {
			audit.mu.Unlock()
			fail(w, http.StatusBadRequest, "note location is not in the project")
			return
		}
	}
	audit.notes = append(audit.notes, Note{Text: input.Text, Path: input.Path, Line: input.Line, At: s.now()})
	audit.record(Event{At: s.now(), Type: "note", Path: input.Path, Revision: audit.revision})
	audit.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (s *Server) finishAudit(w http.ResponseWriter, r *http.Request) {
	audit, _, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if err := s.finish(audit, "submitted"); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "finished"})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	audit, role, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	if role != "auditor" {
		fail(w, http.StatusForbidden, "auditor token required")
		return
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if audit.status != "finished" {
		fail(w, http.StatusConflict, "report is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": audit.id, "status": audit.status, "reason": audit.reason,
		"started": audit.started, "deadline": audit.deadline, "ended": audit.ended,
		"sourceCommit": audit.sourceCommit, "mutations": audit.project.Mutations,
		"tests": audit.lastRun, "testError": audit.runError,
		"notes": audit.notes, "changes": audit.changes(), "events": audit.events,
		"interpretation": "Passing tests show observed behavior only; an auditor judges understanding.",
	})
}
