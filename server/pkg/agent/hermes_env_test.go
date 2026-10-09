package agent

import (
	"strings"
	"testing"
)

// childEnvValues returns every value of key in env, in order (a key can repeat).
func childEnvValues(env []string, key string) []string {
	var out []string
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		if k == key {
			out = append(out, v)
		}
	}
	return out
}

// LAB-151: a daemon started from an agent shell passed that shell's Hermes
// session/approval/kanban markers and the 1Password service-account token to
// every Hermes task. The inherited environment must not reach the child; the
// agent's own env (custom_env, HERMES_HOME overlay) must.
func TestHermesChildEnvDropsInheritedSessionAndSecretVars(t *testing.T) {
	// The daemon's own process environment, as when started from a Hermes agent shell.
	t.Setenv("HERMES_SINGLE_QUERY_SESSION", "1")
	t.Setenv("HERMES_EXEC_ASK", "1")
	t.Setenv("HERMES_INTERACTIVE", "1")
	t.Setenv("HERMES_KANBAN_DB", "x")
	t.Setenv("HERMES_KANBAN_BOARD", "x")
	t.Setenv("HERMES_KANBAN_WORKSPACE", "x")
	t.Setenv("HERMES_KANBAN_TASK", "x")
	t.Setenv("HERMES_SESSION_KEY", "x")
	t.Setenv("hermes_lowercase_marker", "x")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "x")
	t.Setenv("OP_CONNECT_TOKEN", "x")
	t.Setenv("OP_SESSION_myaccount", "x")
	t.Setenv("HERMES_HOME", "/inherited/home")
	t.Setenv("LAB151_UNRELATED", "kept")

	agentEnv := map[string]string{
		"HERMES_HOME":      "/task/overlay", // set by the daemon (overlay)
		"HERMES_KANBAN_DB": "/agent/own.db", // set in the agent's custom_env
		"OP_CONNECT_HOST":  "http://op",     // not a credential, custom_env
	}

	for _, yolo := range []bool{false, true} {
		env := hermesChildEnv(agentEnv, yolo)

		for _, key := range []string{
			"HERMES_SINGLE_QUERY_SESSION", "HERMES_EXEC_ASK", "HERMES_INTERACTIVE",
			"HERMES_KANBAN_BOARD", "HERMES_KANBAN_WORKSPACE", "HERMES_KANBAN_TASK",
			"HERMES_SESSION_KEY", "hermes_lowercase_marker",
			"OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "OP_SESSION_myaccount",
		} {
			if got := childEnvValues(env, key); len(got) != 0 {
				t.Errorf("yolo=%v: inherited %s leaked into the Hermes child env", yolo, key)
			}
		}
		// custom_env wins, and only the agent's value is present.
		if got := childEnvValues(env, "HERMES_KANBAN_DB"); len(got) != 1 || got[0] != "/agent/own.db" {
			t.Errorf("yolo=%v: HERMES_KANBAN_DB = %q, want only the custom_env value", yolo, got)
		}
		if got := childEnvValues(env, "HERMES_HOME"); len(got) != 1 || got[0] != "/task/overlay" {
			t.Errorf("yolo=%v: HERMES_HOME = %q, want only the task overlay", yolo, got)
		}
		if got := childEnvValues(env, "OP_CONNECT_HOST"); len(got) != 1 || got[0] != "http://op" {
			t.Errorf("yolo=%v: OP_CONNECT_HOST = %q, want the custom_env value", yolo, got)
		}
		// Ordinary inherited env still passes (PATH, HOME, ...).
		if got := childEnvValues(env, "LAB151_UNRELATED"); len(got) != 1 {
			t.Errorf("yolo=%v: unrelated inherited var dropped: %q", yolo, got)
		}
	}
}

// The allow-list keeps HERMES_HOME when the daemon did not set an overlay home
// (the only inherited HERMES_* Hermes needs to find its config).
func TestHermesChildEnvKeepsAllowListedInheritedHermesHome(t *testing.T) {
	t.Setenv("HERMES_HOME", "/daemon/home")
	t.Setenv("HERMES_SINGLE_QUERY_SESSION", "1")
	env := hermesChildEnv(nil, false)
	if got := childEnvValues(env, "HERMES_HOME"); len(got) != 1 || got[0] != "/daemon/home" {
		t.Errorf("HERMES_HOME = %q, want the inherited value", got)
	}
	if got := childEnvValues(env, "HERMES_SINGLE_QUERY_SESSION"); len(got) != 0 {
		t.Errorf("HERMES_SINGLE_QUERY_SESSION leaked: %q", got)
	}
}

func TestHermesInheritedEnvAllowed(t *testing.T) {
	cases := map[string]bool{
		"PATH":                        true,
		"HOME":                        true,
		"OP_CONNECT_HOST":             true,
		"HERMES_HOME":                 true,
		"HERMES_YOLO_MODE":            false,
		"HERMES_SINGLE_QUERY_SESSION": false,
		"HERMES_KANBAN_DB":            false,
		"Hermes_Kanban_Db":            false,
		"HERMES_ANYTHING_NEW":         false,
		"OP_SERVICE_ACCOUNT_TOKEN":    false,
		"op_service_account_token":    false,
		"OP_CONNECT_TOKEN":            false,
		"OP_SESSION_abc123":           false,
	}
	for key, want := range cases {
		if got := hermesInheritedEnvAllowed(key); got != want {
			t.Errorf("hermesInheritedEnvAllowed(%q) = %v, want %v", key, got, want)
		}
	}
}
