package store

import (
	"testing"
)

// The newest registered break-fix definition declares the CON-890 acceptance
// oracle on record_worker_job, so the authoring route refuses an oracle-free
// job on that pin. Historical pins keep accepting oracle-free jobs; the
// oracle-capability boundary is the declared action member, never a mutable
// behavior flag on a released definition.
func TestOwnerOracleRequiredOnOracleCapablePin(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-required-on-pin"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	defer fixture.store.Close()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if !workflowWorkerJobsActive(registered.Definition) {
		t.Fatalf("the newest break-fix definition %d lost the worker-job lifecycle", registered.Definition.Version)
	}
	err = recordWorkerJobActionForTest(t, fixture.store, workID, fixture.owner, map[string]any{
		"job_id":             "job:oracle-required",
		"objective":          "carry the oracle obligation",
		"stopping_condition": "the oracle controls pass",
		"ready":              false,
	})
	if err == nil {
		t.Fatalf("record_worker_job accepted an oracle-free job on break-fix version %d; the oracle-capable pin must require the acceptance oracle", registered.Definition.Version)
	}
}
