package store

import (
	"database/sql"
	"testing"
	"time"
)

func terminalStepsMigrationFixture(t *testing.T) *sql.DB {
	t.Helper()
	s := openTemp(t)
	const work = "work-upgrade-to"
	seedWork(t, s, work)
	actor := WorkflowActor{PrincipalRef: "principal:fixture", ClientRef: "client:fixture", AgentRef: "agent:fixture", SessionRef: "session:fixture", ActorClass: ActorAgent}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := initializeWorkflowRawTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: work, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s.db
}
