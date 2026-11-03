package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestIntakeWorkPinOutputSchema(t *testing.T) {
	pin := []byte(`{"work_id":"work-1","title":"Need","linear_issue_key":"","project_id":"project-1","project_display_name":"Project","version":3,"lifecycle":"needed","cancelled_instance_closes":0,"workflow_type":"","step":"intake","driving_sessions":[],"watermark":"seq:3","next_valid_intents":[]}`)
	if err := payloadschema.Validate("work_pin", pin); err != nil {
		t.Fatalf("valid intake pin rejected: %v", err)
	}
}
