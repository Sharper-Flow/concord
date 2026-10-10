package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// concord_work_trace.research is versioned at the result, not behind a second
// operation. Version 1 stays the legacy full pack keyed by pack_id, so every
// existing consumer keeps its exact wire shape; version 2 is the canonical
// read: an exact revision/finding selection through pack_id, or a bounded
// owner descriptor page through work_id. The version 1 owner read is refused
// explicitly instead of silently discarding every pack after the first.

func invokeResearchRead(t *testing.T, s *store.Store, service *Service, grant Authority, input map[string]any) Envelope {
	t.Helper()
	ctx := context.Background()
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "research", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// seedResearchReadPack authors one two-revision pack owned by work-1 through the store's
// direct mutation API: revision 1 carries findings f-1 and f-1b with source
// s-1, revision 2 carries finding f-2. The fixture is what the store produces,
// not a literal that can drift from it.
func seedResearchReadPack(t *testing.T, s *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	identity := func(key string) store.ResearchMutationIdentity {
		return store.ResearchMutationIdentity{PrincipalRef: "human-1", Tool: "research-read-versioning", OperationKind: "test", IdempotencyKey: key}
	}
	revision := func(question string) store.ResearchRevisionInput {
		return store.ResearchRevisionInput{
			Question: question, ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`),
			DoneWhen: json.RawMessage(`[]`), Method: "source_code",
		}
	}
	pack, err := store.CreateResearchPack(ctx, s, store.CreateResearchPackRequest{
		Identity: identity(id + "-create"), PackID: id, OwnerWorkID: "work-1",
		Freshness: store.ResearchCurrent, Revision: revision("Which revision does the exact read return?"),
	})
	if err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if _, err := store.AppendResearchRevision(ctx, s, store.AppendResearchRevisionRequest{
		Identity: identity(id + "-append"), PackID: id, ExpectedVersion: pack.ExpectedVersion,
		Revision: revision("What did the second revision add?"),
	}); err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
	version := pack.ExpectedVersion + 1
	if _, err := s.AddResearchSource(ctx, store.ResearchSourceRequest{
		Identity: identity(id + "-source"), PackID: id, Revision: 1, ExpectedVersion: version,
		Source: store.ResearchSource{SourceID: "s-1", Kind: store.SourceCode, Locator: "internal/store/research_reads.go", Title: "research reads", PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("source %s: %v", id, err)
	}
	version++
	for _, finding := range []struct{ id, statement string }{{"f-1", "revision one finding one"}, {"f-1b", "revision one finding two"}} {
		if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{
			Identity: identity(id + "-" + finding.id), PackID: id, Revision: 1, ExpectedVersion: version,
			Finding: store.ResearchFinding{FindingID: finding.id, Kind: store.FindingObservation, Statement: finding.statement, Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, SourceIDs: []string{"s-1"}, Scopes: store.ResearchScopes{Mode: "home"}},
		}); err != nil {
			t.Fatalf("finding %s: %v", finding.id, err)
		}
		version++
	}
	if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{
		Identity: identity(id + "-f-2"), PackID: id, Revision: 2, ExpectedVersion: version,
		Finding: store.ResearchFinding{FindingID: "f-2", Kind: store.FindingObservation, Statement: "revision two finding", Confidence: store.ConfidenceMedium, Freshness: store.ResearchCurrent, Status: store.FindingActive, Scopes: store.ResearchScopes{Mode: "home"}},
	}); err != nil {
		t.Fatalf("finding f-2: %v", err)
	}
}

// The legacy pack read keeps the exact existing result: the bare research_pack
// object with every revision, no version marker, whether the caller omits
// result_version or sends 1.
func TestResearchReadLegacyPackStaysBareFullResult(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	seedResearchReadPack(t, s, "legacy-pack")
	for _, input := range []map[string]any{
		{"product_id": "product-1", "pack_id": "legacy-pack"},
		{"product_id": "product-1", "pack_id": "legacy-pack", "result_version": 1},
	} {
		read := invokeResearchRead(t, s, service, grant, input)
		if read.Outcome != OutcomeOK {
			t.Fatalf("legacy read %+v failed: %+v", input, read.Error)
		}
		var result map[string]any
		if err := json.Unmarshal(read.Result, &result); err != nil {
			t.Fatal(err)
		}
		if _, versioned := result["result_version"]; versioned {
			t.Fatalf("legacy result must carry no result_version: %s", read.Result)
		}
		if result["pack_id"] != "legacy-pack" {
			t.Fatalf("legacy result pack_id=%v", result["pack_id"])
		}
		revisions, ok := result["revisions"].([]any)
		if !ok || len(revisions) != 2 {
			t.Fatalf("legacy result must carry both revisions: %s", read.Result)
		}
	}
}

// The version 1 owner read is refused with invalid_input naming result_version
// 2; it never returns the first pack while discarding the rest.
func TestResearchReadLegacyOwnerRefused(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	seedResearchReadPack(t, s, "owner-pack")
	for _, input := range []map[string]any{
		{"product_id": "product-1", "work_id": "work-1"},
		{"product_id": "product-1", "work_id": "work-1", "result_version": 1},
	} {
		read := invokeResearchRead(t, s, service, grant, input)
		if read.Outcome == OutcomeOK {
			t.Fatalf("legacy owner read must refuse: %+v", input)
		}
		if read.Error == nil || read.Error.Kind != "invalid_input" {
			t.Fatalf("expected invalid_input, got %+v", read.Error)
		}
		if !strings.Contains(read.Error.Message, "result_version") {
			t.Fatalf("refusal must direct the caller to result_version 2: %s", read.Error.Message)
		}
	}
}

// The version 2 owner read returns bounded descriptors with an authenticated
// continuation, and an empty owner is an empty page rather than unknown_scope.
func TestResearchReadOwnerVersion2DescriptorsAndContinuation(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	created := []string{"owner-pack-1", "owner-pack-2", "owner-pack-3"}
	for _, id := range created {
		seedResearchReadPack(t, s, id)
	}
	first := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "work_id": "work-1", "result_version": 2,
		"page": map[string]any{"limit": 2},
	})
	if first.Outcome != OutcomeOK {
		t.Fatalf("owner page one failed: %+v", first.Error)
	}
	var page struct {
		ResultVersion int              `json:"result_version"`
		Packs         []map[string]any `json:"packs"`
	}
	if err := json.Unmarshal(first.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.ResultVersion != 2 || len(page.Packs) != 2 {
		t.Fatalf("page one version=%d packs=%d", page.ResultVersion, len(page.Packs))
	}
	descriptorFields := []string{"pack_id", "owner_work_id", "current_revision", "freshness", "expected_version", "created_at", "updated_at"}
	seen := []string{}
	for _, descriptor := range page.Packs {
		if len(descriptor) != len(descriptorFields) {
			t.Fatalf("descriptor carries unexpected fields: %v", descriptor)
		}
		for _, field := range descriptorFields {
			if _, ok := descriptor[field]; !ok {
				t.Fatalf("descriptor lost %s: %v", field, descriptor)
			}
		}
		if _, hasRevisions := descriptor["revisions"]; hasRevisions {
			t.Fatalf("descriptor must not expand revisions: %v", descriptor)
		}
		id, _ := descriptor["pack_id"].(string)
		seen = append(seen, id)
	}
	if first.NextCursor == nil || *first.NextCursor == "" {
		t.Fatal("owner page one must carry a continuation cursor")
	}
	second := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "work_id": "work-1", "result_version": 2,
		"page": map[string]any{"limit": 2, "cursor": *first.NextCursor},
	})
	if second.Outcome != OutcomeOK {
		t.Fatalf("owner page two failed: %+v", second.Error)
	}
	if err := json.Unmarshal(second.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Packs) != 1 {
		t.Fatalf("page two packs=%d", len(page.Packs))
	}
	id, _ := page.Packs[0]["pack_id"].(string)
	seen = append(seen, id)
	if second.NextCursor != nil {
		t.Fatalf("page two must end the page: %v", *second.NextCursor)
	}
	sort.Strings(seen)
	sort.Strings(created)
	for i := range created {
		if seen[i] != created[i] {
			t.Fatalf("pages covered %v, created %v", seen, created)
		}
	}
}

func TestResearchReadOwnerVersion2EmptyOwnerIsEmptyPage(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	read := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "work_id": "work-1", "result_version": 2,
	})
	if read.Outcome != OutcomeOK {
		t.Fatalf("empty owner read failed: %+v", read.Error)
	}
	var result struct {
		ResultVersion int              `json:"result_version"`
		Packs         []map[string]any `json:"packs"`
	}
	if err := json.Unmarshal(read.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ResultVersion != 2 || result.Packs == nil || len(result.Packs) != 0 {
		t.Fatalf("empty owner must return version 2 with an empty page: %s", read.Result)
	}
}

// The version 2 pack read returns the typed wrapper. An exact revision returns
// only that revision; selected finding_ids return only those findings with
// their provenance; no selector returns the full pack.
func TestResearchReadVersion2ExactRevisionAndFindings(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	seedResearchReadPack(t, s, "exact-pack")

	wrapper := func(read Envelope) (int, store.ResearchPack) {
		t.Helper()
		var result struct {
			ResultVersion int                `json:"result_version"`
			Pack          store.ResearchPack `json:"pack"`
		}
		if err := json.Unmarshal(read.Result, &result); err != nil {
			t.Fatalf("wrapper %s: %v", read.Result, err)
		}
		return result.ResultVersion, result.Pack
	}

	full := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "pack_id": "exact-pack", "result_version": 2,
	})
	if full.Outcome != OutcomeOK {
		t.Fatalf("version 2 full pack failed: %+v", full.Error)
	}
	version, pack := wrapper(full)
	if version != 2 || pack.PackID != "exact-pack" || len(pack.Revisions) != 2 {
		t.Fatalf("version 2 full pack wrapper=%d pack=%+v", version, pack)
	}

	exact := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "pack_id": "exact-pack", "result_version": 2, "revision": 1,
	})
	if exact.Outcome != OutcomeOK {
		t.Fatalf("version 2 exact revision failed: %+v", exact.Error)
	}
	version, pack = wrapper(exact)
	if version != 2 || len(pack.Revisions) != 1 || pack.Revisions[0].Revision != 1 {
		t.Fatalf("exact revision wrapper=%d revisions=%+v", version, pack.Revisions)
	}
	if len(pack.Revisions[0].Findings) != 2 || len(pack.Revisions[0].Sources) != 1 {
		t.Fatalf("exact revision must keep its findings and provenance: %+v", pack.Revisions[0])
	}

	selected := invokeResearchRead(t, s, service, grant, map[string]any{
		"product_id": "product-1", "pack_id": "exact-pack", "result_version": 2,
		"revision": 1, "finding_ids": []string{"f-1"},
	})
	if selected.Outcome != OutcomeOK {
		t.Fatalf("version 2 selected findings failed: %+v", selected.Error)
	}
	version, pack = wrapper(selected)
	if version != 2 || len(pack.Revisions) != 1 || len(pack.Revisions[0].Findings) != 1 {
		t.Fatalf("selected findings wrapper=%d revisions=%+v", version, pack.Revisions)
	}
	finding := pack.Revisions[0].Findings[0]
	if finding.FindingID != "f-1" || len(finding.SourceIDs) != 1 || finding.SourceIDs[0] != "s-1" {
		t.Fatalf("selected finding lost its provenance: %+v", finding)
	}
}

// Selector combinations the published input cannot express stay runtime
// refusals: selectors need pack_id with result_version 2, finding_ids need an
// explicit revision, and pack reads take no continuation cursor.
func TestResearchReadSelectorCombinationsRefuse(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := researchSurfaceFixture(t)
	seedResearchReadPack(t, s, "combo-pack")
	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"revision without version 2", map[string]any{"product_id": "product-1", "pack_id": "combo-pack", "revision": 1}},
		{"revision with version 1", map[string]any{"product_id": "product-1", "pack_id": "combo-pack", "result_version": 1, "revision": 1}},
		{"revision on owner read", map[string]any{"product_id": "product-1", "work_id": "work-1", "result_version": 2, "revision": 1}},
		{"finding_ids without revision", map[string]any{"product_id": "product-1", "pack_id": "combo-pack", "result_version": 2, "finding_ids": []string{"f-1"}}},
		{"cursor on pack read", map[string]any{"product_id": "product-1", "pack_id": "combo-pack", "result_version": 2, "revision": 1, "page": map[string]any{"cursor": "stale-token"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := invokeResearchRead(t, s, service, grant, tc.input)
			if read.Outcome == OutcomeOK {
				t.Fatalf("incompatible research read must refuse: %v", tc.input)
			}
			if read.Error == nil || read.Error.Kind != "invalid_input" {
				t.Fatalf("expected invalid_input, got %+v", read.Error)
			}
		})
	}
}

// The published input bounds the new selectors before the handler runs.
func TestResearchReadInputSchemaBounds(t *testing.T) {
	t.Parallel()
	base := map[string]any{"product_id": "product-1", "pack_id": "pack-1", "result_version": 2}
	valid := []map[string]any{
		{"product_id": "product-1", "pack_id": "pack-1"},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 1},
		base,
		{"product_id": "product-1", "work_id": "work-1", "result_version": 2},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 2, "revision": 1, "finding_ids": []string{"f-1", "f-2"}},
	}
	for _, input := range valid {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateOperationPayload("concord_work_trace", "research", raw, false); err != nil {
			t.Fatalf("valid research input refused: %s: %v", raw, err)
		}
	}
	thirtyThree := make([]string, 33)
	for i := range thirtyThree {
		thirtyThree[i] = fmt.Sprintf("f-%02d", i)
	}
	invalid := []map[string]any{
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 3},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 0},
		{"product_id": "product-1", "pack_id": "pack-1", "revision": 0},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 2, "finding_ids": []string{}},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 2, "finding_ids": []string{"f-1", "f-1"}},
		{"product_id": "product-1", "pack_id": "pack-1", "result_version": 2, "finding_ids": thirtyThree},
	}
	for _, input := range invalid {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateOperationPayload("concord_work_trace", "research", raw, false); err == nil {
			t.Fatalf("invalid research input admitted: %s", raw)
		}
	}
}

// The result union admits exactly the legacy bare pack and the two typed
// version 2 wrappers, and nothing else.
func TestResearchReadResultSchemaUnion(t *testing.T) {
	t.Parallel()
	bare := store.ResearchPack{PackID: "pack-1", OwnerWorkID: "work-1", CurrentRevision: 1, Freshness: store.ResearchCurrent, ExpectedVersion: 1, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	valid := []any{
		bare,
		map[string]any{"result_version": 2, "pack": bare},
		map[string]any{"result_version": 2, "packs": []store.ResearchPackDescriptor{}},
		map[string]any{"result_version": 2, "packs": []store.ResearchPackDescriptor{{PackID: "pack-1", OwnerWorkID: "work-1", CurrentRevision: 1, Freshness: store.ResearchCurrent, ExpectedVersion: 1, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}}},
	}
	for _, result := range valid {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateOperationPayload("concord_work_trace", "research", raw, true); err != nil {
			t.Fatalf("valid research result refused: %s: %v", raw, err)
		}
	}
	invalid := []any{
		map[string]any{"result_version": 2},
		map[string]any{"result_version": 1, "pack": bare},
		map[string]any{"result_version": 2, "pack": bare, "packs": []store.ResearchPackDescriptor{}},
		map[string]any{"result_version": 2, "packs": []map[string]any{{"pack_id": "pack-1", "revisions": []any{}}}},
	}
	for _, result := range invalid {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateOperationPayload("concord_work_trace", "research", raw, true); err == nil {
			t.Fatalf("invalid research result admitted: %s", raw)
		}
	}
}
