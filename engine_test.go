package audit

import (
	"context"
	"errors"
	"go/token"
	"strings"
	"testing"
)

const sample = `package sample
func Eligible(score int) bool { return score >= 70 }
func CanEnter(active bool, credits int) bool { return active && credits > 0 }
func Remaining(total, used int) int { return total - used }
`

type fixtureRunner struct{}

func (fixtureRunner) Run(_ context.Context, files Files) (RunResult, error) {
	s := string(files["sample.go"])
	if s == "" {
		return RunResult{}, errors.New("missing source")
	}
	failures := []string{}
	if !strings.Contains(s, "score >= 70") {
		failures = append(failures, "TestBoundary")
	}
	if !strings.Contains(s, "active && credits > 0") {
		failures = append(failures, "TestAccess")
	}
	if !strings.Contains(s, "total - used") {
		failures = append(failures, "TestRemaining")
	}
	return RunResult{Passed: len(failures) == 0, FailedTests: failures}, nil
}

func fixtureFiles() Files {
	return Files{"go.mod": []byte("module example.com/sample\n\ngo 1.22\n"), "sample.go": []byte(sample), "sample_test.go": []byte("package sample\n")}
}

func TestPrepareThreeRealMutations(t *testing.T) {
	p, err := Prepare(context.Background(), fixtureFiles(), fixtureRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Mutations) != 3 {
		t.Fatalf("mutations = %d, want 3", len(p.Mutations))
	}
	if string(p.Modified["sample.go"]) == sample {
		t.Fatal("student source was not mutated")
	}
	if string(p.Original["sample.go"]) != sample {
		t.Fatal("original source changed")
	}
	kinds := map[string]bool{}
	for _, m := range p.Mutations {
		if len(m.FailedTests) == 0 {
			t.Fatalf("mutation %+v has no failing tests", m)
		}
		if kinds[m.Function] {
			t.Fatalf("function %s was mutated twice", m.Function)
		}
		kinds[m.Function] = true
	}
}

func TestPrepareRejectsUnverifiableProject(t *testing.T) {
	files := fixtureFiles()
	files["sample.go"] = []byte("package sample\nfunc Healthy() bool { return true }\n")
	if _, err := Prepare(context.Background(), files, fixtureRunner{}); err == nil {
		t.Fatal("expected baseline failure")
	}
}

func TestApplyDoesNotEditInputs(t *testing.T) {
	files := fixtureFiles()
	candidates, err := Analyze(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("no candidates")
	}
	changed, err := Apply(files, candidates[:1])
	if err != nil {
		t.Fatal(err)
	}
	if string(files["sample.go"]) != sample || string(changed["sample.go"]) == sample {
		t.Fatal("mutation did not preserve the original")
	}
}

func TestASTMutationOperatorsAndValidation(t *testing.T) {
	files := Files{
		"go.mod": []byte("module example.com/operators\n"),
		"operators.go": []byte(`package operators
// Fake operands: a >= b, a + b.
var text = "a == b"
func Compare(a, b int) bool { return a == b || a != b || a < b || a <= b || a > b || a >= b }
func Math(a, b int) int { return (a + b - b) * a / b }
`),
	}
	candidates, err := Analyze(files)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"==": "!=", "!=": "==", "<": ">=", "<=": ">", ">": "<=", ">=": ">", "||": "&&", "+": "-", "-": "+", "*": "/", "/": "*"}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if expected[candidate.Before] != candidate.After {
			t.Fatalf("unexpected AST mutation: %+v", candidate)
		}
		seen[candidate.Before] = true
	}
	for operator := range expected {
		if !seen[operator] {
			t.Errorf("operator %q was not found in the AST", operator)
		}
	}
	if swapOperator(token.LAND) != "||" {
		t.Fatal("and operator did not invert")
	}
	if swapOperator(token.LOR) != "&&" || swapOperator(token.ASSIGN) != "" {
		t.Fatal("invalid logical/assignment mutation")
	}
	if _, err := Apply(files, []Mutation{{File: "missing.go", Offset: 0, Before: "+", After: "-"}}); err == nil {
		t.Fatal("mutated nonexistent file")
	}
	if _, err := Apply(files, []Mutation{{File: "operators.go", Offset: 0, Before: "+", After: "-"}}); err == nil {
		t.Fatal("accepted stale AST offset")
	}
}

type alwaysPassRunner struct{}

func (alwaysPassRunner) Run(context.Context, Files) (RunResult, error) {
	return RunResult{Passed: true}, nil
}

func TestPrepareDoesNotCreateFictitiousMutations(t *testing.T) {
	if _, err := Prepare(context.Background(), fixtureFiles(), alwaysPassRunner{}); !errors.Is(err, ErrNotEnoughMutations) {
		t.Fatalf("unverified mutations were accepted: %v", err)
	}
	broken := fixtureFiles()
	broken["sample.go"] = []byte("package sample\nfunc Broken( {\n")
	if _, err := Analyze(broken); err == nil {
		t.Fatal("invalid Go syntax was accepted")
	}
}
