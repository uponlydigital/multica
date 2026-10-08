package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// LAB-152: with yolo off, the daemon grants a Hermes edit approval only when
// every file the edit touches resolves inside the task workdir. Before this,
// every edit was granted whatever its path (blocker-1 gate P24).

func hermesEditParams(t *testing.T, content []map[string]any, raw map[string]any) json.RawMessage {
	t.Helper()
	tc := map[string]any{"toolCallId": "edit-approval-1", "kind": "edit", "title": "Approve edit", "status": "pending"}
	if content != nil {
		tc["content"] = content
	}
	if raw != nil {
		tc["rawInput"] = raw
	}
	b, err := json.Marshal(map[string]any{"sessionId": "s", "toolCall": tc, "options": json.RawMessage(hermesEditApprovalOptions)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hermesWriteFileEdit(t *testing.T, path string) json.RawMessage {
	t.Helper()
	return hermesEditParams(t,
		[]map[string]any{{"type": "diff", "path": path, "newText": "x"}},
		map[string]any{"tool": "write_file", "arguments": map[string]any{"path": path, "content": "x"}})
}

func hermesV4AEdit(t *testing.T, display, patch string) json.RawMessage {
	t.Helper()
	return hermesEditParams(t,
		[]map[string]any{{"type": "diff", "path": display, "newText": patch}},
		map[string]any{"tool": "patch", "arguments": map[string]any{"mode": "patch", "patch": patch}})
}

func TestHermesEditPathPolicy(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	workdir := filepath.Join(base, "task", "workdir")
	outsideDir := filepath.Join(base, "canary")
	for _, d := range []string{filepath.Join(workdir, "sub"), outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink inside the workdir that points outside it.
	if err := os.Symlink(outsideDir, filepath.Join(workdir, "escape")); err != nil {
		t.Fatal(err)
	}
	// The workdir reached through a link (macOS /var -> /private/var style)
	// must still count as inside.
	linkedWorkdir := filepath.Join(base, "wd-link")
	if err := os.Symlink(workdir, linkedWorkdir); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "b.txt")

	sel := hermesPermissionSelector(true, false, workdir, nil)
	cases := []hermesEditCase{
		{"relative file in workdir", hermesWriteFileEdit(t, "notes.md"), true},
		{"nested new dirs in workdir", hermesWriteFileEdit(t, "a/b/c.txt"), true},
		{"absolute path in workdir", hermesWriteFileEdit(t, filepath.Join(workdir, "sub", "f.txt")), true},
		{"workdir reached via symlink", hermesWriteFileEdit(t, filepath.Join(linkedWorkdir, "f.txt")), true},
		{"dot-dot that stays inside", hermesWriteFileEdit(t, "sub/../f.txt"), true},
		{"absolute path outside (gate P24)", hermesWriteFileEdit(t, filepath.Join(outsideDir, "tok-written.txt")), false},
		{"relative escape with ..", hermesWriteFileEdit(t, "../../canary/x.txt"), false},
		{"dot-dot after a missing dir", hermesWriteFileEdit(t, "newdir/../../x.txt"), false},
		{"symlink inside workdir pointing outside", hermesWriteFileEdit(t, "escape/x.txt"), false},
		{"tilde home path", hermesWriteFileEdit(t, "~/.hermes/.env"), false},
		{"other user's home", hermesWriteFileEdit(t, "~root/x"), false},
		{"sibling sharing the workdir prefix", hermesWriteFileEdit(t, workdir+"-evil/x"), false},
		{"tmp dir", hermesWriteFileEdit(t, "/tmp/x"), false},
		{"no paths at all", hermesEditParams(t, nil, nil), false},
		{"diff path inside but rawInput path outside", hermesEditParams(t,
			[]map[string]any{{"type": "diff", "path": "ok.md", "newText": "x"}},
			map[string]any{"tool": "write_file", "arguments": map[string]any{"path": "/etc/x", "content": "x"}}), false},
		{"V4A patch, all targets inside", hermesV4AEdit(t, "a.txt, b.txt",
			"*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+y\n*** Add File: b.txt\n+z\n*** End Patch"), true},
		{"V4A patch, one target outside", hermesV4AEdit(t, "a.txt, "+outsideFile,
			"*** Begin Patch\n*** Update File: a.txt\n*** Add File: "+outsideFile+"\n+z\n*** End Patch"), false},
		{"V4A move to outside", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\n*** Move File: a.txt -> ../../canary/a.txt\n*** End Patch"), false},
	}
	runHermesEditCases(t, sel, cases)
}

// Review of PR #2 (LAB-152 round 2): bypasses found by the independent review.
func TestHermesEditPathPolicyReviewBypasses(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	workdir := filepath.Join(base, "task", "workdir")
	outsideDir := filepath.Join(base, "canary")
	for _, d := range []string{workdir, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outsideDir, filepath.Join(workdir, "escape")); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "target.txt")
	if err := os.WriteFile(outsideFile, []byte("canary"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "note " (trailing space) is a symlink to a file outside; "note" does
	// not exist. And "plain" is a symlink outside, requested as "plain ".
	if err := os.Symlink(outsideFile, filepath.Join(workdir, "note ")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(workdir, "plain")); err != nil {
		t.Fatal(err)
	}
	abs := func(rel string) string { return workdir + "/" + rel }

	sel := hermesPermissionSelector(true, false, workdir, nil)
	cases := []hermesEditCase{
		// 1. Hermes' executor accepts "***Add File:" with no space after ***.
		{"V4A no-space Add header outside", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+y\n***Add File: "+filepath.Join(outsideDir, "x")+"\n+z\n*** End Patch"), false},
		{"V4A no-space Move header outside", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\n***Move File: a.txt -> "+filepath.Join(outsideDir, "a.txt")+"\n*** End Patch"), false},
		{"V4A NBSP after *** outside", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\n***\u00a0Add File: "+filepath.Join(outsideDir, "x")+"\n+z\n*** End Patch"), false},
		{"V4A unknown *** line", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+y\n*** Copy File: a.txt\n*** End Patch"), false},
		{"V4A CRLF, all inside", hermesV4AEdit(t, "a.txt",
			"*** Begin Patch\r\n***Update File: a.txt\r\n@@\r\n-x\r\n+y\r\n*** End Patch\r\n"), true},
		// 2. ".." out of a missing dir must not hide a later symlink.
		{"missing/../symlink escape (absolute)", hermesWriteFileEdit(t, abs("missing/../escape/new.txt")), false},
		{"missing/../symlink escape (relative)", hermesWriteFileEdit(t, "missing/../escape/new.txt"), false},
		{"missing/../ back inside", hermesWriteFileEdit(t, "missing/../new.txt"), true},
		// 4. Paths are not trimmed into a different file.
		{"trailing-space symlink outside", hermesWriteFileEdit(t, abs("note ")), false},
		{"trailing space on an outside symlink name", hermesWriteFileEdit(t, abs("plain ")), false},
		{"trailing Python-only whitespace on symlink name", hermesWriteFileEdit(t, abs("plain\x1c")), false},
		{"whitespace-only path", hermesWriteFileEdit(t, "   "), false},
		{"tilde is denied", hermesWriteFileEdit(t, "~"), false},
	}
	runHermesEditCases(t, sel, cases)
}

type hermesEditCase struct {
	name   string
	params json.RawMessage
	grant  bool
}

func runHermesEditCases(t *testing.T, sel func(json.RawMessage) (string, bool, bool), cases []hermesEditCase) {
	t.Helper()
	for _, tc := range cases {
		id, grant, ok := sel(tc.params)
		if !ok {
			t.Errorf("%s: want a selected option, got a protocol error", tc.name)
			continue
		}
		wantID := "deny"
		if tc.grant {
			wantID = "allow_once"
		}
		if grant != tc.grant || id != wantID {
			t.Errorf("%s: got grant=%v option=%q, want grant=%v option=%q", tc.name, grant, id, tc.grant, wantID)
		}
	}
}

func TestHermesEditDeniedWithoutUsableWorkdir(t *testing.T) {
	t.Parallel()
	for _, wd := range []string{"", "relative/dir", "/does/not/exist/at/all"} {
		sel := hermesPermissionSelector(true, false, wd, nil)
		if _, grant, _ := sel(hermesWriteFileEdit(t, "notes.md")); grant {
			t.Errorf("workdir %q: edit granted, want deny (fail closed)", wd)
		}
	}
}

func TestHermesEditYoloKeepsGenericPolicy(t *testing.T) {
	t.Parallel()
	if hermesPermissionSelector(true, true, t.TempDir(), nil) != nil {
		t.Fatal("yolo opt-in must keep the shared ACP policy, including for edits outside the workdir")
	}
}
