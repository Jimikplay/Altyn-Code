package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// FetchGitHub imports only public repositories from github.com and pins the
// archive to the default branch's resolved commit before downloading it.
func FetchGitHub(ctx context.Context, raw string, client *http.Client) (Files, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, "", errors.New("only https://github.com/owner/repository URLs are accepted")
	}
	components := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(components) != 2 || !ownerPattern.MatchString(components[0]) || !repoPattern.MatchString(components[1]) || components[1] == "." || components[1] == ".." {
		return nil, "", errors.New("invalid GitHub owner or repository")
	}
	owner, repo := components[0], strings.TrimSuffix(components[1], ".git")
	if !repoPattern.MatchString(repo) || repo == "." || repo == ".." {
		return nil, "", errors.New("invalid GitHub repository")
	}
	if client == nil {
		client = http.DefaultClient
	}
	safe := *client
	safe.Timeout = 15 * time.Second
	safe.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 || request.URL.Scheme != "https" || request.URL.Port() != "" || (request.URL.Hostname() != "api.github.com" && request.URL.Hostname() != "codeload.github.com") {
			return errors.New("GitHub redirected outside approved hosts")
		}
		return nil
	}
	commitsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=1", owner, repo)
	response, err := fetch(ctx, &safe, commitsURL, 32<<10)
	if err != nil {
		return nil, "", err
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(response, &commits) != nil || len(commits) == 0 || !commitPattern.MatchString(commits[0].SHA) {
		return nil, "", errors.New("GitHub did not return a valid default-branch commit")
	}
	commit := commits[0].SHA
	archiveURL := fmt.Sprintf("https://codeload.github.com/%s/%s/zip/%s", owner, repo, commit)
	data, err := fetch(ctx, &safe, archiveURL, MaxArchiveBytes)
	if err != nil {
		return nil, "", err
	}
	files, err := LoadZIP(data)
	return files, commit, err
}

func fetch(ctx context.Context, client *http.Client, address string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "live-code-audit/0.1")
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	return readLimited(response.Body, max)
}
