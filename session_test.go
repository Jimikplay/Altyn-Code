package audit

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestSessionsHaveBoundedCapacityAndExpire(t *testing.T) {
	clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	server := NewServer(fixtureRunner{})
	server.now = func() time.Time { return clock }
	prepared := &Prepared{Original: fixtureFiles(), Modified: fixtureFiles()}
	for range 20 {
		if _, err := server.newSession(prepared, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.newSession(prepared, ""); err == nil {
		t.Fatal("allowed more than 20 live sessions")
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := server.newSession(prepared, ""); err != nil || len(server.sessions) != 1 {
		t.Fatalf("did not evict old prepared sessions: %d %v", len(server.sessions), err)
	}
}

type blockedRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r blockedRunner) Run(ctx context.Context, _ Files) (RunResult, error) {
	close(r.entered)
	select {
	case <-r.release:
		return RunResult{Passed: true}, nil
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
}

func TestInFlightTestsCannotCertifyNewerCode(t *testing.T) {
	server := NewServer(fixtureRunner{})
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample})
	created := decodeResponse(t, request(t, server, "POST", "/v1/audits", "", "application/zip", archive))
	base := "/v1/audits/" + created["id"].(string)
	student := created["studentToken"].(string)
	request(t, server, "POST", base+"/start", created["auditorToken"].(string), "", nil)
	runner := blockedRunner{entered: make(chan struct{}), release: make(chan struct{})}
	server.runner = runner
	done := make(chan int, 1)
	go func() {
		response := request(t, server, "POST", base+"/tests", student, "", nil)
		done <- response.Code
	}()
	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("test run did not start")
	}
	changed := request(t, server, "PUT", base+"/files", student, "application/json", []byte(`{"path":"sample.go","source":"package sample"}`))
	if changed.Code != http.StatusOK {
		t.Fatalf("could not edit during test run: %d", changed.Code)
	}
	close(runner.release)
	select {
	case status := <-done:
		if status != http.StatusConflict {
			t.Fatalf("stale test result was accepted: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test run did not finish")
	}
	view := decodeResponse(t, request(t, server, "GET", base, student, "", nil))
	if view["lastRun"] != nil {
		t.Fatal("stale passing result was published")
	}
}
