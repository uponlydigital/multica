package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// Each task's overlay is its own Hermes data root, so a profile-seeded overlay
// used to build a ~390 MB Python environment under <overlay>/installs on every
// task. These tests pin the shared store that replaces that build.

func readLinkTarget(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v)", path, fi.Mode())
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	return target
}

// Two consecutive tasks seeded from the same profile share one installs dir:
// whatever the first task's Hermes builds there, the second sees.
func TestHermesInstallsSharedAcrossTasks(t *testing.T) {
	t.Parallel()
	profile := t.TempDir() // a named profile: no installs/ of its own
	mustWrite(t, filepath.Join(profile, "config.yaml"), "model: hermes-4\n")
	store := filepath.Join(t.TempDir(), "hermes-installs", "coder")
	skills := []SkillContextForEnv{{Name: "Review Helper", Content: "x"}}

	first := filepath.Join(t.TempDir(), "hermes-home")
	if _, err := prepareHermesHome(first, profile, false, skills, nil, "", "", testLogger()); err != nil {
		t.Fatalf("prepare first: %v", err)
	}
	linkHermesInstalls(first, profile, store, testLogger())
	if got := readLinkTarget(t, filepath.Join(first, "installs")); got != store {
		t.Fatalf("first task installs -> %q, want store %q", got, store)
	}
	// Hermes builds its environment through the link.
	mustWrite(t, filepath.Join(first, "installs", "abc123", "facts.json"), "{}")

	second := filepath.Join(t.TempDir(), "hermes-home")
	if _, err := prepareHermesHome(second, profile, false, skills, nil, "", "", testLogger()); err != nil {
		t.Fatalf("prepare second: %v", err)
	}
	linkHermesInstalls(second, profile, store, testLogger())
	if _, err := os.Stat(filepath.Join(second, "installs", "abc123", "facts.json")); err != nil {
		t.Fatalf("second task does not see the first task's install: %v", err)
	}
	// The profile itself is never written.
	if _, err := os.Lstat(filepath.Join(profile, "installs")); !os.IsNotExist(err) {
		t.Errorf("source profile gained an installs entry: %v", err)
	}
}

// A source home that has its own installs/ (the native root used without -p)
// keeps linking to it, as the generic mirror did before; the store is unused.
func TestHermesInstallsPrefersSourceInstalls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "installs"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "store")
	home := filepath.Join(t.TempDir(), "hermes-home")
	skills := []SkillContextForEnv{{Name: "Review Helper", Content: "x"}}
	if _, err := prepareHermesHome(home, root, false, skills, nil, "", "", testLogger()); err != nil {
		t.Fatal(err)
	}
	linkHermesInstalls(home, root, store, testLogger())
	if got, want := readLinkTarget(t, filepath.Join(home, "installs")), filepath.Join(root, "installs"); got != want {
		t.Fatalf("installs -> %q, want source %q", got, want)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("store should not be created when the source has installs: %v", err)
	}
}

// Reuse rebuilds the overlay (mirror + reconcile). The link must survive it,
// and a real task-local installs dir from an older daemon is replaced.
func TestHermesInstallsSurvivesReuseAndReplacesLegacyDir(t *testing.T) {
	t.Parallel()
	profile := t.TempDir()
	mustWrite(t, filepath.Join(profile, "config.yaml"), "model: hermes-4\n")
	store := filepath.Join(t.TempDir(), "store")
	withSkill := TaskContextForEnv{
		IssueID:     "hermes-installs",
		AgentSkills: []SkillContextForEnv{{Name: "Review Helper", Content: "Help review."}},
	}
	env, err := Prepare(PrepareParams{
		WorkspacesRoot:      t.TempDir(),
		WorkspaceID:         "ws-hermes-installs",
		TaskID:              "cccc1111-2222-3333-4444-555566667777",
		Provider:            "hermes",
		HermesSourceHome:    profile,
		HermesInstallsStore: store,
		Task:                withSkill,
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer env.Cleanup(true)
	link := filepath.Join(env.HermesHome, "installs")
	if got := readLinkTarget(t, link); got != store {
		t.Fatalf("after Prepare installs -> %q, want %q", got, store)
	}

	// Simulate an overlay left by an older daemon: a real, task-local build.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(link, "old", "facts.json"), "{}")

	reused := Reuse(ReuseParams{WorkDir: env.WorkDir, Provider: "hermes", HermesSourceHome: profile, HermesInstallsStore: store, Task: withSkill}, testLogger())
	if reused == nil || reused.HermesHome == "" {
		t.Fatal("Reuse with a bound skill should keep the overlay")
	}
	if got := readLinkTarget(t, link); got != store {
		t.Fatalf("after Reuse installs -> %q, want %q", got, store)
	}
}

// No store and no source installs: behaviour before the store — nothing is
// linked and Hermes builds task-local.
func TestHermesInstallsNoStoreLeavesTaskLocal(t *testing.T) {
	t.Parallel()
	profile := t.TempDir()
	home := filepath.Join(t.TempDir(), "hermes-home")
	skills := []SkillContextForEnv{{Name: "Review Helper", Content: "x"}}
	if _, err := prepareHermesHome(home, profile, false, skills, nil, "", "", testLogger()); err != nil {
		t.Fatal(err)
	}
	linkHermesInstalls(home, profile, "", testLogger())
	if _, err := os.Lstat(filepath.Join(home, "installs")); !os.IsNotExist(err) {
		t.Fatalf("no installs entry expected without a store, got err=%v", err)
	}
}

// One store per Hermes source profile: two profiles (different plugin sets)
// must not rebuild over each other.
func TestHermesInstallsStorePathPerProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	native := filepath.Join(home, ".hermes")
	a := HermesInstallsStorePath("", filepath.Join(native, "profiles", "labs"))
	b := HermesInstallsStorePath("", filepath.Join(native, "profiles", "bench"))
	if a == "" || b == "" || a == b {
		t.Fatalf("store paths must be non-empty and distinct: %q %q", a, b)
	}
	if filepath.Base(a) != "labs" || filepath.Base(filepath.Dir(a)) != hermesInstallsStoreRoot {
		t.Errorf("unexpected store layout: %q", a)
	}
	if again := HermesInstallsStorePath("", filepath.Join(native, "profiles", "labs")); again != a {
		t.Errorf("store path not stable: %q vs %q", a, again)
	}
}

// Yolo is opt-in per agent via custom_env only. Hermes loads the overlay .env
// with override=True, so a HERMES_YOLO_MODE line in the source profile's .env
// must not reach it.
func TestHermesOverlayEnvStripsYolo(t *testing.T) {
	t.Parallel()
	sourceHome := t.TempDir()
	mustWrite(t, filepath.Join(sourceHome, ".env"), "ANTHROPIC_API_KEY=sk-source\nexport HERMES_YOLO_MODE=1\n")
	hermesHome := filepath.Join(t.TempDir(), "hermes-home")
	skills := []SkillContextForEnv{{Name: "Review Helper", Content: "x"}}
	if _, err := prepareHermesHome(hermesHome, sourceHome, false, skills, nil, "", "", testLogger()); err != nil {
		t.Fatal(err)
	}
	env := applyDotenvOverride(t, filepath.Join(hermesHome, ".env"))
	if v, ok := env["HERMES_YOLO_MODE"]; ok {
		t.Errorf("HERMES_YOLO_MODE leaked from the source .env: %q", v)
	}
	if env["ANTHROPIC_API_KEY"] != "sk-source" {
		t.Errorf("other source settings must survive, got %q", env["ANTHROPIC_API_KEY"])
	}
}
