package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestWorkRemovalInputRejectsSessions(t *testing.T) {
	for _, state := range []string{"active", "unknown", "vacated"} {
		t.Run(state, func(t *testing.T) {
			var input workRemovalMutationInput
			raw := []byte(fmt.Sprintf(`{"sessions":[{"session_ref":"session-fixture","state":%q}]}`, state))
			if err := decodeOperationInput(raw, &input); err == nil || !strings.Contains(err.Error(), `unknown field "sessions"`) {
				t.Fatalf("removal session payload must be rejected: %v", err)
			}
		})
	}
}
