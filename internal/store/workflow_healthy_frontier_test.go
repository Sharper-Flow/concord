package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowHealthyFrontierFollowsCurrentVerdictSet(t *testing.T) {
	const work = "healthy-verdict-frontier"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{})
	defer s.Close()
	var primary string
	if err := s.db.QueryRow(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1 AND ordinal=0`, work).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
 SELECT work_id,contract_version,'predicate:second',1,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1 AND ordinal=0;
DELETE FROM write_guard`, work); err != nil {
		t.Fatal(err)
	}
	var healthy int64
	base := s
	c := historicalFactCorpus{parents: func(t *testing.T, target *Store, guard string) {
		copyFixtureDataToPrefix(t, base, target.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts", "workflow_contract_predicates"}, map[string]map[string]string{"workflow_instances": {"definition_version": "26"}, "workflow_contracts": {"definition_version": "26"}}, guard)
	}}
	for i, verdict := range []workflowVerdictRecordedPayload{
		{ContractVersion: 1, PredicateID: primary, VerdictKind: "ok"},
		{ContractVersion: 1, PredicateID: "predicate:second", VerdictKind: "insufficient_evidence"},
		{ContractVersion: 1, PredicateID: "predicate:second", VerdictKind: "ok"},
		{ContractVersion: 1, PredicateID: primary, VerdictKind: "outcome_mismatch"},
	} {
		raw, err := json.Marshal(verdict)
		if err != nil {
			t.Fatal(err)
		}
		c.events = append(c.events, corpusEvent([]string{"healthy:first", "healthy:missing", "healthy:all", "healthy:unhealthy"}[i], WorkflowVerdictRecorded, work, json.RawMessage(raw)))
		if i == 2 {
			healthy = int64(i + 1)
		}
	}
	s = c.prefix(t, 141)
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	got, err := workflowLatestComparableHealthySequence(t.Context(), s.db, work, 1)
	if err != nil || got != healthy {
		t.Fatalf("healthy frontier = %d, %v; want last simultaneous healthy set at %d", got, err, healthy)
	}
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"healthy-frontier": func() (any, error) { return workflowLatestComparableHealthySequence(t.Context(), s.db, work, 1) },
	})
	got, err = workflowLatestComparableHealthySequence(t.Context(), s.db, work, 1)
	if err != nil || got != healthy {
		t.Fatalf("backfill frontier = %d, %v; want %d", got, err, healthy)
	}
}

func TestWorkflowHealthyFrontierWrittenByCurrentVerdict(t *testing.T) {
	const work = "healthy-frontier-current-writer"
	f := qBuildAcceptance(t, work, "", true)
	defer f.store.Close()
	var expected int64
	if err := f.store.db.QueryRow(`SELECT MAX(verdict_seq) FROM workflow_predicate_verdicts WHERE work_id=?`, work).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	got, err := workflowLatestComparableHealthySequence(t.Context(), f.store.db, work, 1)
	if err != nil || expected == 0 || got != expected {
		t.Fatalf("current verdict writer frontier = %d, %v; want %d", got, err, expected)
	}
}

func TestWorkflowHealthyBasisRequiresEveryComparablePredicate(t *testing.T) {
	predicate := WorkflowReadPredicate{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: `{"kind":"exists","surface":"repo","subjects":["file"]}`}
	other := predicate
	other.PredicateID = "predicate:two"
	for _, test := range []struct {
		name   string
		mutate func(*workflowHealthBasis, []workflowHealthVerdict)
		want   int64
	}{
		{name: "compatible predecessor", want: 12},
		{name: "missing predicate", mutate: func(_ *workflowHealthBasis, v []workflowHealthVerdict) {
			v[1].verdict.PredicateID = "predicate:unrelated"
		}},
		{name: "unhealthy predicate", mutate: func(_ *workflowHealthBasis, v []workflowHealthVerdict) {
			v[1].verdict.VerdictKind = "insufficient_evidence"
		}},
		{name: "incomparable ok", mutate: func(_ *workflowHealthBasis, v []workflowHealthVerdict) { v[1].verdict.IncomparableWithApproved = true }},
		{name: "changed criterion", mutate: func(b *workflowHealthBasis, _ []workflowHealthVerdict) {
			p := b.history[2]["predicate:two"]
			p.OutcomePayload = `{"kind":"exists","surface":"repo","subjects":["changed"]}`
			b.history[2][p.PredicateID] = p
		}},
		{name: "changed definition", mutate: func(b *workflowHealthBasis, _ []workflowHealthVerdict) {
			pin := b.pins[2]
			pin.definitionRef = "workflow.changed"
			b.pins[2] = pin
		}},
		{name: "complete correction cutoff", mutate: func(b *workflowHealthBasis, _ []workflowHealthVerdict) { b.cutoff = 12 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			basis := workflowHealthBasis{version: 2, history: workflowContractPredicateHistoryData{
				1: {predicate.PredicateID: predicate, other.PredicateID: other},
				2: {predicate.PredicateID: predicate, other.PredicateID: other},
			}, pins: workflowContractDefinitionPinsData{
				1: {definitionRef: "workflow.test", definitionDigest: testManifestDigest},
				2: {definitionRef: "workflow.test", definitionDigest: testManifestDigest},
			}}
			candidates := []workflowHealthVerdict{
				{seq: 11, verdict: workflowVerdictRecordedPayload{ContractVersion: 2, PredicateID: predicate.PredicateID, VerdictKind: "ok"}},
				{seq: 12, verdict: workflowVerdictRecordedPayload{ContractVersion: 1, PredicateID: other.PredicateID, VerdictKind: "ok"}},
			}
			if test.mutate != nil {
				test.mutate(&basis, candidates)
			}
			if got := basis.healthySequence(candidates); got != test.want {
				t.Fatalf("healthy sequence = %d; want %d", got, test.want)
			}
		})
	}
}

func TestWorkflowHealthyFrontierContractChangeRecomputesCurrentSet(t *testing.T) {
	const work = "healthy-frontier-contract-change"
	f := qBuildAcceptance(t, work, "", true)
	defer f.store.Close()
	s := f.store
	before := workerFactsQueryRows(t, s, []string{`SELECT last_healthy_seq FROM workflow_contracts WHERE work_id='healthy-frontier-contract-change'`})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE workflow_predicate_verdicts SET payload=json_set(payload,'$.incomparable_with_approved',json('true')) WHERE work_id=?`, work); err != nil {
		t.Fatal(err)
	}
	if err := refreshWorkflowHealthySequence(t.Context(), tx, work, 1, false); err != nil {
		t.Fatal(err)
	}
	var retained int64
	if err := tx.QueryRow(`SELECT last_healthy_seq FROM workflow_contracts WHERE work_id=?`, work).Scan(&retained); err != nil || retained == 0 {
		t.Fatalf("unhealthy verdict erased prior frontier: %d, %v", retained, err)
	}
	if err := refreshWorkflowHealthySequence(t.Context(), tx, work, 1, true); err != nil {
		t.Fatal(err)
	}
	var reset int64
	if err := tx.QueryRow(`SELECT last_healthy_seq FROM workflow_contracts WHERE work_id=?`, work).Scan(&reset); err != nil || reset != 0 {
		t.Fatalf("contract recomputation kept obsolete health: %d, %v", reset, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := workerFactsQueryRows(t, s, []string{`SELECT last_healthy_seq FROM workflow_contracts WHERE work_id='healthy-frontier-contract-change'`}); !reflect.DeepEqual(before, after) {
		t.Fatalf("rolled-back health change persisted: %v -> %v", before, after)
	}
}
