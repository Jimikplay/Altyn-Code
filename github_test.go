package audit

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestFetchGitHubPinsCommitAndAcceptsOnlyGitHub(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	archive := zipFixture(t, map[string]string{"repo-0123456/go.mod": "module example.com/sample\n", "repo-0123456/sample.go": sample})
	requests := []string{}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		var body []byte
		switch req.URL.String() {
		case "https://api.github.com/repos/owner/repo/commits?per_page=1":
			body = []byte(`[{"sha":"` + sha + `"}]`)
		case "https://codeload.github.com/owner/repo/zip/" + sha:
			body = archive
		default:
			t.Fatalf("unexpected network request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header), Request: req}, nil
	})}
	files, commit, err := FetchGitHub(context.Background(), "https://github.com/owner/repo", client)
	if err != nil || commit != sha || string(files["sample.go"]) != sample || len(requests) != 2 {
		t.Fatalf("unexpected import: commit=%s files=%v requests=%v error=%v", commit, files, requests, err)
	}
}

func TestFetchGitHubRejectsNonGitHubHostsWithoutNetwork(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("network request for invalid URL: %s", req.URL)
		return nil, nil
	})}
	for _, raw := range []string{"http://github.com/a/b", "https://github.com.evil.com/a/b", "https://github.com@evil.com/a/b", "https://github.com/a/b/tree/main", "https://github.com:444/a/b", "https://github.com/a/../b"} {
		if _, _, err := FetchGitHub(context.Background(), raw, client); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestFetchGitHubRejectsRedirectToExternalHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.github.com" {
			t.Fatalf("followed untrusted redirect: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")),
			Header: http.Header{"Location": {"http://169.254.169.254/metadata"}}, Request: req,
		}, nil
	})}
	if _, _, err := FetchGitHub(context.Background(), "https://github.com/owner/repo", client); err == nil {
		t.Fatal("external redirect was accepted")
	}
}
