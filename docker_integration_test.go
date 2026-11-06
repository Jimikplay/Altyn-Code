package audit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRealGoProjectMutatesOnlyTestDetectableBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("Docker integration test")
	}
	files := demoFiles()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	prepared, err := Prepare(ctx, files, DockerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Mutations) != 3 {
		t.Fatalf("wanted 3 mutations, got %d", len(prepared.Mutations))
	}
	for _, mutation := range prepared.Mutations {
		if len(mutation.FailedTests) == 0 || !strings.Contains(mutation.FailedTests[0], "/Test") {
			t.Fatalf("mutation is not confirmed by a real Go test: %+v", mutation)
		}
	}
}
