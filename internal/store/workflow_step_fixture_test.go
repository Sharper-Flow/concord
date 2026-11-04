package store

import (
	"context"
	"testing"
	"time"
)

func stepFixtureActor() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
}

func cutoverTableSnapshot(t *testing.T, s *Store, table string) string {
	t.Helper()
	rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY 1,2") //nolint:gosec // test-owned closed table names.
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return string(mustJSONValue(snapshot))
}

func seedStepWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-s"), locatorProjectEvent("project-s"), locatorMembershipEvent("product-s", "project-s")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-s"): 0, VersionRef(SubjectProject, "project-s"): 0}}); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Step work","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-s","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}})
	if err != nil {
		t.Fatal(err)
	}
}
