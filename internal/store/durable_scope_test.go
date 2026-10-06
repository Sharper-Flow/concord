package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// pinReaderSnapshot holds a read transaction from a separate connection, the
// way another Concord process does. While it is open, a CD-0050 barrier
// reports busy; an ordinary write must still be acknowledged.
func pinReaderSnapshot(t *testing.T, s *Store) {
	t.Helper()
	reader, err := sql.Open(driverName, "file:"+s.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	readTx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readTx.Rollback() })
	var probe int
	if err := readTx.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&probe); err != nil {
		t.Fatalf("reader snapshot: %v", err)
	}
	if err := s.appendSeedEvent(context.Background()); err != nil {
		t.Fatalf("commit after the pinned snapshot: %v", err)
	}
}

// TestOrdinaryWritesOutsideBarrierEnumeration pins CD-0050 D3: writes outside
// the consequential families carry no durability barrier, so a concurrent
// reader in another process cannot turn their committed effect into a refusal.
func TestOrdinaryWritesOutsideBarrierEnumeration(t *testing.T) {
	t.Run("orchestrator identity assertion", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		pinReaderSnapshot(t, s)
		_, err := s.RecordOrchestratorIdentityAssertion(context.Background(), "identity-under-reader", s.Now(), OrchestratorIdentityAssertion{
			Type: "orchestrator", Version: "1", RulesetDigest: "sha256:" + strings.Repeat("a", 64),
			Sources:   []OrchestratorArtifactSource{{Kind: "orchestrator_definition", Path: "/tmp/orchestrator.md", SHA256: strings.Repeat("b", 64)}},
			ProductID: "prod", WorkID: "work", PrincipalRef: "principal/orchestrator", ClientRef: "client/session", AgentRef: "agent/orchestrator", SessionRef: "session/prod",
		})
		if err != nil {
			t.Fatalf("identity assertion under a pinned reader: %v", err)
		}
	})
	t.Run("workflow staleness observation", func(t *testing.T) {
		t.Parallel()
		s, _ := seedCompletionGateCase(t, "staleness-reader", completionGateCase{requiredEvidence: []string{"verification", "review"}})
		pinReaderSnapshot(t, s)
		payload := json.RawMessage(`{"staleness_rule_id":"staleness:warning","observed_drift":{"severity":"warning","drifted":true}}`)
		if err := AppendWorkflowStalenessObservation(context.Background(), s, "staleness-reader:observation", "staleness-reader", "actor:staleness", testManifestDigest, payload, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("staleness observation under a pinned reader: %v", err)
		}
	})
}
