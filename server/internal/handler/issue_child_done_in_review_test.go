package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// putWorkspaceChildDone changes the child_done workspace default as an owner.
func putWorkspaceChildDone(t *testing.T, body map[string]any) []workspaceSystemWakeupResponse {
	t.Helper()
	req := withURLParam(newRequest("PUT", "/api/system-wakeups/child_done", body), "rule", "child_done")
	req.Header.Set("X-User-ID", testUserID)
	rec := httptest.NewRecorder()
	testHandler.UpdateWorkspaceSystemWakeup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set workspace default %v: %d %s", body, rec.Code, rec.Body.String())
	}
	var out []workspaceSystemWakeupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("workspace defaults = %s (%v)", rec.Body.String(), err)
	}
	return out
}

// countInReview turns the workspace setting on for one test and restores the
// workspace's settings afterwards.
func countInReview(t *testing.T) {
	t.Helper()
	var settings []byte
	dbfx.QueryRow(t, "SELECT settings FROM workspace WHERE id=$1", testWorkspaceID).Scan(&settings)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "UPDATE workspace SET settings=$2 WHERE id=$1", testWorkspaceID, settings)
	})
	if got := putWorkspaceChildDone(t, map[string]any{"count_in_review": true}); !got[0].CountInReview {
		t.Fatalf("count_in_review not reported: %+v", got)
	}
}

// Agents finish at In Review and done stays human. By default that is not
// delivered: the parent waits for done, as before.
func TestChildDoneInReviewIgnoredByDefault(t *testing.T) {
	fx := newChildDoneFixture(t, "in_progress")
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", handlerTestAgentID(t))
	second := createUnstagedChild(t, fx.parent.ID, "in_progress")

	updateChildStatus(t, fx.child.ID, "in_review")
	updateChildStatus(t, second.ID, "in_review")
	runWakeupTick(t)
	if entries := childDoneEntries(t, fx.parent.ID); len(entries) != 0 {
		t.Fatalf("in_review woke the parent with the setting off: %+v", entries)
	}
}

// With the workspace setting on, the parent's agent is woken once when the
// last sub-issue reaches In Review, a human moving them on to done is not a
// second delivery, and sending one back for rework and re-delivering is.
func TestChildDoneCountsInReviewWhenSet(t *testing.T) {
	countInReview(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
	second := createUnstagedChild(t, fx.parent.ID, "in_progress")

	updateChildStatus(t, fx.child.ID, "in_review")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 0 {
		t.Fatalf("one of two sub-issues in review fired %d entries", got)
	}
	updateChildStatus(t, second.ID, "in_review")
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "woke" || entries[0].TargetID != agentID || entries[0].Total != 2 {
		t.Fatalf("entries = %+v, want one wake of the parent's agent", entries)
	}
	runs := childDoneRuns(t, fx.parent.ID)
	if len(runs) != 1 || !strings.Contains(runs[0].Note, `"all":true`) || !strings.Contains(runs[0].Note, `"in_review":2`) {
		t.Fatalf("runs = %+v, want one run whose facts say both are in review", runs)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1`, fx.parent.ID)

	updateChildStatus(t, fx.child.ID, "done")
	updateChildStatus(t, second.ID, "done")
	runWakeupTick(t)
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 1 {
		t.Fatalf("accepting reviewed sub-issues woke the parent again: %d entries", got)
	}

	updateChildStatus(t, second.ID, "in_progress")
	updateChildStatus(t, second.ID, "in_review")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 2 {
		t.Fatalf("re-delivery after rework: %d entries, want 2", got)
	}
}

// The setting is the platform rule's reading only. A person's
// --until-children-done condition still waits for closed sub-issues.
func TestChildDoneInReviewLeavesConditionRules(t *testing.T) {
	countInReview(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	svc := service.IssueWakeupService{Tasks: testHandler.TaskService}
	if _, err := svc.Create(context.Background(), parseUUID(fx.parent.ID), parseUUID(testUserID), pgtype.UUID{}, service.WakeupInput{
		AgentID: agentID, Instruction: "Summarize the children", Kind: "event", Condition: json.RawMessage(`{"type":"children_done"}`),
	}); err != nil {
		t.Fatal(err)
	}
	updateChildStatus(t, fx.child.ID, "in_review")
	runWakeupTick(t)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND context->>'wakeup_system' IS NULL AND context->>'wakeup_id' IS NOT NULL`, fx.parent.ID); n != 0 {
		t.Fatalf("the person's children_done rule fired on in_review: %d runs", n)
	}
}

// count_in_review is a workspace setting; one issue cannot change it.
func TestChildDoneInReviewRejectedPerIssue(t *testing.T) {
	fx := newChildDoneFixture(t, "in_progress")
	if w := putChildDoneRule(t, fx.parent.ID, map[string]any{"count_in_review": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("per-issue count_in_review: %d %s, want 400", w.Code, w.Body.String())
	}
}
