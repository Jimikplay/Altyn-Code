package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, handler http.Handler, method, endpoint, token, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, endpoint, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}

func decodeResponse(t *testing.T, r *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON (%d): %s", r.Code, r.Body.String())
	}
	return body
}

func TestAuditAPISeparatesRolesAndReportsEvidence(t *testing.T) {
	clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	server := NewServer(fixtureRunner{})
	server.now = func() time.Time { return clock }
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample, "sample_test.go": "package sample\n"})
	created := request(t, server, "POST", "/v1/audits", "", "application/zip", archive)
	if created.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", created.Code, created.Body.String())
	}
	access := decodeResponse(t, created)
	id, auditor, student := access["id"].(string), access["auditorToken"].(string), access["studentToken"].(string)
	base := "/v1/audits/" + id
	if id == "" || auditor == "" || student == "" || auditor == student {
		t.Fatal("audit credentials are missing or identical")
	}
	if request(t, server, "GET", base, "", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("anonymous user can read audit")
	}
	view := request(t, server, "GET", base, student, "", nil)
	if view.Code != http.StatusOK || strings.Contains(view.Body.String(), "mutations") || strings.Contains(view.Body.String(), "original") {
		t.Fatalf("student can access mutation map: %s", view.Body.String())
	}
	if request(t, server, "POST", base+"/start", student, "", nil).Code != http.StatusForbidden {
		t.Fatal("student started audit")
	}
	started := request(t, server, "POST", base+"/start", auditor, "", nil)
	if started.Code != http.StatusOK {
		t.Fatalf("start: %s", started.Body.String())
	}
	if deadline := decodeResponse(t, started)["deadline"].(string); deadline != clock.Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected deadline %s", deadline)
	}
	if request(t, server, "PUT", base+"/files", student, "application/json", []byte(`{"path":"sample_test.go","source":"package sample"}`)).Code != http.StatusForbidden {
		t.Fatal("student can change test definitions")
	}
	if request(t, server, "PUT", base+"/files", student, "application/json", []byte(`{"path":"sample.go","source":`+stringMustJSON(t, sample)+`}`)).Code != http.StatusOK {
		t.Fatal("student cannot repair source")
	}
	tested := request(t, server, "POST", base+"/tests", student, "", nil)
	if tested.Code != http.StatusOK || decodeResponse(t, tested)["passed"] != true {
		t.Fatalf("repaired tests failed: %d %s", tested.Code, tested.Body.String())
	}
	if request(t, server, "POST", base+"/notes", student, "application/json", []byte(`{"path":"sample.go","line":2,"text":"Исправил границу и расчёт"}`)).Code != http.StatusOK {
		t.Fatal("cannot submit explanation")
	}
	auditorView := request(t, server, "GET", base, auditor, "", nil)
	if !strings.Contains(auditorView.Body.String(), `"events"`) {
		t.Fatal("auditor has no event history")
	}
	if request(t, server, "GET", base+"/report", student, "", nil).Code != http.StatusForbidden {
		t.Fatal("student accessed auditor report")
	}
	ended := request(t, server, "POST", base+"/finish", student, "", nil)
	if ended.Code != http.StatusOK {
		t.Fatalf("finish: %s", ended.Body.String())
	}
	report := request(t, server, "GET", base+"/report", auditor, "", nil)
	if report.Code != http.StatusOK || !strings.Contains(report.Body.String(), "TestBoundary") || !strings.Contains(report.Body.String(), "Исправил") {
		t.Fatalf("report lacks evidence: %d %s", report.Code, report.Body.String())
	}
	if !strings.Contains(report.Body.String(), `"line":2`) || !strings.Contains(report.Body.String(), `"events"`) {
		t.Fatal("report lost the student's located explanation or audit history")
	}
	if request(t, server, "PUT", base+"/files", student, "application/json", []byte(`{"path":"sample.go","source":"package sample"}`)).Code != http.StatusConflict {
		t.Fatal("editing after finish was accepted")
	}
}

func TestAuditCreationFailsClosedWithoutDocker(t *testing.T) {
	server := NewServer(DockerRunner{Docker: "missing-docker-binary-for-test"})
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample})
	result := request(t, server, "POST", "/v1/audits", "", "application/zip", archive)
	if result.Code != http.StatusServiceUnavailable || len(server.sessions) != 0 {
		t.Fatalf("created a session without sandbox: %d %s", result.Code, result.Body.String())
	}
}

func TestGitHubImportEndpointPinsCommitAndRejectsBadRequests(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	server := NewServer(fixtureRunner{})
	archive := zipFixture(t, map[string]string{"repo-0123456/go.mod": "module example.com/sample\n", "repo-0123456/sample.go": sample})
	server.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload []byte
		if req.URL.Host == "api.github.com" {
			payload = []byte(`[{"sha":"` + sha + `"}]`)
		} else if req.URL.Host == "codeload.github.com" {
			payload = archive
		} else {
			t.Fatalf("unexpected host %s", req.URL.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header), Request: req}, nil
	})}
	created := request(t, server, "POST", "/v1/audits/github", "", "application/json", []byte(`{"url":"https://github.com/owner/repo"}`))
	if created.Code != http.StatusCreated || decodeResponse(t, created)["sourceCommit"] != sha {
		t.Fatalf("GitHub import failed: %d %s", created.Code, created.Body.String())
	}
	bad := request(t, server, "POST", "/v1/audits/github", "", "application/json", []byte(`{"url":"http://localhost/secrets"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unsafe GitHub URL accepted: %s", bad.Body.String())
	}
	bad = request(t, server, "POST", "/v1/audits/github", "", "application/json", []byte(`{"url":"https://github.com/owner/repo","extra":true}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatal("unknown JSON fields accepted")
	}
}

