package audit

import "net/http"

func demoFiles() Files {
	return Files{
		"go.mod": []byte("module example.com/audit-demo\n\ngo 1.22\n"),
		"sample.go": []byte(`package sample

func Eligible(score int) bool { return score >= 70 }
func CanEnter(active bool, credits int) bool { return active && credits > 0 }
func Remaining(total, used int) int { return total - used }
`),
		"sample_test.go": []byte(`package sample

import "testing"

func TestBoundary(t *testing.T) {
	if !Eligible(70) { t.Fatal("score 70 should be eligible") }
}

func TestAccess(t *testing.T) {
	if CanEnter(false, 10) { t.Fatal("inactive user cannot enter") }
}

func TestRemaining(t *testing.T) {
	if Remaining(10, 3) != 7 { t.Fatal("remaining credits should be 7") }
}
`),
	}
}

func (s *Server) createDemo(w http.ResponseWriter, r *http.Request) {
	if !s.reserve(w) {
		return
	}
	defer func() { <-s.preparing }()
	s.create(w, r, demoFiles(), "")
}
