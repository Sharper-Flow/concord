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
// routing attention to the session waiting on a decision. Several grants, a
// subset blocked, oldest-first routing, and active-only filtering.

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
	grants := map[string]string{}
	for _, sess := range sessions {
		if _, err := s.DB().Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`,
			"client-"+sess.session, "active", "human-1", `["product_read"]`, `["product-1"]`, `["project-1"]`, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`,
			"client-"+sess.session, "key-"+sess.session, seedKey(t, sess.session), "active", now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		grantRef := strings.Repeat("g", 56) + sess.session[len(sess.session)-8:]
		tokenHash := sha256Hash(t, "token:"+sess.session)
		scopeJSON, _ := json.Marshal([]string{"product-1"})
		if _, err := s.DB().Exec(`INSERT INTO agent_grants(grant_ref,grant_hash,principal_ref,client_ref,session_ref,agent_ref,directory,worktree,client_version,client_key_id,surface_version,envelope_version,manifest_digest,capabilities_json,product_scope_json,project_scope_json,issued_at,expires_at,max_uses,used_count,scope_version,scope_snapshot_json,candidate_products_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			grantRef, tokenHash, "human-1", "client-"+sess.session, sess.session, sess.agent, "/repo", sess.worktree, "3.3.0", "key-"+sess.session, "3.3.0", "1.0", "sha256:"+strings.Repeat("0", 64), `["product_read"]`, string(scopeJSON), `["project-1"]`, now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), 100, 0, "v1", "{}", "[]"); err != nil {
			t.Fatal(err)
		}
		grants[sess.session] = grantRef
	}

	// Blocked: b (oldest), c, d. Excluded: a (active but expires passed), e (consumed).
	insertChallenge(t, s, grants["session-b"], now.Add(-40*time.Minute), now.Add(30*time.Minute), "active", "lifecycle")
	insertChallenge(t, s, grants["session-c"], now.Add(-20*time.Minute), now.Add(30*time.Minute), "active", "workflow_action")
	insertChallenge(t, s, grants["session-d"], now.Add(-5*time.Minute), now.Add(30*time.Minute), "active", "publication")
	// session-a: challenge already expired at read time.
	insertChallenge(t, s, grants["session-a"], now.Add(-2*time.Hour), now.Add(-1*time.Hour), "active", "intent")
	// session-e: consumed.
	insertChallenge(t, s, grants["session-e"], now.Add(-15*time.Minute), now.Add(30*time.Minute), "consumed", "intent")

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

	// Product filter: another Product's grant is invisible.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-2"), locatorProjectEvent("project-2"), locatorMembershipEvent("product-2", "project-2")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-2"): 0, VersionRef(SubjectProject, "project-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	scope2, _ := json.Marshal([]string{"product-2"})
	if _, err := s.DB().Exec(`INSERT INTO agent_grants(grant_ref,grant_hash,principal_ref,client_ref,session_ref,agent_ref,directory,worktree,client_version,client_key_id,surface_version,envelope_version,manifest_digest,capabilities_json,product_scope_json,project_scope_json,issued_at,expires_at,max_uses,used_count,scope_version,scope_snapshot_json,candidate_products_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		strings.Repeat("o", 56)+"grant-ot", sha256Hash(t, "token:session-x"), "human-1", "client-session-a", "session-x", "agent-x", "/repo", "/repo/wt-x", "3.3.0", "key-session-a", "3.3.0", "1.0", "sha256:"+strings.Repeat("0", 64), `["product_read"]`, string(scope2), `["project-1"]`, now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), 100, 0, "v1", "{}", "[]"); err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, s, strings.Repeat("o", 56)+"grant-ot", now.Add(-30*time.Minute), now.Add(30*time.Minute), "active", "intent")
	filtered, err := s.BlockedSessions(ctx, now, []string{"product-1"}, 20)
	if err != nil || len(filtered.Sessions) != 3 {
		t.Fatalf("product filter broken: %d err=%v", len(filtered.Sessions), err)
	}
}

func insertChallenge(t *testing.T, s *Store, grantRef string, issued, expires time.Time, status, consequence string) {
	t.Helper()
	consumed := any(nil)
	if status != "active" {
		consumed = expires
	}
	_, err := s.DB().Exec(`INSERT INTO agent_approval_challenges(challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		challengeRefFor(t, grantRef, status, issued), grantRef, "sha256:"+strings.Repeat("2", 64), `{}`, `{}`, consequence, "sha256:"+strings.Repeat("1", 64), issued.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), status, consumed)
	if err != nil {
		t.Fatal(err)
	}
}

func sha256Hash(t *testing.T, value string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func seedKey(t *testing.T, seed string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("key:" + seed))
	return sum[:]
}

func challengeRefFor(t *testing.T, grantRef, status string, issued time.Time) string {
	t.Helper()
	sum := sha256.Sum256([]byte(grantRef + status + issued.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}