func TestAPIRejectsInvalidStateAndInput(t *testing.T) {
	server := NewServer(fixtureRunner{})
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample})
	if request(t, server, "POST", "/v1/audits", "", "text/plain", archive).Code != http.StatusUnsupportedMediaType {
		t.Fatal("accepted wrong archive media type")
	}
	if request(t, server, "POST", "/v1/audits", "", "application/zip", []byte("not a zip")).Code != http.StatusBadRequest {
		t.Fatal("accepted invalid archive")
	}
	created := decodeResponse(t, request(t, server, "POST", "/v1/audits", "", "application/zip", archive))
	base := "/v1/audits/" + created["id"].(string)
	auditor, student := created["auditorToken"].(string), created["studentToken"].(string)
	if request(t, server, "GET", base, "wrong", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("accepted invalid token")
	}
	if request(t, server, "GET", base+"/report", auditor, "", nil).Code != http.StatusConflict {
		t.Fatal("early report was available")
	}
	if request(t, server, "POST", base+"/tests", student, "", nil).Code != http.StatusConflict {
		t.Fatal("tests ran before start")
	}
	if request(t, server, "POST", base+"/start", auditor, "", nil).Code != http.StatusOK || request(t, server, "POST", base+"/start", auditor, "", nil).Code != http.StatusConflict {
		t.Fatal("start was not single-use")
	}
	if request(t, server, "PUT", base+"/files", auditor, "application/json", []byte(`{"path":"sample.go","source":"package sample"}`)).Code != http.StatusForbidden {
		t.Fatal("auditor changed student code")
	}
	for _, note := range []string{`{"text":""}`, `{"path":"missing.go","line":1,"text":"why"}`, `{"text":"ok","line":2}`} {
		if request(t, server, "POST", base+"/notes", student, "application/json", []byte(note)).Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid note %s", note)
		}
	}
	if request(t, server, "POST", base+"/finish", auditor, "", nil).Code != http.StatusOK || request(t, server, "POST", base+"/finish", auditor, "", nil).Code != http.StatusConflict {
		t.Fatal("finish was not single-use")
	}
}

func TestFinalReportDistinguishesSandboxFailureFromPassingTests(t *testing.T) {
	server := NewServer(fixtureRunner{})
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample})
	created := decodeResponse(t, request(t, server, "POST", "/v1/audits", "", "application/zip", archive))
	base := "/v1/audits/" + created["id"].(string)
	auditor := created["auditorToken"].(string)
	request(t, server, "POST", base+"/start", auditor, "", nil)
	server.runner = DockerRunner{Docker: "missing-docker-binary-for-test"}
	if request(t, server, "POST", base+"/finish", auditor, "", nil).Code != http.StatusOK {
		t.Fatal("sandbox failure prevented audit closure")
	}
	report := decodeResponse(t, request(t, server, "GET", base+"/report", auditor, "", nil))
	if report["tests"] != nil || report["testError"] == "" {
		t.Fatalf("sandbox failure was reported as a test pass: %v", report)
	}
}

func stringMustJSON(t *testing.T, s string) string {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAuditDeadlineFinishesOnServer(t *testing.T) {
	clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	server := NewServer(fixtureRunner{})
	server.now = func() time.Time { return clock }
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample, "sample_test.go": "package sample\n"})
	created := decodeResponse(t, request(t, server, "POST", "/v1/audits", "", "application/zip", archive))
	base := "/v1/audits/" + created["id"].(string)
	auditor, student := created["auditorToken"].(string), created["studentToken"].(string)
	request(t, server, "POST", base+"/start", auditor, "", nil)
	clock = clock.Add(11 * time.Minute)
	result := request(t, server, "PUT", base+"/files", student, "application/json", []byte(`{"path":"sample.go","source":"package sample"}`))
	if result.Code != http.StatusConflict {
		t.Fatalf("accepted late edit: %d", result.Code)
	}
	report := request(t, server, "GET", base+"/report", auditor, "", nil)
	if report.Code != http.StatusOK || decodeResponse(t, report)["reason"] != "deadline" {
		t.Fatalf("deadline was not enforced: %s", report.Body.String())
	}
}

func TestDeadlineReachedAfterAuthenticationStillRejectsEdit(t *testing.T) {
	clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	server := NewServer(fixtureRunner{})
	server.now = func() time.Time { return clock }
	archive := zipFixture(t, map[string]string{"go.mod": string(fixtureFiles()["go.mod"]), "sample.go": sample, "sample_test.go": "package sample\n"})
	created := decodeResponse(t, request(t, server, "POST", "/v1/audits", "", "application/zip", archive))
	base := "/v1/audits/" + created["id"].(string)
	request(t, server, "POST", base+"/start", created["auditorToken"].(string), "", nil)
	calls := 0
	server.now = func() time.Time {
		calls++
		if calls == 1 {
			return clock.Add(9 * time.Minute)
		}
		return clock.Add(11 * time.Minute)
	}
	result := request(t, server, "PUT", base+"/files", created["studentToken"].(string), "application/json", []byte(`{"path":"sample.go","source":"package sample"}`))
	if result.Code != http.StatusConflict {
		t.Fatalf("deadline race accepted edit: %d %s", result.Code, result.Body.String())
	}
}
