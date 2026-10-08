package agent

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// Multica used to force HERMES_YOLO_MODE=1 on every Hermes task and grant every
// permission prompt, so a Hermes agent could run any dangerous command with
// nobody asked. Yolo is now opt-in per agent (custom_env), and without it the
// daemon denies Hermes' dangerous-command prompts. These tests pin both halves.

func TestHermesYoloOptIn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nil env", nil, false},
		{"absent", map[string]string{"OTHER": "1"}, false},
		{"one", map[string]string{"HERMES_YOLO_MODE": "1"}, true},
		{"true mixed case and spaces", map[string]string{"HERMES_YOLO_MODE": " True "}, true},
		{"yes", map[string]string{"HERMES_YOLO_MODE": "yes"}, true},
		{"on", map[string]string{"HERMES_YOLO_MODE": "on"}, true},
		{"zero", map[string]string{"HERMES_YOLO_MODE": "0"}, false},
		{"false", map[string]string{"HERMES_YOLO_MODE": "false"}, false},
		{"empty", map[string]string{"HERMES_YOLO_MODE": ""}, false},
		{"different key case is not the Hermes switch", map[string]string{"hermes_yolo_mode": "1"}, false},
	}
	for _, tc := range cases {
		if got := hermesYoloOptIn(tc.env); got != tc.want {
			t.Errorf("%s: hermesYoloOptIn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func yoloEntries(env []string) []string {
	var out []string
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == hermesYoloEnvKey {
			out = append(out, e)
		}
	}
	return out
}

// The daemon's own environment must not decide yolo for every agent: an
// inherited HERMES_YOLO_MODE is dropped unless the agent opted in.
func TestHermesChildEnvYolo(t *testing.T) {
	t.Setenv("HERMES_YOLO_MODE", "1") // e.g. exported in the shell that started the daemon

	if got := yoloEntries(hermesChildEnv(map[string]string{"A": "b"}, false)); len(got) != 0 {
		t.Fatalf("yolo off: inherited value leaked into child env: %v", got)
	}
	if got := yoloEntries(hermesChildEnv(map[string]string{"HERMES_YOLO_MODE": "0"}, false)); len(got) != 0 {
		t.Fatalf("yolo off: falsy custom_env value should be removed, got %v", got)
	}
	got := yoloEntries(hermesChildEnv(map[string]string{"HERMES_YOLO_MODE": "true"}, true))
	if len(got) != 1 || got[0] != "HERMES_YOLO_MODE=1" {
		t.Fatalf("yolo on: want exactly HERMES_YOLO_MODE=1, got %v", got)
	}
}

func TestHermesPermissionSelectorScope(t *testing.T) {
	t.Parallel()
	if hermesPermissionSelector(true, true, nil) != nil {
		t.Error("yolo opt-in must keep the shared ACP policy (nil selector)")
	}
	if hermesPermissionSelector(false, false, nil) != nil {
		t.Error("non-builtin hermes-family runtimes (jcode) must keep the shared ACP policy")
	}
	if hermesPermissionSelector(true, false, nil) == nil {
		t.Error("builtin Hermes without yolo must get the guarded selector")
	}
}

// hermesCommandApprovalOptions is the option list Hermes' ACP adapter sends
// for a dangerous-command approval (acp_adapter/permissions.py).
const hermesCommandApprovalOptions = `[{"optionId":"allow_once","kind":"allow_once","name":"Allow once"},{"optionId":"allow_session","kind":"allow_always","name":"Allow for session"},{"optionId":"allow_always","kind":"allow_always","name":"Allow always"},{"optionId":"deny","kind":"reject_once","name":"Deny"},{"optionId":"deny_always","kind":"reject_always","name":"Deny always"}]`

func TestHermesGuardedPermissionReplies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		toolCall string
		options  string
		wantErr  bool
		wantID   string
	}{
		{
			name:     "dangerous command is denied",
			toolCall: `{"toolCallId":"perm-check-1","title":"recursive delete: rm -rf /tmp/x","kind":"execute","status":"pending"}`,
			options:  hermesCommandApprovalOptions,
			wantID:   "deny",
		},
		{
			name:     "smart-denied once-only command prompt is denied",
			toolCall: `{"toolCallId":"perm-check-2","title":"curl | sh","kind":"execute"}`,
			options:  `[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"deny","kind":"reject_once"}]`,
			wantID:   "deny",
		},
		{
			name:     "missing kind fails closed",
			toolCall: `{"toolCallId":"x","title":"something"}`,
			options:  hermesCommandApprovalOptions,
			wantID:   "deny",
		},
		{
			name:     "unknown kind fails closed",
			toolCall: `{"toolCallId":"x","title":"fetch","kind":"fetch"}`,
			options:  hermesCommandApprovalOptions,
			wantID:   "deny",
		},
		{
			name:     "command prompt without a reject option returns a protocol error, never a grant",
			toolCall: `{"toolCallId":"x","title":"rm -rf","kind":"execute"}`,
			options:  `[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"allow_session","kind":"allow_always"}]`,
			wantErr:  true,
		},
		{
			name:     "file edit approval keeps the single-use grant",
			toolCall: `{"toolCallId":"edit-approval-1","title":"Approve edit: reply.md","kind":"edit","status":"pending"}`,
			options:  `[{"optionId":"allow_once","kind":"allow_once","name":"Allow edit"},{"optionId":"deny","kind":"reject_once","name":"Deny"}]`,
			wantID:   "allow_once",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &bufferWriter{}
			c := &hermesClient{
				cfg:              Config{Logger: slog.Default()},
				stdin:            w,
				pending:          make(map[int]*pendingRPC),
				selectPermission: hermesPermissionSelector(true, false, slog.Default()),
			}
			c.handleLine(`{"jsonrpc":"2.0","id":42,"method":"session/request_permission","params":{"sessionId":"ses_1","toolCall":` + tc.toolCall + `,"options":` + tc.options + `}}`)

			var resp struct {
				ID     int `json:"id"`
				Result *struct {
					Outcome struct {
						Outcome  string `json:"outcome"`
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(w.String())), &resp); err != nil {
				t.Fatalf("reply is not valid JSON: %q err=%v", w.String(), err)
			}
			if resp.ID != 42 {
				t.Errorf("id: got %d, want 42", resp.ID)
			}
			if tc.wantErr {
				if resp.Error == nil || resp.Result != nil {
					t.Fatalf("want a JSON-RPC error reply, got %q", w.String())
				}
				return
			}
			if resp.Result == nil {
				t.Fatalf("want a selected outcome, got %q", w.String())
			}
			if resp.Result.Outcome.Outcome != "selected" || resp.Result.Outcome.OptionID != tc.wantID {
				t.Errorf("outcome: got %+v, want selected %q", resp.Result.Outcome, tc.wantID)
			}
		})
	}
}
