//go:build !windows

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hermesYoloStub records the HERMES_YOLO_MODE it was started with, then — like
// the real Hermes ACP adapter without yolo — asks permission for a dangerous
// command mid-turn and records the daemon's reply.
const hermesYoloStub = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_yolo"}}\n' "$id"
      ;;
    *'"method":"session/prompt"'*)
      printf 'yolo=%s\n' "${HERMES_YOLO_MODE-unset}" > "$STUB_OUT.env"
      printf '{"jsonrpc":"2.0","id":9001,"method":"session/request_permission","params":{"sessionId":"ses_yolo","toolCall":{"toolCallId":"perm-check-1","title":"recursive delete: rm -rf /tmp/x","kind":"execute","status":"pending"},"options":[{"optionId":"allow_once","kind":"allow_once","name":"Allow once"},{"optionId":"allow_session","kind":"allow_always","name":"Allow for session"},{"optionId":"allow_always","kind":"allow_always","name":"Allow always"},{"optionId":"deny","kind":"reject_once","name":"Deny"},{"optionId":"deny_always","kind":"reject_always","name":"Deny always"}]}}\n'
      IFS= read -r reply
      printf '%s\n' "$reply" > "$STUB_OUT.perm"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id"
      exit 0
      ;;
  esac
done
`

func runHermesYoloTurn(t *testing.T, customEnv map[string]string) (envLine, permReply string) {
	t.Helper()
	dir := t.TempDir()
	fakePath := filepath.Join(dir, "hermes")
	writeTestExecutable(t, fakePath, []byte(hermesYoloStub))
	out := filepath.Join(dir, "out")

	env := map[string]string{"STUB_OUT": out}
	for k, v := range customEnv {
		env[k] = v
	}
	backend, err := New("hermes", Config{
		ExecutablePath: fakePath,
		Env:            env,
		Logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		BuiltinRuntime: true,
	})
	if err != nil {
		t.Fatalf("new hermes backend: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "ping", ExecOptions{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for range session.Messages {
	}
	<-session.Result

	e, err := os.ReadFile(out + ".env")
	if err != nil {
		t.Fatalf("stub did not record its env: %v", err)
	}
	p, err := os.ReadFile(out + ".perm")
	if err != nil {
		t.Fatalf("stub did not record the permission reply: %v", err)
	}
	return strings.TrimSpace(string(e)), strings.TrimSpace(string(p))
}

// Default: no opt-in. Even with HERMES_YOLO_MODE=1 exported in the daemon's
// own environment, Hermes starts without yolo and its dangerous-command prompt
// is denied.
func TestHermesExecuteYoloOffByDefaultDeniesDangerousCommand(t *testing.T) {
	t.Setenv("HERMES_YOLO_MODE", "1")
	envLine, reply := runHermesYoloTurn(t, nil)
	if envLine != "yolo=unset" {
		t.Errorf("hermes child env: got %q, want yolo=unset", envLine)
	}
	if !strings.Contains(reply, `"id":9001`) || !strings.Contains(reply, `"optionId":"deny"`) {
		t.Errorf("permission reply: got %q, want the offered deny option for request 9001", reply)
	}
}

// Opt-in through the agent's custom_env: same behaviour as before the change —
// yolo on, and the generic policy (session-scoped grant) for any prompt.
func TestHermesExecuteYoloOptInKeepsPreviousBehaviour(t *testing.T) {
	envLine, reply := runHermesYoloTurn(t, map[string]string{"HERMES_YOLO_MODE": "1"})
	if envLine != "yolo=1" {
		t.Errorf("hermes child env: got %q, want yolo=1", envLine)
	}
	if !strings.Contains(reply, `"optionId":"allow_session"`) {
		t.Errorf("permission reply: got %q, want the generic policy's allow_session", reply)
	}
}
