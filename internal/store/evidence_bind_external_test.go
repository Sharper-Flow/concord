package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEvidenceBindExternalProducerRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, kind, subject, producer, run, watermark string
	}{
		{"merge", "verification", "https://github.com/Sharper-Flow/concord/commit/874e7db8011972a2a1e36892e2b81c4708ae38b4", "github", "https://github.com/Sharper-Flow/concord/pull/2317", "874e7db8011972a2a1e36892e2b81c4708ae38b4"},
		{"ci", "verification", "https://github.com/Sharper-Flow/concord/actions/runs/111", "github/actions", "https://github.com/Sharper-Flow/concord/actions/runs/111", "github-run:111"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := seedMandateRecoveryItem(t, "external-evidence-"+tc.name)
			payload, err := json.Marshal(map[string]string{
				"evidence_kind": tc.kind, "immutable_subject_ref": tc.subject,
				"producer_id": tc.producer, "producer_run_ref": tc.run,
				"producer_watermark": tc.watermark,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = runVerdictAction(t, s, "external-evidence-"+tc.name, "bind_evidence", payload)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.EffectPossible {
				t.Fatalf("want a no-effect invariant refusal, got %v", err)
			}
			for _, part := range []string{"evidence", "otherwise admitted", "operation_id", "request_id", "principal", "producer_run_ref", "producer_watermark", "producer_id", "omit producer_id"} {
				if !strings.Contains(failure.RecoveryAction, part) {
					t.Errorf("recovery %q does not name %q", failure.RecoveryAction, part)
				}
			}
		})
	}
}
