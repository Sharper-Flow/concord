package store

import (
	"reflect"
	"testing"
)

func TestBudgetStateAbsentFromCurrentCorrectionAndAdmission(t *testing.T) {
	for _, target := range []struct {
		typ    reflect.Type
		fields []string
	}{
		{reflect.TypeOf(WorkflowCorrectionContext{}), []string{"AttemptCount", "AttemptLimit", "Escalated"}},
		{reflect.TypeOf(WorkflowAdmissionState{}), []string{"CorrectionEscalated", "NonProgressAttempts", "RetryConvergence", "FundedChecksRepairReviews"}},
		{reflect.TypeOf(WorkflowAdmissionDecision{}), []string{"ConvergenceRequired"}},
	} {
		for _, field := range target.fields {
			if _, exists := target.typ.FieldByName(field); exists {
				t.Errorf("%s still carries budget state %s", target.typ.Name(), field)
			}
		}
	}
}
