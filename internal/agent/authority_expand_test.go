package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0097 D6: an expansion preserves every existing capability, Product scope,
// and Project scope, never touches the stored principal, and replays as an
// idempotent no-op. The replace verb keeps its full-statement semantics.

func expandService(t *testing.T) *Service {
	t.Helper()
	service := NewService(openAgentDB(t))
	service.Now = fixedTime
	return service
}

func readBackPolicy(t *testing.T, service *Service, clientRef string) (capabilities, products, projects []string, principal string) {
	t.Helper()
	client, _, err := service.Store.TrustedClientWithKey(context.Background(), clientRef)
	if err != nil {
		t.Fatalf("read back trusted client: %v", err)
	}
	for _, decode := range []struct {
		label   string
		encoded string
		target  *[]string
	}{
		{"capabilities", client.CapabilitiesJSON, &capabilities},
		{"product scope", client.ProductScopeJSON, &products},
		{"project scope", client.ProjectScopeJSON, &projects},
	} {
		if err := json.Unmarshal([]byte(decode.encoded), decode.target); err != nil {
			t.Fatalf("decode %s %q: %v", decode.label, decode.encoded, err)
		}
	}
	return capabilities, products, projects, client.PrincipalRef
}

// assertStringSet fails unless got holds exactly the want entries, in any
// order and without duplicates.
func assertStringSet(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want exactly %v", label, got, want)
	}
	for _, value := range want {
		found := false
		for _, candidate := range got {
			if candidate == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s = %v, want exactly %v", label, got, want)
		}
	}
}

func TestExpandTrustedClientPolicyPreservesEveryExistingGrant(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read", "work_define"}, []string{"product-1"}, []string{"project-1", "project-2"})); err != nil {
		t.Fatal(err)
	}
	if err := service.ExpandTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{Capabilities: []Capability{"cross_scope"}, ProductScope: []string{"pokeedge"}, ProjectScope: []string{"pokeedge-main"}}); err != nil {
		t.Fatalf("expand: %v", err)
	}
	capabilities, products, projects, principal := readBackPolicy(t, service, "client-1")
	assertStringSet(t, "capabilities", capabilities, "product_read", "work_define", "cross_scope")
	assertStringSet(t, "product scope", products, "product-1", "pokeedge")
	assertStringSet(t, "project scope", projects, "project-1", "project-2", "pokeedge-main")
	if principal != "human-1" {
		t.Fatalf("principal = %q, want the stored principal human-1", principal)
	}
}

func TestExpandTrustedClientPolicyReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	additions := TrustedClientPolicy{Capabilities: []Capability{"work_define"}, ProductScope: []string{"pokeedge"}, ProjectScope: []string{"pokeedge-main"}}
	for run := 1; run <= 2; run++ {
		if err := service.ExpandTrustedClientPolicy(ctx, "client-1", additions); err != nil {
			t.Fatalf("expand run %d: %v", run, err)
		}
		capabilities, products, projects, principal := readBackPolicy(t, service, "client-1")
		assertStringSet(t, "capabilities", capabilities, "product_read", "work_define")
		assertStringSet(t, "product scope", products, "product-1", "pokeedge")
		assertStringSet(t, "project scope", projects, "project-1", "pokeedge-main")
		if principal != "human-1" {
			t.Fatalf("principal = %q after run %d, want human-1", principal, run)
		}
	}
}

func TestExpandTrustedClientPolicyEmptyAdditionsChangeNothing(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	if err := service.ExpandTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{}); err != nil {
		t.Fatalf("empty expansion: %v", err)
	}
	capabilities, products, projects, _ := readBackPolicy(t, service, "client-1")
	assertStringSet(t, "capabilities", capabilities, "product_read")
	assertStringSet(t, "product scope", products, "product-1")
	assertStringSet(t, "project scope", projects, "project-1")
}

func TestExpandTrustedClientPolicyRefusesInvalidAdditionsAndKeepsPolicyUnchanged(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		label     string
		clientRef string
		additions TrustedClientPolicy
	}{
		{"unknown capability", "client-1", TrustedClientPolicy{Capabilities: []Capability{"root_everything"}}},
		{"duplicate capability", "client-1", TrustedClientPolicy{Capabilities: []Capability{"product_read", "product_read"}}},
		{"empty product scope entry", "client-1", TrustedClientPolicy{ProductScope: []string{""}}},
		{"oversized scope entry", "client-1", TrustedClientPolicy{ProjectScope: []string{string(make([]byte, 129))}}},
		{"empty client reference", "", TrustedClientPolicy{Capabilities: []Capability{"product_read"}}},
	}
	for _, testCase := range cases {
		if err := service.ExpandTrustedClientPolicy(ctx, testCase.clientRef, testCase.additions); err == nil {
			t.Fatalf("%s: expansion was accepted", testCase.label)
		}
	}
	capabilities, products, projects, _ := readBackPolicy(t, service, "client-1")
	assertStringSet(t, "capabilities after refusals", capabilities, "product_read")
	assertStringSet(t, "product scope after refusals", products, "product-1")
	assertStringSet(t, "project scope after refusals", projects, "project-1")
}

func TestExpandTrustedClientPolicyRefusesUnknownClientTyped(t *testing.T) {
	err := expandService(t).ExpandTrustedClientPolicy(context.Background(), "missing-client", TrustedClientPolicy{Capabilities: []Capability{"product_read"}})
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindProjectionNotFound {
		t.Fatalf("expand unknown client error = %v, want KindProjectionNotFound", err)
	}
}

func TestExpandTrustedClientPolicyRefusesWhenUnionExceedsTheScopeBound(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	full := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		full = append(full, "product-"+string(rune('a'+i%26))+"-"+string(rune('0'+i/26)))
	}
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read"}, full, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	expandErr := service.ExpandTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{ProductScope: []string{"pokeedge"}})
	if expandErr == nil {
		t.Fatal("expansion past the product-scope bound was accepted")
	}
	var failure *store.Failure
	if !errors.As(expandErr, &failure) || failure.Kind != store.KindInvalidOperation {
		t.Fatalf("bound refusal error = %v, want KindInvalidOperation", expandErr)
	}
	_, products, _, _ := readBackPolicy(t, service, "client-1")
	if len(products) != 100 {
		t.Fatalf("product scope count after bound refusal = %d, want the stored 100", len(products))
	}
}

func TestExpandTrustedClientPolicyLeavesReplaceVerbSemanticsUnchanged(t *testing.T) {
	ctx := context.Background()
	service := expandService(t)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-1", "human-1", []Capability{"product_read", "work_define"}, []string{"product-1", "pokeedge"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "human-2", Capabilities: []Capability{"product_read"}, ProductScope: []string{"pokeedge"}, ProjectScope: []string{"project-1"}}); err != nil {
		t.Fatal(err)
	}
	capabilities, products, _, principal := readBackPolicy(t, service, "client-1")
	assertStringSet(t, "capabilities after replace", capabilities, "product_read")
	assertStringSet(t, "product scope after replace", products, "pokeedge")
	if principal != "human-2" {
		t.Fatalf("principal after replace = %q, want human-2", principal)
	}
}
