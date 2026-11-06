package audit

import (
	"net/http"
	"strings"
	"testing"
)

func TestDemoPageServesSameOriginAssets(t *testing.T) {
	server := NewServer(fixtureRunner{})
	for _, endpoint := range []string{"/", "/app.js", "/style.css"} {
		response := request(t, server, "GET", endpoint, "", "", nil)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("%s unavailable: %d", endpoint, response.Code)
		}
	}
	page := request(t, server, "GET", "/", "", "", nil).Body.String()
	securityHeaders := request(t, server, "GET", "/", "", "", nil)
	if !strings.Contains(securityHeaders.Header().Get("Content-Security-Policy"), "script-src 'self'") || securityHeaders.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("local test page must not load remote scripts or cache role data")
	}
	for _, label := range []string{"Создать демо-аудит", "Рабочая область", "Экран аудитора"} {
		if !strings.Contains(page, label) {
			t.Errorf("missing UI action %q", label)
		}
	}
	if strings.Contains(page, "auditorToken") {
		t.Fatal("sensitive role token embedded in page")
	}
}

func TestDemoEndpointUsesRealPreparationFlow(t *testing.T) {
	server := NewServer(fixtureRunner{})
	created := request(t, server, "POST", "/v1/audits/demo", "", "", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("demo was not created: %d %s", created.Code, created.Body.String())
	}
	access := decodeResponse(t, created)
	id := access["id"].(string)
	student := request(t, server, "GET", "/v1/audits/"+id, access["studentToken"].(string), "", nil)
	if student.Code != http.StatusOK || strings.Contains(student.Body.String(), "mutations") {
		t.Fatalf("student view leaked mutations: %d %s", student.Code, student.Body.String())
	}
	auditor := request(t, server, "GET", "/v1/audits/"+id, access["auditorToken"].(string), "", nil)
	if auditor.Code != http.StatusOK || !strings.Contains(auditor.Body.String(), "mutations") {
		t.Fatal("auditor view missing mutations")
	}
}
