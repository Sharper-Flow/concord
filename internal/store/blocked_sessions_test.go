package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #72: under burst concurrency the operator needs one bounded surface
// routing attention to the session waiting on a decision. Several challenges,
// a subset blocked, oldest-first routing, and active-only filtering.

func TestBlockedSessionsRoutesAttentionUnderBurst(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	t.Cleanup(func() { _ = s.Close() })
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-1"), locatorProjectEvent("project-1"), locatorMembershipEvent("product-1", "project-1")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-1"): 0, VersionRef(SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	// Five concurrent sessions (burst); three will end up blocked.
	sessions := []struct{ session, agent, worktree string }{
		{"session-a", "agent-a", "/repo/wt-a"},
		{"session-b", "agent-b", "/repo/wt-b"},
		{"session-c", "agent-c", "/repo/wt-c"},
		{"session-d", "agent-d", "/repo/wt-d"},
		{"session-e", "agent-e", "/repo/wt-e"},
	}
	for _, sess := range sessions {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`,
			"client-"+sess.session, "active", "human-1", `["product_read"]`, `["product-1"]`, `["project-1"]`, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	// Blocked: b (oldest), c, d. Excluded: a (active but expires passed), e (consumed).
	insertChallenge(t, s, "client-session-b", "session-b", "agent-b", "/repo/wt-b", []string{"product-1"}, now.Add(-40*time.Minute), now.Add(30*time.Minute), "active", "lifecycle")
	insertChallenge(t, s, "client-session-c", "session-c", "agent-c", "/repo/wt-c", []string{"product-1"}, now.Add(-20*time.Minute), now.Add(30*time.Minute), "active", "workflow_action")
	insertChallenge(t, s, "client-session-d", "session-d", "agent-d", "/repo/wt-d", []string{"product-1"}, now.Add(-5*time.Minute), now.Add(30*time.Minute), "active", "publication")
	// session-a: challenge already expired at read time.
	insertChallenge(t, s, "client-session-a", "session-a", "agent-a", "/repo/wt-a", []string{"product-1"}, now.Add(-2*time.Hour), now.Add(-1*time.Hour), "active", "intent")
	// session-e: consumed.
	insertChallenge(t, s, "client-session-e", "session-e", "agent-e", "/repo/wt-e", []string{"product-1"}, now.Add(-15*time.Minute), now.Add(30*time.Minute), "consumed", "intent")

	result, err := s.BlockedSessions(ctx, now, []string{"product-1"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 3 {
		t.Fatalf("sessions=%+v", result.Sessions)
	}
	want := []struct {
		session, consequence string
		age                  int64
	}{
		{"session-b", "lifecycle", 2400},
		{"session-c", "workflow_action", 1200},
		{"session-d", "publication", 300},
	}
	for i, expected := range want {
		got := result.Sessions[i]
		if got.SessionRef != expected.session || got.Consequence != expected.consequence || got.BlockAgeSec != expected.age {
			t.Fatalf("session[%d]=%+v want %+v", i, got, expected)
		}
		if got.Worktree == "" || got.AgentRef == "" {
			t.Fatalf("routing identity missing: %+v", got)
		}
	}

	// Product filter: a challenge for another Product is invisible.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-2"), locatorProjectEvent("project-2"), locatorMembershipEvent("product-2", "project-2")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-2"): 0, VersionRef(SubjectProject, "project-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	scope2, _ := json.Marshal([]string{"product-2"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`,
		"client-session-x", "active", "human-1", `["product_read"]`, string(scope2), `["project-1"]`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, s, "client-session-x", "session-x", "agent-x", "/repo/wt-x", []string{"product-2"}, now.Add(-30*time.Minute), now.Add(30*time.Minute), "active", "intent")
	filtered, err := s.BlockedSessions(ctx, now, []string{"product-1"}, 20)
	if err != nil || len(filtered.Sessions) != 3 {
		t.Fatalf("product filter broken: %d err=%v", len(filtered.Sessions), err)
	}
}

func insertChallenge(t *testing.T, s *Store, clientRef, sessionRef, agentRef, worktree string, products []string, issued, expires time.Time, status, consequence string) {
	t.Helper()
	consumed := any(nil)
	if status != "active" {
		consumed = expires
	}
	productJSON, _ := json.Marshal(products)
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_approval_challenges(challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		challengeRefFor(t, sessionRef, status, issued), clientRef, "human-1", sessionRef, agentRef, "/repo", worktree, string(productJSON), "sha256:"+strings.Repeat("2", 64), `{}`, `{}`, consequence, "sha256:"+strings.Repeat("1", 64), issued.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), status, consumed, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
}

func challengeRefFor(t *testing.T, sessionRef, status string, issued time.Time) string {
	t.Helper()
	sum := sha256.Sum256([]byte(sessionRef + status + issued.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}
