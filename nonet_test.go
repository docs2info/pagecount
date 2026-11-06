package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoNet enforces the "sends nothing anywhere" promise: the binary must not link any net
// package, directly or transitively.
func TestNoNet(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found on PATH: %v", err)
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "net") {
			t.Errorf("forbidden dependency: %s", pkg)
		}
	}
}
