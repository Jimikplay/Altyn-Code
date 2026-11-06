package audit

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"
)

type Files map[string][]byte

func (files Files) Clone() Files {
	copy := make(Files, len(files))
	for name, contents := range files {
		copy[name] = append([]byte(nil), contents...)
	}
	return copy
}

type RunResult struct {
	Passed      bool     `json:"passed"`
	FailedTests []string `json:"failedTests"`
	Output      string   `json:"output"`
	TimedOut    bool     `json:"timedOut"`
}

type Runner interface {
	Run(context.Context, Files) (RunResult, error)
}

type Mutation struct {
	ID          int      `json:"id"`
	File        string   `json:"file"`
	Function    string   `json:"function"`
	Line        int      `json:"line"`
	Offset      int      `json:"-"`
	Before      string   `json:"before"`
	After       string   `json:"after"`
	FailedTests []string `json:"failedTests"`
}

type Prepared struct {
	Original  Files
	Modified  Files
	Mutations []Mutation
}

var ErrBaseline = errors.New("the original project must pass all tests")
var ErrNotEnoughMutations = errors.New("fewer than three test-detectable mutations on different lines")

func Analyze(files Files) ([]Mutation, error) {
	if len(files["go.mod"]) == 0 {
		return nil, errors.New("project root must contain go.mod")
	}
	var names []string
	for name := range files {
		if path.Ext(name) == ".go" && !strings.HasSuffix(name, "_test.go") && !strings.HasPrefix(name, "vendor/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("no Go source files found")
	}
	var result []Mutation
	for _, name := range names {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, files[name], parser.AllErrors)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				expression, ok := n.(*ast.BinaryExpr)
				if !ok {
					return true
				}
				after := swapOperator(expression.Op)
				if after == "" {
					return true
				}
				position := fset.Position(expression.OpPos)
				result = append(result, Mutation{
					File: name, Function: fn.Name.Name, Line: position.Line,
					Offset: position.Offset, Before: expression.Op.String(), After: after,
				})
				return true
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].File == result[j].File {
			return result[i].Offset < result[j].Offset
		}
		return result[i].File < result[j].File
	})
	return result, nil
}

func swapOperator(op token.Token) string {
	switch op {
	case token.EQL:
		return "!="
	case token.NEQ:
		return "=="
	case token.LSS:
		return ">="
	case token.LEQ:
		return ">"
	case token.GTR:
		return "<="
	case token.GEQ:
		return ">"
	case token.LAND:
		return "||"
	case token.LOR:
		return "&&"
	case token.ADD:
		return "-"
	case token.SUB:
		return "+"
	case token.MUL:
		return "/"
	case token.QUO:
		return "*"
	default:
		return ""
	}
}

func Apply(files Files, mutations []Mutation) (Files, error) {
	changed := files.Clone()
	byFile := make(map[string][]Mutation)
	for _, m := range mutations {
		byFile[m.File] = append(byFile[m.File], m)
	}
	for name, selected := range byFile {
		code, ok := changed[name]
		if !ok {
			return nil, fmt.Errorf("unknown file %q", name)
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].Offset > selected[j].Offset })
		for _, m := range selected {
			if m.Offset < 0 || m.Offset+len(m.Before) > len(code) || string(code[m.Offset:m.Offset+len(m.Before)]) != m.Before {
				return nil, fmt.Errorf("invalid mutation offset for %q", name)
			}
			code = append(append(append([]byte(nil), code[:m.Offset]...), m.After...), code[m.Offset+len(m.Before):]...)
		}
		changed[name] = code
	}
	return changed, nil
}

// Prepare never fabricates a mutation: each candidate must fail at least one
// previously passing test while still compiling and executing the test suite.
func Prepare(ctx context.Context, files Files, runner Runner) (*Prepared, error) {
	candidates, err := Analyze(files)
	if err != nil {
		return nil, err
	}
	baseline, err := runner.Run(ctx, files)
	if err != nil {
		return nil, err
	}
	if !baseline.Passed {
		return nil, fmt.Errorf("%w: %s", ErrBaseline, baseline.Output)
	}
	selected := make([]Mutation, 0, 3)
	usedLines := make(map[string]bool)
	usedFunctions := make(map[string]bool)
	if len(candidates) > 80 {
		candidates = candidates[:80]
	}
	for pass := 0; pass < 2 && len(selected) < 3; pass++ {
		for _, candidate := range candidates {
			if len(selected) == 3 {
				break
			}
			lineKey := fmt.Sprintf("%s:%d", candidate.File, candidate.Line)
			functionKey := candidate.File + ":" + candidate.Function
			if usedLines[lineKey] || (pass == 0 && usedFunctions[functionKey]) {
				continue
			}
			changed, err := Apply(files, []Mutation{candidate})
			if err != nil {
				return nil, err
			}
			outcome, err := runner.Run(ctx, changed)
			if err != nil {
				return nil, err
			}
			if outcome.Passed || outcome.TimedOut || len(outcome.FailedTests) == 0 {
				continue
			}
			candidate.ID = len(selected) + 1
			candidate.FailedTests = append([]string(nil), outcome.FailedTests...)
			selected = append(selected, candidate)
			usedLines[lineKey] = true
			usedFunctions[functionKey] = true
		}
	}
	if len(selected) != 3 {
		return nil, ErrNotEnoughMutations
	}
	combined, err := Apply(files, selected)
	if err != nil {
		return nil, err
	}
	check, err := runner.Run(ctx, combined)
	if err != nil {
		return nil, err
	}
	if check.Passed || check.TimedOut || len(check.FailedTests) == 0 {
		return nil, errors.New("combined mutations do not produce a runnable failing test suite")
	}
	return &Prepared{Original: files.Clone(), Modified: combined, Mutations: selected}, nil
}
