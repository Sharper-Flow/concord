package main

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerReceiptReconciliationKeepsProducerSubjectAndExactContent(t *testing.T) {
	stored := store.Event{Kind: store.WorkerCompleted, Payload: json.RawMessage(`{"evidence":[{"obligation":"verification_commands","detail":"actual C proof","oracle_receipt":{"subject_commit":"cccccccccccccccccccccccccccccccccccccccc","run_ref":"worktree_verify:actual-c","result":"pass"}}]}`)}
	for _, tc := range []struct {
		name, receipt string
		match         bool
	}{
		{"omitted subject", `{"run_ref":"worktree_verify:actual-c","result":"pass"}`, true},
		{"same subject", `{"subject_commit":"cccccccccccccccccccccccccccccccccccccccc","run_ref":"worktree_verify:actual-c","result":"pass"}`, true},
		{"forged B", `{"subject_commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","run_ref":"worktree_verify:actual-c","result":"pass"}`, false},
		{"other run", `{"run_ref":"worktree_verify:other","result":"pass"}`, false},
		{"changed result", `{"run_ref":"worktree_verify:actual-c","result":"fail"}`, false},
		{"unknown member", `{"run_ref":"worktree_verify:actual-c","result":"pass","extra":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := stored
			requested.Payload = json.RawMessage(`{"evidence":[{"obligation":"verification_commands","detail":"actual C proof","oracle_receipt":` + tc.receipt + `}]}`)
			merged, err := mergeWorkerTerminalProducerFields(stored, requested)
			if err != nil {
				t.Fatal(err)
			}
			if sameWorkerEvidence(stored, merged) != tc.match {
				t.Fatalf("exact replay match=%t, want %t", sameWorkerEvidence(stored, merged), tc.match)
			}
		})
	}
}
