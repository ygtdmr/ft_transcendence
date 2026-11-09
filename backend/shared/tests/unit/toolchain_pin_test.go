package unit_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var goDirective = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)

// goVersion returns the "go" directive of a go.mod or go.work file.
func goVersion(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := goDirective.FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s has no go directive", path)
	}
	return string(m[1])
}

// TestBackendModulesShareOneGoVersion keeps every backend module and the
// workspace on a single Go toolchain pin (issue #83). A mismatch such as
// 1.27.0 vs 1.27.1 breaks Docker builds and workspace resolution.
func TestBackendModulesShareOneGoVersion(t *testing.T) {
	backend, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mods, err := filepath.Glob(filepath.Join(backend, "*", "go.mod"))
	if err != nil || len(mods) < 2 {
		t.Fatalf("found %d backend modules (err=%v), want at least shared plus one service", len(mods), err)
	}

	want := goVersion(t, filepath.Join(backend, "go.work"))
	for _, mod := range mods {
		if got := goVersion(t, mod); got != want {
			t.Errorf("%s pins go %s, but backend/go.work pins go %s", mod, got, want)
		}
	}
}

// TestWorkspaceListsEveryBackendModule fails when a service module is added
// without being added to backend/go.work.
func TestWorkspaceListsEveryBackendModule(t *testing.T) {
	backend, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	work, err := os.ReadFile(filepath.Join(backend, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	mods, _ := filepath.Glob(filepath.Join(backend, "*", "go.mod"))
	for _, mod := range mods {
		name := filepath.Base(filepath.Dir(mod))
		if !regexp.MustCompile(`(?m)^\s*\./` + regexp.QuoteMeta(name) + `\s*$`).Match(work) {
			t.Errorf("backend/go.work does not use ./%s", name)
		}
	}
}
