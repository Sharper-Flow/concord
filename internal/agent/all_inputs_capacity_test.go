package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// Workflow action fields are bounded by their admitted anyOf variants, not
// by the wrapper's unconstrained fields member.
func TestEveryServedInputDerivesTransportCapacity(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	opt := schemawire.Options{Defs: defs}
	type row struct {
		op, schema     string
		input, request int
	}
	var rows []row
	for _, op := range agentcontract.ContractOperations {
		schemas := []string{op.InputSchema}
		if op.InputSchema == "work_transition_action_input" {
			wrapper, _ := defs[op.InputSchema].(map[string]any)
			anyOf, _ := wrapper["anyOf"].([]any)
			schemas = schemas[:0]
			for _, branch := range anyOf {
				ref, _ := branch.(map[string]any)["$ref"].(string)
				name := ref[strings.LastIndex(ref, "/")+1:]
				if name == "" || defs[name] == nil {
					t.Fatalf("the admitted variant branch %v names no generated definition", branch)
				}
				schemas = append(schemas, name)
			}
			if len(schemas) == 0 {
				t.Fatal("work_transition_action_input declares no admitted action variants")
			}
		}
		for _, name := range schemas {
			size, err := schemawire.Max(defs[name], name, opt)
			if err != nil {
				t.Fatalf("%s input %s walk hit an unbudgeted member: %v", op.ID, name, err)
			}
			rows = append(rows, row{op.ID, name, size, invokeRequestWireBytes(t, size)})
		}
	}
	if len(rows) == 0 {
		t.Fatal("the contract declares no served input schemas")
	}
	widest := rows[0]
	for _, r := range rows {
		t.Logf("%-44s %-54s input=%8d request=%8d", r.op, r.schema, r.input, r.request)
		if r.request > widest.request {
			widest = r
		}
	}
	if widest.schema != "work_transition_remove_input" {
		t.Fatalf("widest served input = %s (%d request bytes), want work_transition_remove_input", widest.schema, widest.request)
	}
	if widest.request != 7930203 {
		t.Fatalf("widest full invoke request = %d bytes, want 7930203", widest.request)
	}
	derived := schemawire.NextPowerOfTwo(widest.request)
	if derived != 8388608 {
		t.Fatalf("widest request rounds to %d, want 8388608", derived)
	}
	if MaxRequestBytes != derived {
		t.Fatalf("MaxRequestBytes = %d, want derived %d", MaxRequestBytes, derived)
	}
}

func TestInvokeRequestExactTransportLimit(t *testing.T) {
	const request = `{"call_envelope":{"schema_version":"1.0","request_id":"request-1","manifest_digest":"digest"},"tool":"concord_product_view","operation":"resolve","input":{}}`
	data := []byte(request + strings.Repeat(" ", MaxRequestBytes-len(request)))
	decoded, envelope, err := DecodeInvokeRequest(data)
	if err != nil || decoded.Operation != "resolve" || envelope.RequestID != "request-1" {
		t.Fatalf("exact-limit decode: request=%+v envelope=%+v error=%v", decoded, envelope, err)
	}
	data = append(data, ' ')
	if _, _, err := DecodeInvokeRequest(data); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over-limit decode error = %v, want size refusal", err)
	}
}
