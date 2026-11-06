package audit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDockerRunnerFailsClosedWithoutDocker(t *testing.T) {
	runner := DockerRunner{Docker: "missing-docker-binary-for-test"}
	if result, err := runner.Run(context.Background(), fixtureFiles()); err == nil || result.Passed {
		t.Fatalf("unsafe success with missing sandbox: %+v, %v", result, err)
	}
}

func TestSandboxConcurrencyLimitRejectsCancelledQueue(t *testing.T) {
	inner := blockedRunner{entered: make(chan struct{}), release: make(chan struct{})}
	runner := &limitedRunner{inner: inner, slots: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), fixtureFiles())
		done <- err
	}()
	select {
	case <-inner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first sandbox did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx, fixtureFiles()); err == nil {
		t.Fatal("cancelled sandbox request ran instead of staying queued")
	}
	close(inner.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDockerArgumentsRestrictUntrustedCode(t *testing.T) {
	args := dockerArgs("/tmp/audit", "golang:1.27.3-alpine", "audit-test")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--network none", "--read-only", "--pull never", "--cap-drop ALL", "--pids-limit 64", "--memory 1g", "--security-opt no-new-privileges", "--user 65534:65534", "--mount type=bind,source=/tmp/audit,target=/workspace,readonly"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing sandbox restriction %q", required)
		}
	}
	if strings.Contains(joined, "--privileged") {
		t.Fatal("privileged container")
	}
}

func TestGoTestJSONDistinguishesTestFailureFromBuildError(t *testing.T) {
	fail := []byte("{\"Action\":\"fail\",\"Package\":\"example/a\",\"Test\":\"TestBoundary\"}\n")
	if tests := failedGoTests(fail); len(tests) != 1 || tests[0] != "example/a/TestBoundary" {
		t.Fatalf("unexpected failing tests: %v", tests)
	}
	if tests := failedGoTests([]byte("# example/a\n./main.go:1: syntax error\n")); len(tests) != 0 {
		t.Fatal("compilation error was mistaken for a behavioral failure")
	}
}
