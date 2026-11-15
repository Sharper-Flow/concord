package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// derivedRequestWireMaxBytes pins the derived full-invoke wire maximum of
// the current widest admitted action shape. The test owns the pin, not
// production: any change in the widest shape fails this exact figure first,
// and the pin moves only by a deliberate re-derivation beside the store
// budget's own next-power-of-two assertion.
const derivedRequestWireMaxBytes = 2089791

// Derive the store workflow budget independently of the transport ceiling.
func TestWorkflowActionWidestShapeDerivesStoreBudget(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	input, _ := defs["work_transition_action_input"].(map[string]any)
	anyOf, _ := input["anyOf"].([]any)
	if len(anyOf) == 0 {
		t.Fatal("the generated contract declares no admitted action variants")
	}
	var variants []string
	for _, branch := range anyOf {
		ref, _ := branch.(map[string]any)["$ref"].(string)
		name := ref[strings.LastIndex(ref, "/")+1:]
		if name == "" || defs[name] == nil {
			t.Fatalf("the admitted variant branch %v names no generated definition", branch)
		}
		variants = append(variants, name)
	}
	// Writer-owned bounds apply to every current variant's schema walk.
	opt := schemawire.Options{Defs: defs}
	widest, widestName := 0, ""
	for _, name := range variants {
		size, err := schemawire.Max(defs[name], name, opt)
		if err != nil {
			t.Fatalf("the %s derivation hit an unbudgeted member: %v", name, err)
		}
		if size > widest {
			widest, widestName = size, name
		}
	}
	if widestName == "" {
		t.Fatal("no admitted variant derived a figure")
	}
	request := invokeRequestWireBytes(t, widest)
	derived := schemawire.NextPowerOfTwo(request)
	if request != derivedRequestWireMaxBytes {
		t.Fatalf("the derived full-invoke request = %d bytes, want the pinned %d; the widest shape changed and the pin must be re-derived beside the store budget it rounds to", request, derivedRequestWireMaxBytes)
	}
	if store.MaxWorkflowActionPayloadBytes != derived {
		t.Fatalf("store.MaxWorkflowActionPayloadBytes = %d, want the widest request composition's own rounding %d", store.MaxWorkflowActionPayloadBytes, derived)
	}
	t.Logf("the widest admitted action %s serializes to %d bytes as a whole variant and %d bytes as a full invoke request; store budget derives to %d", widestName, widest, request, derived)
}
