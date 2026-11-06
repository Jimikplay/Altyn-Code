package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const defaultImage = "golang:1.27.3-alpine"

type DockerRunner struct {
	Docker  string
	Image   string
	Timeout time.Duration
}

// limitedRunner bounds the number of simultaneous sandbox containers across
// preparation, student test runs, and automatic finalization.
type limitedRunner struct {
	inner Runner
	slots chan struct{}
}

func (r *limitedRunner) Run(ctx context.Context, files Files) (RunResult, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	return r.inner.Run(ctx, files)
}

func dockerArgs(workspace, image, name string) []string {
	return []string{
		"run", "--rm", "--name", name, "--pull", "never", "--network", "none",
		"--read-only", "--tmpfs", "/tmp:rw,exec,nosuid,size=512m",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", "1g", "--memory-swap", "1g", "--cpus", "1",
		"--pids-limit", "64", "--user", "65534:65534",
		"--mount", "type=bind,source=" + workspace + ",target=/workspace,readonly",
		"--workdir", "/workspace", "--env", "GOPROXY=off", "--env", "GOSUMDB=off",
		"--env", "GOTOOLCHAIN=local", "--env", "CGO_ENABLED=0",
		"--env", "GOCACHE=/tmp/cache", "--env", "GOMODCACHE=/tmp/modcache",
		"--env", "HOME=/tmp", image, "go", "test", "-json", "-count=1", "./...",
	}
}

type boundedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const max = 64 << 10
	if len(b.data) < max {
		b.data = append(b.data, p[:min(len(p), max-len(b.data))]...)
	}
	return len(p), nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func failedGoTests(output []byte) []string {
	var event struct {
		Action, Package, Test string
	}
	set := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Action == "fail" && event.Test != "" {
			set[event.Package+"/"+event.Test] = true
		}
		event = struct{ Action, Package, Test string }{}
	}
	tests := make([]string, 0, len(set))
	for name := range set {
		tests = append(tests, name)
	}
	sort.Strings(tests)
	return tests
}

func (r DockerRunner) Run(ctx context.Context, files Files) (RunResult, error) {
	docker := r.Docker
	if docker == "" {
		docker = "docker"
	}
	image := r.Image
	if image == "" {
		image = defaultImage
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	workspace, err := os.MkdirTemp("", "live-code-audit-")
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0755); err != nil {
		return RunResult{}, err
	}
	for name, contents := range files {
		if !validProjectPath(name) {
			return RunResult{}, fmt.Errorf("invalid source path %q", name)
		}
		target := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return RunResult{}, err
		}
		if err := os.WriteFile(target, contents, 0644); err != nil {
			return RunResult{}, err
		}
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return RunResult{}, err
	}
	name := "audit-" + hex.EncodeToString(raw[:])
	job, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(job, docker, dockerArgs(workspace, image, name)...)
	output := &boundedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if job.Err() != nil {
		// Killing the Docker CLI does not reliably kill its container.
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_ = exec.CommandContext(cleanCtx, docker, "rm", "-f", name).Run()
		return RunResult{Output: "sandbox execution timed out", TimedOut: true}, nil
	}
	if err == nil {
		return RunResult{Passed: true, Output: output.String()}, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 125 {
		return RunResult{}, fmt.Errorf("Docker sandbox unavailable: %w: %s", err, output.String())
	}
	text := output.String()
	return RunResult{FailedTests: failedGoTests([]byte(text)), Output: text}, nil
}

// ReadonlyFile is used for streaming bounded external archives, not for
// executing them. It returns an error as soon as the configured limit is hit.
func readLimited(r io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("input exceeds size limit")
	}
	return data, nil
}
