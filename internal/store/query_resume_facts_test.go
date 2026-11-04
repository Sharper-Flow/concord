package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestQ3ItemScopedFullCarriesNativeRuns(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := seedNativeRunFixture(t, s)
	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	event, err := buildNativeRunEvent("op:start", workID, actor, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), 2, "start", "run-1", "routing://prod", "started", "https://evidence.invalid/run-1", "sha256:"+repeatHex("a"), "2026-08-20T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := applyWorkflowOperationTx(ctx, tx.tx, Operation{Events: []Event{event}}, newWriteScope(tx.tx))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, detail string
		ids          []string
		want         bool
	}{
		{"item-full", "full", []string{workID}, true},
		{"item-summary", "summary", []string{workID}, false},
		{"broad-full", "full", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.QueryQ3(ctx, Q3Request{Product: "product-1", WorkIDs: tc.ids, Detail: tc.detail})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 1 {
				t.Fatalf("items: %+v", result.Items)
			}
			data, err := json.Marshal(result.Items[0])
			if err != nil {
				t.Fatal(err)
			}
			var item map[string]json.RawMessage
			if err := json.Unmarshal(data, &item); err != nil {
				t.Fatal(err)
			}
			_, found := item["native_runs"]
			if found != tc.want {
				t.Fatalf("native_runs present = %v, want %v: %s", found, tc.want, data)
			}
		})
	}
}

func TestQ3ItemScopedHandoffSelection(t *testing.T) {
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	read := func(project string) *ProjectHandoff {
		t.Helper()
		result, err := f.store.QueryQ3(ctx, Q3Request{Product: "product-w", WorkIDs: []string{f.workID}, Detail: "full", AmbientProject: project})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("items: %+v", result.Items)
		}
		return result.Items[0].PendingProjectHandoff
	}
	if got := read(f.targetProject); got != nil {
		t.Fatalf("unexpected handoff: %+v", got)
	}
	recorded := recordHandoff(t, f, f.sourceTree)
	if got := read(f.sourceProject); got == nil || got.HandoffID != recorded.HandoffID {
		t.Fatalf("fallback handoff: %+v", got)
	}
	consumeHandoff(t, f, recorded.HandoffID)
	if got := read(f.targetProject); got == nil || got.HandoffID != recorded.HandoffID || got.State != ProjectHandoffConsumed {
		t.Fatalf("addressed handoff: %+v", got)
	}
	if got := read(f.sourceProject); got != nil {
		t.Fatalf("consumed fallback: %+v", got)
	}
}

func TestQ3ItemScopedLawContext(t *testing.T) {
	s := openTemp(t)
	workID := "law-context-list"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		detail string
		ids    []string
		want   bool
	}{{"full", []string{workID}, true}, {"summary", []string{workID}, false}, {"full", nil, false}} {
		result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: tc.ids, Detail: tc.detail})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("items: %+v", result.Items)
		}
		law := result.Items[0].LawContext
		if (law != nil) != tc.want {
			t.Fatalf("law context = %+v, want present %v", law, tc.want)
		}
		if law != nil && (len(law.Laws) != 2 || len(law.Domains) != 2) {
			t.Fatalf("law context: %+v", law)
		}
	}
	seedWork(t, s, "law-context-unbound")
	result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{workID, "law-context-unbound"}, Detail: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items: %+v", result.Items)
	}
	for _, item := range result.Items {
		if (item.LawContext != nil) != (item.ID == workID) {
			t.Fatalf("item %s law context: %+v", item.ID, item.LawContext)
		}
	}
}
