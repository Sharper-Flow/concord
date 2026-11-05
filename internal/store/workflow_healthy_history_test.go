package store

import "testing"

func TestHistoricalWorkflowHealthyBasisRequiresEveryComparablePredicate(t *testing.T) {
	predicate := historicalWorkflowPredicate{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: `{"kind":"exists","surface":"repo","subjects":["file"]}`}
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
		{name: "incomparable ok", mutate: func(_ *workflowHealthBasis, v []workflowHealthVerdict) {
			v[1].verdict.IncomparableWithApproved = true
		}},
		{name: "changed criterion", mutate: func(b *workflowHealthBasis, _ []workflowHealthVerdict) {
			p := b.history[2]["predicate:two"]
			p.OutcomePayload = `{"kind":"exists","surface":"repo","subjects":["changed"]}`
			b.history[2][p.PredicateID] = p
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			basis := workflowHealthBasis{version: 2, history: workflowContractPredicateHistoryData{
				1: {predicate.PredicateID: predicate, other.PredicateID: other},
				2: {predicate.PredicateID: predicate, other.PredicateID: other},
			}}
			candidates := []workflowHealthVerdict{
				{seq: 11, verdict: historicalWorkflowVerdict{ContractVersion: 2, PredicateID: predicate.PredicateID, VerdictKind: "ok"}},
				{seq: 12, verdict: historicalWorkflowVerdict{ContractVersion: 1, PredicateID: other.PredicateID, VerdictKind: "ok"}},
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

func TestHistoricalWorkflowPredicateHistoryCompatibility(t *testing.T) {
	history := workflowContractPredicateHistoryData{
		1: {"predicate:first": {PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"kind":"check","expected_result":"pass"}`}},
		2: {"predicate:first": {PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"expected_result":"pass","kind":"check"}`}},
	}
	verdict := historicalWorkflowVerdict{PredicateID: "predicate:first"}
	if !workflowPredicateHistoryCompatible(history, 1, 2, verdict.PredicateID, verdict) {
		t.Fatal("canonical-equivalent predicate payload was refused")
	}
	history[2]["predicate:first"] = historicalWorkflowPredicate{PredicateID: "predicate:first", OutcomeKind: "check", OutcomePayload: `{"kind":"check","expected_result":"fail"}`}
	if workflowPredicateHistoryCompatible(history, 1, 2, verdict.PredicateID, verdict) {
		t.Fatal("changed predicate payload remained compatible")
	}
}

func TestHistoricalWorkflowHealthyFrontierMigration142Interleavings(t *testing.T) {
	const work = "healthy-history-interleaving"
	for _, stage := range []struct {
		name  string
		count int
		want  int64
	}{
		{"first predicate", 1, 0},
		{"second unhealthy", 2, 0},
		{"both healthy", 3, 3},
		{"later unhealthy retains last healthy", 4, 3},
	} {
		t.Run(stage.name, func(t *testing.T) {
			c := evidenceCorpusForWork(t, work)
			parents := c.parents
			c.parents = func(t *testing.T, s *Store, guard string) {
				parents(t, s, guard)
				retirementUpgradeExec(t, s.db, `INSERT INTO fold_guard VALUES(1)`)
				retirementUpgradeExec(t, s.db, `DELETE FROM workflow_step_epochs WHERE work_id=?`, work)
				retirementUpgradeExec(t, s.db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,contract_version,'predicate:second',1,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1 AND ordinal=0`, work)
				retirementUpgradeExec(t, s.db, `DELETE FROM fold_guard`)
			}
			c.events = nil
			for i, verdict := range []historicalWorkflowVerdict{
				{ContractVersion: 1, PredicateID: "predicate:return-route", VerdictKind: "ok"},
				{ContractVersion: 1, PredicateID: "predicate:second", VerdictKind: "insufficient_evidence"},
				{ContractVersion: 1, PredicateID: "predicate:second", VerdictKind: "ok"},
				{ContractVersion: 1, PredicateID: "predicate:return-route", VerdictKind: "outcome_mismatch"},
			}[:stage.count] {
				c.events = append(c.events, corpusEvent([]string{"healthy:first", "healthy:missing", "healthy:all", "healthy:unhealthy"}[i], "workflow.verdict_recorded", work, verdict))
			}
			s := c.prefix(t, 141)
			advanceHistoricalPrefix(t, s.db, 142)
			read := func() int64 {
				t.Helper()
				var got int64
				if err := s.db.QueryRow(`SELECT last_healthy_seq FROM workflow_contracts WHERE work_id=? AND contract_version=1`, work).Scan(&got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			if got := read(); got != stage.want {
				t.Fatalf("migration 142 healthy frontier=%d; want %d", got, stage.want)
			}
			retirementUpgradeExec(t, s.db, `INSERT INTO fold_guard VALUES(1)`)
			retirementUpgradeExec(t, s.db, `UPDATE workflow_contracts SET last_healthy_seq=0 WHERE work_id=? AND contract_version=1`, work)
			retirementUpgradeExec(t, s.db, `DELETE FROM fold_guard`)
			if err := backfillWorkflowHealthySequences(t.Context(), s.db); err != nil {
				t.Fatal(err)
			}
			if got := read(); got != stage.want {
				t.Fatalf("independent historical backfill frontier=%d; want %d", got, stage.want)
			}
		})
	}
}
