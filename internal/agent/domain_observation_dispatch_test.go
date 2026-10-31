package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// Domain detail reads the bounded open window from existing journal events.
// Dismissed rows remain durable but do not enter the open observation payload.
// The seed is committed registry content only: the retired SQL Domain
// projection stays empty, and the fold resolves the Domain from the
// pre-transaction registry proof (law-folder-authority.md, Committed reads).
func TestDomainDetailReadsExistingOpenObservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	assertNoDomainProjectionRows(t, s)
	domain := pm1fixture.FixtureRootDomainID
	verified, err := s.EstablishDomainRegistryProof(ctx, "product-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		payload, err := json.Marshal(map[string]any{
			"observation_id": fmt.Sprintf("dob:%016x", i), "product_id": "product-1", "domain_id": domain,
			"statement": fmt.Sprintf("existing observation %d", i), "refs": []string{}, "tags": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(verified, s, store.Operation{Events: []store.Event{{
			EventID: fmt.Sprintf("domain-observation-%d", i), Kind: "domain.observation_recorded",
			SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "agent", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	dismissal, err := json.Marshal(map[string]any{"observation_id": "dob:0000000000000002", "product_id": "product-1", "domain_id": domain})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(verified, s, store.Operation{Events: []store.Event{{
		EventID: "domain-observation-dismissed", Kind: "domain.observation_dismissed",
		SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: dismissal,
	}}}); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	detail := domainRead(t, s, service, env, "detail", fmt.Sprintf(`{"product_id":"product-1","domain_id":%q}`, domain))
	var payload struct {
		Observations []struct {
			ObservationID string `json:"observation_id"`
			Statement     string `json:"statement"`
			State         string `json:"state"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(detail.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Observations) != 1 || payload.Observations[0].ObservationID != "dob:0000000000000001" || payload.Observations[0].Statement != "existing observation 1" || payload.Observations[0].State != "open" {
		t.Fatalf("detail open observations=%+v", payload.Observations)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_observations`); got != 2 {
		t.Fatalf("detail removed durable observations: rows=%d", got)
	}
}

// A dispatched concord_work_define.observation_record runs through the
// pre-transaction registry proof and records its work-anchored observation.
// The dispatch itself proves the wire: the proof establishment precedes the
// transaction and the fold resolves from the returned context.
func TestObservationRecordDispatchSucceedsWithCommittedRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	assertNoDomainProjectionRows(t, s)
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-1","statement":"dispatched observation through the committed registry","refs":[],"tags":[],"idempotency_key":"observation-proof-wire"}`)}
	response := dispatchMutation(t, s, service, request, env)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("observation_record outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_observations`); got != 1 {
		t.Fatalf("dispatched observation rows=%d, want the one recorded observation", got)
	}
	assertNoDomainProjectionRows(t, s)
}

// A committed-registry observation succeeds with no SQL Domain projection
// rows, and a wrong or deprecated Domain refuses with no row written.
func TestDomainObservationResolvesFromCommittedRegistryProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read"})
	deprecated := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0",
		ProductKey:    "fixture-product-1",
		RootDomainID:  "product-root:product-1",
		Domains: []store.KnowledgeDomain{
			{DomainID: "product-root:product-1", Name: "Population root", Purpose: "Governs the fixture Product.", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			{DomainID: "deprecated-domain", Name: "Deprecated", Purpose: "Retired observation anchor.", ParentDomainID: "product-root:product-1", Status: "deprecated", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
		},
	}
	if _, err := pm1fixture.SeedProductDomainRegistry(ctx, s, "product-1", "project-1", t.TempDir(), deprecated); err != nil {
		t.Fatal(err)
	}
	assertNoDomainProjectionRows(t, s)
	verified, err := s.EstablishDomainRegistryProof(ctx, "product-1")
	if err != nil {
		t.Fatal(err)
	}

	domainObservation := func(id, domain string) store.Event {
		payload, err := json.Marshal(map[string]any{
			"observation_id": id, "product_id": "product-1", "domain_id": domain,
			"statement": "recorded against the committed registry", "refs": []string{}, "tags": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return store.Event{
			EventID: "rec-" + id, Kind: "domain.observation_recorded",
			SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "agent",
			OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload,
		}
	}

	// The current root Domain accepts the observation from the committed
	// registry alone: no projection row was populated anywhere.
	current := domainObservation("dob:0000000000000001", "product-root:product-1")
	if err := store.ApplyOperation(verified, s, store.Operation{Events: []store.Event{current}}); err != nil {
		t.Fatalf("committed registry observation refused: %v", err)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_observations`); got != 1 {
		t.Fatalf("committed registry observation rows=%d, want 1", got)
	}

	// A Domain the committed registry does not declare refuses closed.
	wrong := domainObservation("dob:0000000000000002", "no-such-domain")
	err = store.ApplyOperation(verified, s, store.Operation{Events: []store.Event{wrong}})
	var wrongFailure *store.Failure
	if !errors.As(err, &wrongFailure) || wrongFailure.Kind != store.KindProjectionNotFound {
		t.Fatalf("wrong Domain error=%v, want KindProjectionNotFound", err)
	}

	// A registry-declared deprecated Domain refuses the observation.
	deprecatedRefusal := domainObservation("dob:0000000000000003", "deprecated-domain")
	err = store.ApplyOperation(verified, s, store.Operation{Events: []store.Event{deprecatedRefusal}})
	var deprecatedFailure *store.Failure
	if !errors.As(err, &deprecatedFailure) || deprecatedFailure.Kind != store.KindInvalidTransition {
		t.Fatalf("deprecated Domain error=%v, want KindInvalidTransition", err)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_observations`); got != 1 {
		t.Fatalf("refused observations wrote rows: %d", got)
	}
	assertNoDomainProjectionRows(t, s)
}

// assertNoDomainProjectionRows pins the retired SQL Domain projection at zero
// rows for the fixture store: the registry the observation fold validates
// against is committed content read through the pre-transaction proof, never
// a populated projection (law-folder-authority.md, Committed reads).
func assertNoDomainProjectionRows(t *testing.T, s *store.Store) {
	t.Helper()
	for _, table := range []string{"domain_registries", "domains", "law_domain_homes", "domain_architecture_relations"} {
		if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM `+table); got != 0 {
			t.Fatalf("retired Domain projection table %s holds %d rows, want 0", table, got)
		}
	}
}
