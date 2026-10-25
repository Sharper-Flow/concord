package store

import (
	"context"
	"testing"
)

func TestHistoryRejectsLegacyAndUnknownRecordKinds(t *testing.T) {
	s := seedQueryFixture(t)
	for _, kind := range []string{"worker.unknown", "work.created", "workflow.action_completed", "contract"} {
		_, err := s.QueryQ7(context.Background(), Q7Request{Work: "blocked", RecordKinds: []string{kind}})
		assertFailureKind(t, err, KindInvalidFilter)
	}
}
