package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Selected research in the shared work context: the typed selection, its
// canonical resolution, the shared 32-slot and 64 KiB view bounds, the
// declaration post-append view check, and the dispatch required-resolution
// gate. The tests assert at the JSON level so a missing view member reads
// as a behavioral gap, never as a compile gap.

// researchSelectionEntry authors one closed selection entry.
func researchSelectionEntry(pack string, revision int64, finding, domain string) map[string]any {
	return map[string]any{"pack_id": pack, "revision": revision, "finding_id": finding, "domain_id": domain}
}

// seedResearchOwnerWork creates the pack owner work item beside the
// workflow fixture's existing Product and Project, without reseeding either.
func seedResearchOwnerWork(t *testing.T, s *Store, id string) {
	t.Helper()
	priority := int64(1)
	events := []Event{
		{EventID: "research-work-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: eventKindRegistry["work.created"].CurrentVersion, Payload: mustJSONValue(workCreatedPayload{WorkID: id, WorkKind: "research", Title: id, Priority: &priority})},
		{EventID: "research-work-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"work_id": id, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0}}); err != nil {
		t.Fatal(err)
	}
}

// seedResearchContextPack seeds one research pack with two revisions: the
// selected revision 1 carries f1 (inference, explicit scopes, linked s1 and
// s2), f2 (observation, linked s1 and s3), and f0 (no links at all); the
// newer revision 2 carries its own f3 beside the copied content, so an exact
// older-revision selection must never read the newer revision.
func seedResearchContextPack(t *testing.T, f workContextFixture, key string) ResearchPack {
	t.Helper()
	s := f.store
	seedResearchOwnerWork(t, s, key+"-owner")
	pack := createSimplePack(t, s, key, key+"-owner")
	exec := func(tag, query string, args ...any) {
		t.Helper()
		if _, err := s.DatabaseForTesting().Exec(query, args...); err != nil {
			t.Fatalf("research fixture statement %q failed: %v", tag, err)
		}
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		exec("source "+id, fmt.Sprintf(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,1,'%s','web','https://example.com/%s','Source %s','Example','2026-10-01T00:00:00Z')`, id, id, id), pack.PackID)
	}
	exec("f1", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,'f1','inference','the selected research inference','high','current','active','explicit')`, pack.PackID)
	exec("f1 scopes", `INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES(?,1,'f1','product','product')`, pack.PackID)
	exec("f1 domain scope", `INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES(?,1,'f1','domain','child-alpha')`, pack.PackID)
	exec("f2", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,'f2','observation','the sibling research observation','medium','current','active','home')`, pack.PackID)
	exec("f0", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,'f0','hypothesis','the unsourced research hypothesis','low','current','active','home')`, pack.PackID)
	for _, link := range [][2]string{{"f1", "s1"}, {"f1", "s2"}, {"f2", "s1"}, {"f2", "s3"}} {
		exec("link "+link[0]+"-"+link[1], `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,1,?,?)`, pack.PackID, link[0], link[1])
	}
	if _, err := appendResearchRevision(context.Background(), s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	exec("s4", `INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,2,'s4','issue','https://example.com/newer','Newer','Example','2026-10-02T00:00:00Z')`, pack.PackID)
	exec("f3", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,2,'f3','conclusion','the newer revision conclusion','high','current','active','home')`, pack.PackID)
	exec("f3 link", `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,2,'f3','s4')`, pack.PackID)
	return pack
}

// bindResearchPack records one consumer pin for the fixture work through
// the production pin-replacement owner, exactly as the action's
// research_bindings route binds, so one consumer holds one pin per pack.
func bindResearchPack(t *testing.T, f workContextFixture, packID string, revision int64, required bool) {
	t.Helper()
	tx, err := f.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindResearchRelianceTx(context.Background(), tx, f.workID, []ResearchBindingDeclaration{{PackID: packID, Revision: revision, UseRole: UseContext, Required: required}}, time.Unix(9, 0).UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordResearchDeclarationTx drives the record_work_context semantic
// constructor with the action's full field set — including the optional
// research_finding_refs member, authored through the semantic constructor
// while the published action payload does not declare it — in one
// transaction that mirrors the live action's sequence: the same-action
// research bindings first, then the declaration constructor, then the
// append and fold, then the production post-append view check. The
// transaction commits only when every step holds, so a refusal leaves no
// binding, event, or version effect.
func recordResearchDeclarationTx(t *testing.T, f workContextFixture, fields map[string]any, bindings []ResearchBindingDeclaration) error {
	t.Helper()
	return recordResearchDeclarationTxReceipt(t, f, fields, bindings, nil)
}

// recordResearchDeclarationTxReceipt runs the same mirror and, when the
// caller supplies a key, writes one mutation-idempotency receipt inside the
// same callback transaction before the mutation work, exactly as the
// authenticated mutation boundary does. The store rolls the whole callback
// transaction back on any refusal, so a rolled-back receipt is provably
// absent and a later same-key attempt re-executes instead of replaying.
func recordResearchDeclarationTxReceipt(t *testing.T, f workContextFixture, fields map[string]any, bindings []ResearchBindingDeclaration, receipt *MutationIdempotencyKey) error {
	t.Helper()
	ctx := context.Background()
	version := readWorkVersion(t, f.store, f.workID)
	ownerRef, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	fieldRaw := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		fieldRaw[name] = json.RawMessage(mustJSONValue(value))
	}
	return f.store.Transact(ctx, func(transaction *Transaction) error {
		rawTx, txErr := transactionSQL(transaction, "research declaration mirror")
		if txErr != nil {
			return txErr
		}
		if receipt != nil {
			if err := InsertMutationIdempotencyTx(ctx, transaction, MutationIdempotencyInsert{
				Key: *receipt, CanonicalDigest: "sha256:" + strings.Repeat("a", 64),
				OperationID: "op-research-mirror-" + f.workID, ResultEventIDs: "[]",
				ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: "{}",
				ObservedAt: time.Unix(9, 0).UTC(),
			}); err != nil {
				return err
			}
		}
		if err := BindResearchRelianceTx(ctx, rawTx, f.workID, bindings, time.Unix(9, 0).UTC()); err != nil {
			return err
		}
		events, err := workflowRecordWorkContextEvents(ctx, rawTx, WorkflowActionExecutionRequest{
			WorkID: f.workID, RequestID: "request:ctx-research-" + f.workID, Now: time.Unix(9, 0).UTC(),
		}, "repair", ownerRef, fieldRaw, "ctx-research-declare-"+f.workID+"-"+fmt.Sprint(version), version)
		if err != nil {
			return err
		}
		if _, err := applyWorkflowOperationTx(ctx, rawTx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version}}, newWriteScope(rawTx)); err != nil {
			return err
		}
		// The production post-append view check, invoked exactly where the
		// live action route invokes it.
		return checkWorkContextDeclarationTx(ctx, rawTx, f.workID, events)
	})
}

// researchViewDocument reads the current view and returns it decoded at the
// JSON level beside its serialized bytes. A read refusal is fatal here; the
// drift and bound tests assert refusals themselves and use the raw reader.
func researchViewDocument(t *testing.T, f workContextFixture) (map[string]any, []byte) {
	t.Helper()
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatalf("current view read refused: %v", err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document, raw
}

// researchEntries extracts the view's research_findings member at the JSON
// level: a missing member is the behavioral gap this suite exists to catch.
func researchEntries(t *testing.T, document map[string]any, raw []byte) []any {
	t.Helper()
	value, present := document["research_findings"]
	if !present {
		t.Fatalf("the current view carries no research_findings member: %s", raw)
	}
	entries, ok := value.([]any)
	if !ok {
		t.Fatalf("the view research_findings member is not an array: %s", raw)
	}
	return entries
}

// researchFailingQueryer fails only the provenance link reads the
// resolution owns and passes every other read through, so the error
// propagation of one deterministic read is assertable without disturbing
// the surrounding assembly.
type researchFailingQueryer struct {
	db  queryer
	err error
}

func (q researchFailingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "active_research_finding_sources") {
		return nil, q.err
	}
	return q.db.QueryContext(ctx, query, args...)
}

func (q researchFailingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

// assertUnresolvedEntryCarriesNoContent asserts the finding and sources
// members are ABSENT, never null: an unresolved entry carries references
// only, and a null member would still transmit the member.
func assertUnresolvedEntryCarriesNoContent(t *testing.T, entry map[string]any, raw []byte) {
	t.Helper()
	if _, present := entry["finding"]; present {
		t.Fatalf("unresolved entry carries a finding member: %s", raw)
	}
	if _, present := entry["sources"]; present {
		t.Fatalf("unresolved entry carries a sources member: %s", raw)
	}
}

// latestDeclarationSeq reads the anchor declaration event's sequence.
func latestDeclarationSeq(t *testing.T, f workContextFixture) int64 {
	t.Helper()
	var seq int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// declarationEventCount counts the work item's recorded context declarations.
func declarationEventCount(t *testing.T, f workContextFixture) int {
	t.Helper()
	var count int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, f.workID, WorkflowWorkContextRecorded).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// injectResearchWrite runs one research write with the connection's
// foreign-key enforcement suspended, so a test can inject the incomplete
// stored state legal writes can never produce.
func injectResearchWrite(t *testing.T, f workContextFixture, query string, args ...any) {
	t.Helper()
	db := f.store.DatabaseForTesting()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
}

// injectResearchRowDelete removes one research row under suspended foreign
// keys.
func injectResearchRowDelete(t *testing.T, f workContextFixture, query string, args ...any) {
	t.Helper()
	injectResearchWrite(t, f, query, args...)
}

// researchBoundStatement composes one legal finding statement of exactly the
// requested byte length from an escaped, multibyte prefix, so the view
// boundary counts canonical UTF-8 bytes with JSON escaping.
func researchBoundStatement(pad int) string {
	const prefix = `escaped "quotes" \backslash\ ünïcödé 多言語 retained bytes`
	if pad < len(prefix) {
		panic("research bound statement pad under the prefix")
	}
	return prefix + strings.Repeat("b", pad-len(prefix))
}

// The exact selected revision and only its linked provenance resolve, the
// native finding stays unmapped, and zero-linked findings stay accurately
// unsourced.
func TestWorkContextResearchSelection(t *testing.T) {
	t.Parallel()

	t.Run("the exact older revision resolves with only its linked provenance", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-select")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "sel")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"required_reading":      []map[string]any{fixture.repoReading(workContextTestChild, "", "")},
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		}, nil); err != nil {
			t.Fatalf("research declaration refused: %v", err)
		}
		seq := latestDeclarationSeq(t, fixture)
		document, raw := researchViewDocument(t, fixture)
		entries := researchEntries(t, document, raw)
		if len(entries) != 1 {
			t.Fatalf("view carries %d research entries, want the one selection: %s", len(entries), raw)
		}
		entry, ok := entries[0].(map[string]any)
		if !ok {
			t.Fatalf("research entry is not an object: %s", raw)
		}
		if entry["view_id"] != fmt.Sprintf("research:%d:0", seq) {
			t.Fatalf("view_id = %v, want research:%d:0", entry["view_id"], seq)
		}
		if entry["selection_event_seq"].(float64) != float64(seq) {
			t.Fatalf("selection_event_seq = %v, want %d", entry["selection_event_seq"], seq)
		}
		if entry["ordinal"].(float64) != 0 {
			t.Fatalf("ordinal = %v, want the authored ordinal 0", entry["ordinal"])
		}
		if entry["state"] != "resolved" || entry["required"] != true {
			t.Fatalf("state = %v required = %v, want resolved and required", entry["state"], entry["required"])
		}
		selection, ok := entry["selection"].(map[string]any)
		if !ok || selection["pack_id"] != pack.PackID || selection["revision"].(float64) != 1 || selection["finding_id"] != "f1" || selection["domain_id"] != workContextTestChild {
			t.Fatalf("selection = %+v, want the exact authored tuple", selection)
		}
		finding, ok := entry["finding"].(map[string]any)
		if !ok {
			t.Fatalf("resolved entry carries no finding: %s", raw)
		}
		if finding["kind"] != "inference" || finding["statement"] != "the selected research inference" || finding["confidence"] != "high" {
			t.Fatalf("native finding fields were mapped: %v", finding)
		}
		scopes, ok := finding["scopes"].(map[string]any)
		if !ok || scopes["mode"] != "explicit" {
			t.Fatalf("finding scopes were rewritten: %v", finding["scopes"])
		}
		productIDs := scopes["product_ids"].([]any)
		if len(productIDs) != 1 || productIDs[0] != "product" {
			t.Fatalf("finding product scopes were rewritten: %v", scopes)
		}
		sourceIDs := finding["source_ids"].([]any)
		if len(sourceIDs) != 2 || sourceIDs[0] != "s1" || sourceIDs[1] != "s2" {
			t.Fatalf("finding source ids were rewritten: %v", sourceIDs)
		}
		sources, ok := entry["sources"].([]any)
		if !ok || len(sources) != 2 {
			t.Fatalf("sources = %v, want the two linked descriptors", entry["sources"])
		}
		first, _ := sources[0].(map[string]any)
		second, _ := sources[1].(map[string]any)
		if first["source_id"] != "s1" || second["source_id"] != "s2" {
			t.Fatalf("sources are not the sorted linked descriptors: %v %v", first, second)
		}
		if first["pack_id"] != pack.PackID || first["revision"].(float64) != 1 || first["kind"] != "web" || first["title"] != "Source s1" {
			t.Fatalf("source descriptor lost its native fields: %v", first)
		}
		if strings.Contains(string(raw), `"s3"`) {
			t.Fatalf("unselected linked provenance leaked into the view: %s", raw)
		}
		groups, ok := document["domain_groups"].([]any)
		if !ok {
			t.Fatalf("view carries no domain_groups array: %s", raw)
		}
		grouped := false
		for _, group := range groups {
			groupMap, _ := group.(map[string]any)
			ids, _ := groupMap["finding_ids"].([]any)
			for _, id := range ids {
				if id == fmt.Sprintf("research:%d:0", seq) {
					grouped = true
				}
			}
		}
		if !grouped {
			t.Fatalf("the research view_id is absent from the existing domain_groups: %s", raw)
		}
	})

	t.Run("a zero-linked finding resolves with an empty sources array", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-zero-source")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "zero")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}, nil); err != nil {
			t.Fatalf("research declaration refused: %v", err)
		}
		document, raw := researchViewDocument(t, fixture)
		entry, _ := researchEntries(t, document, raw)[0].(map[string]any)
		if entry["state"] != "resolved" {
			t.Fatalf("zero-linked finding state = %v, want resolved", entry["state"])
		}
		sources, present := entry["sources"]
		if !present {
			t.Fatalf("resolved entry omits sources entirely: %s", raw)
		}
		if arr, ok := sources.([]any); !ok || len(arr) != 0 {
			t.Fatalf("sources = %v, want the empty array", sources)
		}
	})

	t.Run("a shared source descriptor repeats across two selected entries", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-shared")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "shared")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
			"research_finding_refs": []map[string]any{
				researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild),
				researchSelectionEntry(pack.PackID, 1, "f2", workContextTestChild),
			},
		}, nil); err != nil {
			t.Fatalf("research declaration refused: %v", err)
		}
		document, raw := researchViewDocument(t, fixture)
		entries := researchEntries(t, document, raw)
		if len(entries) != 2 {
			t.Fatalf("view carries %d research entries, want the authored order of two: %s", len(entries), raw)
		}
		second, _ := entries[1].(map[string]any)
		if second["view_id"] != researchViewIDForOrdinal(t, fixture, 1) {
			t.Fatalf("second entry view_id = %v, want the selection ordinal 1 namespace", second["view_id"])
		}
		firstFinding, _ := entries[0].(map[string]any)
		firstSources, _ := firstFinding["sources"].([]any)
		secondSources, _ := second["sources"].([]any)
		if len(firstSources) != 2 || len(secondSources) != 2 {
			t.Fatalf("selected entries lost linked descriptors: %v %v", firstSources, secondSources)
		}
		secondFirst, _ := secondSources[0].(map[string]any)
		if secondFirst["source_id"] != "s1" {
			t.Fatalf("second entry sources = %v, want s1 and s3 sorted", secondSources)
		}
	})

	t.Run("duplicate tuples refuse authoring even under another Domain", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-duplicate")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "dup")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
			"research_finding_refs": []map[string]any{
				researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild),
				researchSelectionEntry(pack.PackID, 1, "f1", workContextTestRoot),
			},
		}, nil)
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "repeats") {
			t.Fatalf("duplicate tuple error = %v, want %s refusing the repeated selection", err, KindInvalidPayload)
		}
	})

	t.Run("selection without the consuming pin refuses authoring", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-nopin")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "nopin")
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		}, nil)
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "no consuming binding") {
			t.Fatalf("unpinned selection error = %v, want %s refusing the unbound pack", err, KindInvalidPayload)
		}
	})

	t.Run("selection pinned beside another revision refuses authoring", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-mismatch")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "mismatch")
		for _, revision := range []int64{1, 2} {
			bindResearchPack(t, fixture, pack.PackID, 3-revision, true)
			err := recordResearchDeclarationTx(t, fixture, map[string]any{"finding_refs": []string{}, "context_findings": []map[string]any{}, "research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, revision, []string{"f1", "f3"}[revision-1], workContextTestChild)}}, nil)
			if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "revision") {
				t.Fatalf("mismatched pin error = %v, want %s refusing the unbound revision", err, KindInvalidPayload)
			}
		}
	})

	t.Run("a missing selected finding refuses authoring without effects", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-missing-finding")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "missing")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f-missing", workContextTestChild)},
		}, nil)
		if !hasFailureKind(err, KindProjectionNotFound) {
			t.Fatalf("missing finding error = %v, want %s", err, KindProjectionNotFound)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused declaration moved the version to %d, want %d", got, versionBefore)
		}
	})

	t.Run("a selection outside the declared Domain scope refuses authoring", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-domain")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "domain")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestUnaffected)},
		}, nil); !hasFailureKind(err, KindUnknownScope) {
			t.Fatalf("unaffected Domain selection error = %v, want %s", err, KindUnknownScope)
		}
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestRoot)},
		}, nil); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("root Domain without rationale error = %v, want %s", err, KindInvalidPayload)
		}
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
			"research_finding_refs": []map[string]any{{
				"pack_id": pack.PackID, "revision": 1, "finding_id": "f1",
				"domain_id": workContextTestChild, "product_wide_rationale": "the claim spans every Domain",
			}},
		}, nil); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("child Domain with rationale error = %v, want %s", err, KindInvalidPayload)
		}
	})

	t.Run("a selection entry with an unknown member refuses closed decoding", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-closed")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "closed")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
			"research_finding_refs": []map[string]any{{
				"pack_id": pack.PackID, "revision": 1, "finding_id": "f1",
				"domain_id": workContextTestChild, "statement": "a smuggled research body",
			}},
		}, nil)
		if !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("unknown selection member error = %v, want %s refusing the open shape", err, KindInvalidPayload)
		}
	})
}

// researchViewIDForOrdinal reads the declaration event's sequence for a
// selection ordinal's expected view_id.
func researchViewIDForOrdinal(t *testing.T, f workContextFixture, ordinal int) string {
	t.Helper()
	return fmt.Sprintf("research:%d:%d", latestDeclarationSeq(t, f), ordinal)
}

// Resolution precedence and typed drift: rebound, stale, inactive, and
// missing entries stay visible without content, fail closed on a missing
// binding, and translate the canonical bounded read to the view limit kind.
func TestWorkContextResearchDrift(t *testing.T) {
	t.Parallel()

	declare := func(t *testing.T, f workContextFixture, pack ResearchPack) {
		t.Helper()
		if err := recordResearchDeclarationTx(t, f, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		}, nil); err != nil {
			t.Fatalf("research declaration refused: %v", err)
		}
	}

	t.Run("a rebound binding reports the binding's required flag and never its content", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-rebound")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "rebound")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		declare(t, fixture, pack)
		bindResearchPack(t, fixture, pack.PackID, 2, true)
		document, raw := researchViewDocument(t, fixture)
		entry, _ := researchEntries(t, document, raw)[0].(map[string]any)
		if entry["state"] != "rebound" || entry["required"] != true {
			t.Fatalf("state = %v required = %v, want rebound and required", entry["state"], entry["required"])
		}
		assertUnresolvedEntryCarriesNoContent(t, entry, raw)
		bindResearchPack(t, fixture, pack.PackID, 2, false)
		document, raw = researchViewDocument(t, fixture)
		entry, _ = researchEntries(t, document, raw)[0].(map[string]any)
		if entry["state"] != "rebound" || entry["required"] != false {
			t.Fatalf("state = %v required = %v, want rebound and optional", entry["state"], entry["required"])
		}
		assertUnresolvedEntryCarriesNoContent(t, entry, raw)
	})

	t.Run("a stale selected revision reports stale and stays visible", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-stale-revision")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "stale-rev")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		declare(t, fixture, pack)
		var expected int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&expected); err != nil {
			t.Fatal(err)
		}
		if err := setResearchFreshness(context.Background(), fixture.store, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: expected, Freshness: ResearchStale, Revision: 1}); err != nil {
			t.Fatal(err)
		}
		document, raw := researchViewDocument(t, fixture)
		entry, _ := researchEntries(t, document, raw)[0].(map[string]any)
		if entry["state"] != "stale" {
			t.Fatalf("stale revision entry = %v, want stale", entry)
		}
		assertUnresolvedEntryCarriesNoContent(t, entry, raw)
	})

	t.Run("a non-current finding freshness reports stale", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-stale-finding")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "stale-find")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		declare(t, fixture, pack)
		if _, err := fixture.store.DatabaseForTesting().Exec(`UPDATE active_research_findings SET freshness='unknown' WHERE pack_id=? AND revision=1 AND finding_id='f1'`, pack.PackID); err != nil {
			t.Fatal(err)
		}
		document, _ := researchViewDocument(t, fixture)
		entry, _ := researchEntries(t, document, nil)[0].(map[string]any)
		if entry["state"] != "stale" {
			t.Fatalf("unknown finding freshness state = %v, want stale", entry["state"])
		}
	})

	t.Run("an inactive finding reports inactive", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-inactive")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "inactive")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		declare(t, fixture, pack)
		if _, err := fixture.store.DatabaseForTesting().Exec(`UPDATE active_research_findings SET status='contradicted' WHERE pack_id=? AND revision=1 AND finding_id='f1'`, pack.PackID); err != nil {
			t.Fatal(err)
		}
		document, _ := researchViewDocument(t, fixture)
		entry, _ := researchEntries(t, document, nil)[0].(map[string]any)
		if entry["state"] != "inactive" {
			t.Fatalf("contradicted finding state = %v, want inactive", entry["state"])
		}
	})

	t.Run("missing research state reports missing with fail-closed required", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(t *testing.T, f workContextFixture, pack ResearchPack)
		}{
			{"binding", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				injectResearchRowDelete(t, f, `DELETE FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, pack.PackID, f.workID)
			}},
			{"pack", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				// The retirement delete guard blocks removing an active
				// pack's row, so the injected incomplete read renames it.
				injectResearchWrite(t, f, `UPDATE active_research_packs SET pack_id='renamed-away' WHERE pack_id=?`, pack.PackID)
			}},
			{"revision", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				injectResearchRowDelete(t, f, `DELETE FROM active_research_revisions WHERE pack_id=? AND revision=1`, pack.PackID)
			}},
			{"finding", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				injectResearchRowDelete(t, f, `DELETE FROM active_research_findings WHERE pack_id=? AND revision=1 AND finding_id='f1'`, pack.PackID)
			}},
			{"linked source", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				injectResearchRowDelete(t, f, `DELETE FROM active_research_sources WHERE pack_id=? AND revision=1 AND source_id='s1'`, pack.PackID)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := seedWorkContextFixture(t, "ctx-research-missing-"+strings.ReplaceAll(tc.name, " ", "-"))
				defer fixture.store.Close()
				pack := seedResearchContextPack(t, fixture, "missing")
				bindResearchPack(t, fixture, pack.PackID, 1, true)
				declare(t, fixture, pack)
				tc.setup(t, fixture, pack)
				document, raw := researchViewDocument(t, fixture)
				entry, _ := researchEntries(t, document, raw)[0].(map[string]any)
				if entry["state"] != "missing" {
					t.Fatalf("%s state = %v, want missing", tc.name, entry["state"])
				}
				if entry["required"] != true {
					t.Fatalf("%s required = %v, want fail-closed required", tc.name, entry["required"])
				}
				assertUnresolvedEntryCarriesNoContent(t, entry, raw)
			})
		}
	})

	t.Run("every unresolved state leaves the finding and sources members absent", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			state string
			setup func(t *testing.T, f workContextFixture, pack ResearchPack)
		}{
			{"rebound", "rebound", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				bindResearchPack(t, f, pack.PackID, 2, true)
			}},
			{"stale", "stale", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				var expected int64
				if err := f.store.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&expected); err != nil {
					t.Fatal(err)
				}
				if err := setResearchFreshness(context.Background(), f.store, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: expected, Freshness: ResearchStale, Revision: 1}); err != nil {
					t.Fatal(err)
				}
			}},
			{"inactive", "inactive", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				if _, err := f.store.DatabaseForTesting().Exec(`UPDATE active_research_findings SET status='superseded' WHERE pack_id=? AND revision=1 AND finding_id='f1'`, pack.PackID); err != nil {
					t.Fatal(err)
				}
			}},
			{"missing", "missing", func(t *testing.T, f workContextFixture, pack ResearchPack) {
				injectResearchRowDelete(t, f, `DELETE FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, pack.PackID, f.workID)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := seedWorkContextFixture(t, "ctx-research-absent-"+tc.name)
				defer fixture.store.Close()
				pack := seedResearchContextPack(t, fixture, "absent")
				bindResearchPack(t, fixture, pack.PackID, 1, true)
				if err := recordResearchDeclarationTx(t, fixture, map[string]any{
					"finding_refs":          []string{},
					"context_findings":      []map[string]any{},
					"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
				}, nil); err != nil {
					t.Fatalf("research declaration refused: %v", err)
				}
				tc.setup(t, fixture, pack)
				document, raw := researchViewDocument(t, fixture)
				entry, _ := researchEntries(t, document, raw)[0].(map[string]any)
				if entry["state"] != tc.state {
					t.Fatalf("%s state = %v, want %s", tc.name, entry["state"], tc.state)
				}
				assertUnresolvedEntryCarriesNoContent(t, entry, raw)
			})
		}
	})

	t.Run("authoring a selection past the canonical completeness bound refuses through the same typed bridge", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-author-overflow")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "author-overflow")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		for i := 0; i < 1001; i++ {
			if _, err := fixture.store.DatabaseForTesting().Exec(fmt.Sprintf(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES('%s',1,'sx%04d','web','https://example.com/x','X','Example','2026-10-01T00:00:00Z')`, pack.PackID, i)); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DatabaseForTesting().Exec(fmt.Sprintf(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES('%s',1,'f1','sx%04d')`, pack.PackID, i)); err != nil {
				t.Fatal(err)
			}
		}
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		}, nil)
		if !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("authoring overflow error = %v, want %s from the same typed bridge, never the raw canonical kind", err, KindLimitExceeded)
		}
		if hasFailureKind(err, KindInvalidOperation) {
			t.Fatalf("authoring leaked the raw canonical bounded kind: %v", err)
		}
	})

	t.Run("a failing provenance read propagates its kind, operation, and cause", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-sql-error")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "sql-error")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		}, nil); err != nil {
			t.Fatalf("research declaration refused: %v", err)
		}
		sentinel := errors.New("the provenance cursor failed")
		failing := researchFailingQueryer{db: fixture.store.DatabaseForTesting(), err: sentinel}
		_, readErr := readWorkContextView(context.Background(), failing, fixture.workID)
		var failure *Failure
		if readErr == nil || !errors.As(readErr, &failure) || failure.Kind != KindUnavailable || failure.Op != "research_mutation" {
			t.Fatalf("failing provenance read = %v, want %s from the provenance operation", readErr, KindUnavailable)
		}
		if !errors.Is(readErr, sentinel) {
			t.Fatalf("failing provenance read lost its source cause: %v", readErr)
		}
	})

	t.Run("the typed error bridge translates only the canonical bounded pair", func(t *testing.T) {
		bounded := &Failure{Kind: KindInvalidOperation, Op: "research_read", Detail: "selected finding-source links exceed the bounded read limit"}
		translated := translateWorkContextResearchReadError(bounded)
		var failure *Failure
		if !errors.As(translated, &failure) || failure.Kind != KindLimitExceeded || failure.Op != "work_context_read" || !errors.Is(translated, bounded) {
			t.Fatalf("bounded read translation = %v, want the view limit kind preserving the source cause", translated)
		}
		other := &Failure{Kind: KindInvalidOperation, Op: "research_mutation", Detail: "a different canonical refusal"}
		if got := translateWorkContextResearchReadError(other); got != error(other) {
			t.Fatalf("other invalid-operation error was rewritten: %v", got)
		}
		notFound := &Failure{Kind: KindProjectionNotFound, Op: "research_mutation", Detail: "finding does not exist"}
		if !isResearchNotFound(notFound) {
			t.Fatal("the typed not-found failure was not recognized")
		}
		unavailable := &Failure{Kind: KindUnavailable, Op: "research_mutation"}
		boundedKind := &Failure{Kind: KindInvalidOperation, Op: "work_context_read"}
		if isResearchNotFound(unavailable) || isResearchNotFound(boundedKind) {
			t.Fatal("a non-not-found failure was misread as missing research")
		}
	})

	t.Run("canonical provenance overflow translates to the view limit kind", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-link-overflow")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "overflow")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		declare(t, fixture, pack)
		for i := 0; i < 999; i++ {
			sourceID := fmt.Sprintf("sx%03d", i)
			if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,1,?,'web','https://example.com/x','X','Example','2026-10-01T00:00:00Z')`, pack.PackID, sourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,1,'f1',?)`, pack.PackID, sourceID); err != nil {
				t.Fatal(err)
			}
		}
		_, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		if !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("provenance overflow error = %v, want %s translated from the bounded read", err, KindLimitExceeded)
		}
		if hasFailureKind(err, KindInvalidOperation) {
			t.Fatalf("provenance overflow kept the raw canonical kind: %v", err)
		}
	})
}

// researchBoundFinding builds one legal declaration finding whose statement
// carries the padded text with the given number of bounded evidence refs.
func researchBoundFinding(statement string, evidenceRefs int) map[string]any {
	refs := make([]string, 0, evidenceRefs)
	for i := 0; i < evidenceRefs; i++ {
		refs = append(refs, strings.Repeat("e", 256))
	}
	return map[string]any{
		"kind": "observation", "statement": statement,
		"subject_ref":   strings.Repeat("s", 128),
		"evidence_refs": refs,
		"domain_id":     workContextTestChild,
	}
}

// researchBoundReadings builds the bounded readings the byte boundary's
// declaration carries, so readings and group bytes participate in the
// measured whole.
func researchBoundReadings(f workContextFixture) []map[string]any {
	readings := make([]map[string]any, 0, 32)
	for i := 0; i < 31; i++ {
		readings = append(readings, map[string]any{
			"domain_id": workContextTestChild,
			"reason":    fmt.Sprintf("the bounded boundary reading %02d carries its reason text", i),
			"source": map[string]any{"kind": "repository_file", "project_id": f.sources.repoProject,
				"path": f.sources.repoPath, "commit_oid": f.sources.repoCommit},
		})
	}
	return readings
}

// researchBoundCarrierDeclaration records the fixed 16-finding carrier
// declaration the tuned declaration selects beside its own 16 findings, so
// the view holds 32 findings while each declaration stays inside its own
// 16-finding bound.
func researchBoundCarrierDeclaration(t *testing.T, f workContextFixture, statementLen int) int64 {
	t.Helper()
	findings := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		findings = append(findings, researchBoundFinding(researchBoundStatement(statementLen), 3))
	}
	if err := f.recordWorkContext(t, map[string]any{"context_findings": findings}); err != nil {
		t.Fatalf("carrier declaration refused: %v", err)
	}
	var seq int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// researchBoundDeclaration records the tuned boundary declaration once: the
// bounded readings, the 16 selected carrier findings, 16 new findings (the
// first of statement length firstLen, the rest restLen, the byte-granular
// pad), and one research selection. On failure it returns the refusal
// instead of failing the test.
func researchBoundDeclaration(t *testing.T, f workContextFixture, packID string, carrierSeq int64, firstLen, restLen int) error {
	t.Helper()
	findings := make([]map[string]any, 0, 15)
	findings = append(findings, researchBoundFinding(researchBoundStatement(firstLen), 3))
	for i := 1; i < 15; i++ {
		findings = append(findings, researchBoundFinding(researchBoundStatement(restLen), 3))
	}
	refs := make([]string, 0, 16)
	for ordinal := 0; ordinal < 16; ordinal++ {
		refs = append(refs, fmt.Sprintf("finding:%d:%d", carrierSeq, ordinal))
	}
	readings := researchBoundReadings(f)
	readings = append(readings, f.repoReading(workContextTestRoot, workContextRootRationale, ""))
	return recordResearchDeclarationTx(t, f, map[string]any{
		"required_reading":      readings,
		"finding_refs":          refs,
		"context_findings":      findings,
		"research_finding_refs": []map[string]any{researchSelectionEntry(packID, 1, "f0", workContextTestChild)},
	}, nil)
}

// tuneResearchBoundView lands the whole serialized view at exactly target
// bytes by tuning the 16 tuned findings' statement length one byte at a
// time. A view that overshoots the bound refuses its own read, so the loop
// shrinks and re-approaches from below until the exact landing holds.
func tuneResearchBoundView(t *testing.T, f workContextFixture, packID string, target int) (carrierSeq int64, firstLen, restLen int) {
	t.Helper()
	carrierSeq = researchBoundCarrierDeclaration(t, f, 500)
	firstLen, restLen = 500, 500
	if err := researchBoundDeclaration(t, f, packID, carrierSeq, firstLen, restLen); err != nil {
		t.Fatalf("bound declaration refused: %v", err)
	}
	for iteration := 0; iteration < 16; iteration++ {
		view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
		if err != nil {
			// The landing overshot the bound, so the read refuses; shrink
			// and re-approach from below.
			restLen -= 2
			if restLen <= 32 {
				t.Fatalf("view tuning cannot recover below %d bytes: %v", target, err)
			}
			if err := researchBoundDeclaration(t, f, packID, carrierSeq, firstLen, restLen); err != nil {
				t.Fatalf("bound declaration refused while recovering: %v", err)
			}
			continue
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		delta := target - len(raw)
		if delta == 0 {
			return carrierSeq, firstLen, restLen
		}
		per := delta / 15
		remainder := delta - per*15
		firstLen += per + remainder
		restLen += per
		if firstLen <= 32 || firstLen > 1024 || restLen <= 32 || restLen > 1024 {
			t.Fatalf("view tuning needs statement lengths %d/%d outside the 1024-byte finding bound; recalibrate the fixture", firstLen, restLen)
		}
		if err := researchBoundDeclaration(t, f, packID, carrierSeq, firstLen, restLen); err != nil {
			t.Fatalf("bound declaration refused: %v", err)
		}
	}
	t.Fatalf("view tuning did not converge on %d bytes", target)
	return carrierSeq, firstLen, restLen
}

// The 32-slot and 64 KiB bounds measure the complete shared view: generic
// and research slots share one count, omissions consume slots, and the whole
// serialized view answers to one 64 KiB budget with readings, groups, oracle
// metadata, and research entries included.
func TestWorkContextResearchSharedBounds(t *testing.T) {
	t.Parallel()

	t.Run("32 mixed slots including omissions admit and 33 refuse", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-bounds-count")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "bounds")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		findings := make([]map[string]any, 0, 16)
		for i := 0; i < 16; i++ {
			findings = append(findings, workContextFinding(workContextTestChild, "observation", fmt.Sprintf("the generic slot finding %02d", i), ""))
		}
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": findings}); err != nil {
			t.Fatalf("generic declaration refused: %v", err)
		}
		// One ranked slot rides a historical report's Review block: the
		// mixed 32-slot view spans generic, ranked, and research entries.
		review := reviewBlockFixture()
		review.Findings = []ReviewFinding{{FindingID: "finding:mixed-count", Severity: "P2", Confidence: "low", Detail: "the ranked mixed-count blocker", Status: "open", Blocking: true, InScope: true, EvidenceRefs: []string{}, ClosureRefs: []string{}}}
		rankedSeq := seedContextFactTerminal(t, fixture, "worker.completed", map[string]any{"review": review})
		var seq int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		refs := make([]string, 0, 16)
		for ordinal := 0; ordinal < 15; ordinal++ {
			refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, ordinal))
		}
		refs = append(refs, fmt.Sprintf("finding:%d:0", rankedSeq))
		exec := func(tag, query string, args ...any) {
			t.Helper()
			if _, err := fixture.store.DatabaseForTesting().Exec(query, args...); err != nil {
				t.Fatalf("slot fixture statement %q failed: %v", tag, err)
			}
		}
		for i := 0; i < 15; i++ {
			exec("slot finding", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,?,'observation','slot finding','high','current','active','home')`, pack.PackID, fmt.Sprintf("f%02d", i))
		}
		exec("inactive omission", `UPDATE active_research_findings SET status='superseded' WHERE pack_id=? AND revision=1 AND finding_id='f0'`, pack.PackID)
		selections := make([]map[string]any, 0, 17)
		for i := 0; i < 15; i++ {
			selections = append(selections, researchSelectionEntry(pack.PackID, 1, fmt.Sprintf("f%02d", i), workContextTestChild))
		}
		selections = append(selections, researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild))
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs": refs, "context_findings": []map[string]any{}, "research_finding_refs": selections,
		}, nil); err != nil {
			t.Fatalf("mixed 32-slot declaration refused: %v", err)
		}
		document, raw := researchViewDocument(t, fixture)
		entries := researchEntries(t, document, raw)
		generic := document["findings"].([]any)
		if len(generic)+len(entries) != 32 {
			t.Fatalf("view slots = %d generic + %d research, want 32 total", len(generic), len(entries))
		}
		inactive := entries[15].(map[string]any)
		if inactive["state"] != "inactive" {
			t.Fatalf("omitted entry state = %v, want the inactive omission still visible in its slot", inactive["state"])
		}
		// One more selection rides the same anchor replacement to 33 total
		// slots: the shared count refuses whichever member carries it, and
		// the declaration refuses deterministically with zero effects.
		selections = append(selections, researchSelectionEntry(pack.PackID, 1, "f2", workContextTestChild))
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		eventsBefore := declarationEventCount(t, fixture)
		before := outsideRepairDatabaseSnapshot(t, fixture.store)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs": refs, "context_findings": []map[string]any{}, "research_finding_refs": selections,
		}, nil)
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "32") {
			t.Fatalf("33-slot declaration error = %v, want the deterministic %s refusing past the 32 shared slots", err, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused 33-slot declaration moved the version to %d, want %d", got, versionBefore)
		}
		if got := declarationEventCount(t, fixture); got != eventsBefore {
			t.Fatalf("refused 33-slot declaration left %d declaration events, want %d", got, eventsBefore)
		}
		if after := outsideRepairDatabaseSnapshot(t, fixture.store); after != before {
			t.Fatal("refused 33-slot declaration changed database state")
		}
	})

	t.Run("the whole view admits exactly at 65536 bytes", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-bound-exact")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "exact")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		tuneResearchBoundView(t, fixture, pack.PackID, 65536)
		view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		if err != nil {
			t.Fatalf("the view at exactly the whole-view bound refused: %v", err)
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != 65536 {
			t.Fatalf("tuned view measures %d bytes, want exactly 65536", len(raw))
		}
	})

	t.Run("one byte over the whole view refuses the declaration whole", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-bound-over")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "over")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		carrierSeq, firstLen, restLen := tuneResearchBoundView(t, fixture, pack.PackID, 65536)
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		eventsBefore := declarationEventCount(t, fixture)
		before := outsideRepairDatabaseSnapshot(t, fixture.store)
		err := researchBoundDeclaration(t, fixture, pack.PackID, carrierSeq, firstLen+1, restLen)
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "65536") {
			t.Fatalf("over-bound declaration error = %v, want the deterministic %s naming the 65536-byte bound", err, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused over-bound declaration moved the version to %d, want %d", got, versionBefore)
		}
		if got := declarationEventCount(t, fixture); got != eventsBefore {
			t.Fatalf("refused over-bound declaration left %d declaration events, want %d", got, eventsBefore)
		}
		if after := outsideRepairDatabaseSnapshot(t, fixture.store); after != before {
			t.Fatal("refused over-bound declaration changed database state")
		}
	})

	t.Run("the legacy findings-only measurement would admit what the whole view refuses", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-bound-legacy")
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "legacy")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		for i := 0; i < 12; i++ {
			if _, err := fixture.store.DatabaseForTesting().Exec(fmt.Sprintf(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES('%s',1,'g%02d','observation','gap finding','high','current','active','home')`, pack.PackID, i)); err != nil {
				t.Fatal(err)
			}
		}
		// Eighteen oversized-but-legal generic findings keep the findings
		// array under 64 KiB, while the padded readings, the groups, and the
		// fourteen research entries push the whole serialized view over:
		// exactly the population the findings-only measurement never saw.
		legacyFinding := func(index int) map[string]any {
			return researchBoundFinding(legacyGapStatement(index), 8)
		}
		carrier := make([]map[string]any, 0, 9)
		for i := 0; i < 9; i++ {
			carrier = append(carrier, legacyFinding(i))
		}
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": carrier}); err != nil {
			t.Fatalf("legacy carrier declaration refused: %v", err)
		}
		var carrierSeq int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&carrierSeq); err != nil {
			t.Fatal(err)
		}
		findings := make([]map[string]any, 0, 9)
		for i := 0; i < 9; i++ {
			findings = append(findings, legacyFinding(i+9))
		}
		refs := make([]string, 0, 9)
		for ordinal := 0; ordinal < 9; ordinal++ {
			refs = append(refs, fmt.Sprintf("finding:%d:%d", carrierSeq, ordinal))
		}
		selections := make([]map[string]any, 0, 14)
		selections = append(selections,
			researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild),
			researchSelectionEntry(pack.PackID, 1, "f2", workContextTestChild))
		for i := 0; i < 12; i++ {
			selections = append(selections, researchSelectionEntry(pack.PackID, 1, fmt.Sprintf("g%02d", i), workContextTestChild))
		}
		readings := make([]map[string]any, 0, 32)
		for i := 0; i < 31; i++ {
			readings = append(readings, map[string]any{
				"domain_id": workContextTestChild,
				"reason":    fmt.Sprintf("the legacy-gap boundary reading %02d carries its padded reason text ", i) + strings.Repeat("r", 440),
				"source": map[string]any{"kind": "repository_file", "project_id": fixture.sources.repoProject,
					"path": fixture.sources.repoPath, "commit_oid": fixture.sources.repoCommit},
			})
		}
		readings = append(readings, fixture.repoReading(workContextTestRoot, workContextRootRationale, ""))
		// The measure-gap proof: the view's 18 findings alone marshal well
		// under the 64 KiB bound, so only the readings, groups, and research
		// bytes the findings-only measurement never saw can carry the whole
		// view past it.
		findingBytes := len(mustJSONValue(findings)) + len(mustJSONValue(carrier))
		if findingBytes >= WorkContextCombinedMaxBytes {
			t.Fatalf("authored findings array measures %d bytes, want clearly under the bound so the gap is real", findingBytes)
		}
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		eventsBefore := declarationEventCount(t, fixture)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"required_reading": readings, "finding_refs": refs, "context_findings": findings, "research_finding_refs": selections,
		}, nil)
		var failure *Failure
		// The live post-append check refuses the declaration whole and
		// deterministically.
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "bytes of canonical JSON") {
			t.Fatalf("legacy-gap declaration refusal = %v, want %s naming the whole-view byte bound", err, KindLimitExceeded)
		}
		// The optional reduction is named, and the mandatory supersession or
		// stop route is explicitly conditional — named for the state it
		// describes, never an unconditional command about this overflow.
		if !strings.Contains(failure.RecoveryAction, "reduce the declaration's optional context") {
			t.Fatalf("legacy-gap remedy = %q, want the optional reduction named", failure.RecoveryAction)
		}
		if !strings.Contains(failure.RecoveryAction, "if mandatory context alone exceeds the bound") {
			t.Fatalf("legacy-gap remedy = %q, want the mandatory route explicitly conditional, not an exclusive command", failure.RecoveryAction)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused legacy-gap declaration moved the version to %d, want %d", got, versionBefore)
		}
		if got := declarationEventCount(t, fixture); got != eventsBefore {
			t.Fatalf("refused legacy-gap declaration left %d declaration events, want %d", got, eventsBefore)
		}
	})

	t.Run("an over-bound declaration whose optional context causes the overflow names the reduce remedy", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-bound-mandatory")
		defer fixture.store.Close()
		pack := seedMandatoryResearchOverflow(t, fixture, "mandatory")
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          mandatoryOverflowRefs(t, fixture),
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}})
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.RecoveryAction, "reduce the declaration's optional context") {
			t.Fatalf("over-bound declaration error = %v, want %s naming the reduce-optional-context remedy", err, KindLimitExceeded)
		}
	})

	t.Run("a mandatory-only view one byte over the bound names the mandatory recovery even when its unframed members fit", func(t *testing.T) {
		// The boundary view is legitimate mandatory-only shape: 24 open
		// ranked blockers, each inside its closed bounds, whose whole
		// serialization lands on exactly 65537 canonical bytes. Member
		// names, delimiters, identity fields, and the enclosing arrays
		// carry the overflow: the unframed sum of the mandatory members
		// stays at or under 65536, so a measurement that skips them would
		// wrongly promise that reducing optional context repairs the view.
		buildView := func(commonRefLen, firstRefLen int) WorkContextView {
			view := WorkContextView{SourceEventFrontier: 7}
			for i := 0; i < 24; i++ {
				refs := make([]string, 0, 8)
				for ref := 0; ref < 8; ref++ {
					length := 256
					if i == 0 && ref == 0 {
						length = firstRefLen
					} else if ref == 7 {
						length = commonRefLen
					}
					refs = append(refs, fmt.Sprintf("evidence:boundary-%02d-%s", i, strings.Repeat("e", length-21)))
				}
				view.Findings = append(view.Findings, WorkContextFindingView{
					FindingID:      fmt.Sprintf("finding:%d:0", 100+i),
					Statement:      fmt.Sprintf("the mandatory boundary blocker %02d carries its sized detail ", i) + strings.Repeat("m", 455),
					SubjectRef:     "owner:repair",
					EvidenceRefs:   refs,
					DomainID:       workContextTestChild,
					Origin:         WorkContextOriginWorkerReport,
					Status:         WorkContextFindingStatusOpen,
					SourceEventID:  "source:event",
					SourceEventSeq: int64(100 + i),
					Ordinal:        0,
					SourceKind:     WorkContextSourceKindReviewFinding,
				})
			}
			return view
		}
		view := buildView(256, 256)
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		delta := len(raw) - 65537
		if delta < 0 {
			t.Fatalf("boundary fixture measures %d bytes, want at least 65537 before tuning", len(raw))
		}
		per := delta / 24
		remainder := delta - per*24
		view = buildView(256-per, 256-remainder)
		raw, err = json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != 65537 {
			t.Fatalf("boundary view measures %d bytes, want exactly 65537", len(raw))
		}
		unframed := len(mustJSONValue(view.SubjectCommit)) +
			len(mustJSONValue(view.OraclePreparations))
		for _, finding := range view.Findings {
			if finding.Status == WorkContextFindingStatusOpen {
				unframed += len(mustJSONValue(finding))
			}
		}
		if unframed > WorkContextCombinedMaxBytes {
			t.Fatalf("unframed mandatory sum = %d bytes, want at most 65536 so the framing bytes carry the overflow", unframed)
		}
		// The shared bound prose is the one recovery text the count and byte
		// refusals carry. At this edge — a whole view one byte over its
		// bound while the unframed mandatory members fit inside it — the
		// text must still name the optional reduction and the explicitly
		// conditional mandatory supersession or stop route, never an
		// approximate measurement's false promise.
		recovery := workContextOverflowRecovery()
		if !strings.Contains(recovery, "reduce the declaration's optional context") {
			t.Fatalf("boundary recovery text = %q, want the optional reduction named", recovery)
		}
		if !strings.Contains(recovery, "if mandatory context alone exceeds the bound") || !strings.Contains(recovery, "supersede the active contract") || !strings.Contains(recovery, "stop the work") {
			t.Fatalf("boundary recovery text = %q, want the mandatory supersession or stop route explicitly conditional; the unframed members fit inside the bound, so an unconditional mandatory command would be as false as an optional-only promise", recovery)
		}
	})

	t.Run("33 open ranked slots refuse with the real mandatory recovery instead of promising drop repairs", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-count-mandatory")
		defer fixture.store.Close()
		seedMandatoryNativeEvidence(t, fixture, 33)
		_, readErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		var failure *Failure
		if readErr == nil || !errors.As(readErr, &failure) || failure.Kind != KindLimitExceeded {
			t.Fatalf("33-open-slot read = %v, want %s refusing past the 32 shared slots", readErr, KindLimitExceeded)
		}
		// The open ranked blockers cannot be dropped by a later declaration,
		// so the recovery must name the approved contract supersession or
		// stop route rather than promise that dropping findings repairs the
		// view.
		if !strings.Contains(failure.RecoveryAction, "supersede the active contract") || !strings.Contains(failure.RecoveryAction, "stop the work") {
			t.Fatalf("33-open-slot refusal remedy = %q, want the mandatory supersession or stop route; the overflow rides open ranked slots no declaration can drop", failure.RecoveryAction)
		}
	})

	t.Run("mandatory native evidence alone overflows the whole view and the declaration refuses atomically", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-mandatory-native")
		defer fixture.store.Close()
		seedMandatoryNativeEvidence(t, fixture, 24)
		// The read refuses on the evidence no declaration can reduce alone,
		// and the refusal names the approved contract supersession or stop
		// route, never a promise that dropping research repairs it.
		_, readErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		var failure *Failure
		if readErr == nil || !errors.As(readErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.RecoveryAction, "supersede the active contract") {
			t.Fatalf("mandatory-only read = %v, want %s naming the approved contract supersession or stop remedy", readErr, KindLimitExceeded)
		}
		// The mandatory evidence stays retained: the qualified completion
		// and its bytes survive untouched beside the refusal.
		retained := mandatoryRetainedSnapshot(t, fixture)
		pack := seedResearchContextPack(t, fixture, "mandatory-native")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		eventsBefore := declarationEventCount(t, fixture)
		before := outsideRepairDatabaseSnapshot(t, fixture.store)
		declErr := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}})
		if declErr == nil || !errors.As(declErr, &failure) || failure.Kind != KindLimitExceeded {
			t.Fatalf("mandatory-only declaration error = %v, want %s refusing whole", declErr, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused declaration moved the version to %d, want %d", got, versionBefore)
		}
		if got := declarationEventCount(t, fixture); got != eventsBefore {
			t.Fatalf("refused declaration left %d declaration events, want %d", got, eventsBefore)
		}
		if outsideRepairDatabaseSnapshot(t, fixture.store) != before {
			t.Fatal("refused declaration changed durable state")
		}
		after := mandatoryRetainedSnapshot(t, fixture)
		if len(after) != len(retained) {
			t.Fatalf("retained completion events = %d, want the original %d", len(after), len(retained))
		}
		for eventID, payload := range retained {
			if after[eventID] != payload {
				t.Fatalf("retained completion %s lost its recorded bytes", eventID)
			}
		}
	})
}

// The trusted projection fixture isolates mandatory-view bounds from action
// admission. Native preparations retain their exact qualified subject; each
// open Review finding retains a source identity and its declared Domain.
func seedMandatoryNativeEvidence(t *testing.T, f workContextFixture, blockers int) {
	t.Helper()
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	qualifyReviewSubjectForTest(t, f.store, f.workID, repo)
	oracle := acceptanceOracleFieldsWithoutPreparationsForTest(t, f.store, f.workID)
	seedOraclePreparationFieldsFixture(t, f.store, f.workID, oracle)
	state := WorkflowReviewState{Findings: []ReviewFinding{}, findingSources: map[string]reviewFindingSource{}}
	for i := 0; i < blockers; i++ {
		refs := make([]string, 0, 8)
		for ref := 0; ref < 8; ref++ {
			refs = append(refs, fmt.Sprintf("evidence:mandatory-%02d-%s", i, strings.Repeat("e", 232)))
		}
		id := fmt.Sprintf("finding:mandatory-%02d", i)
		state.Findings = append(state.Findings, ReviewFinding{
			FindingID: id, Severity: "P1", Confidence: "high", InScope: true, Blocking: true, Status: "open",
			Detail:       fmt.Sprintf("the mandatory ranked blocker %02d carries its sized detail ", i) + strings.Repeat("m", 455),
			EvidenceRefs: refs, Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair", FailureFamily: fmt.Sprintf("mandatory-family-%02d", i), ControlIDs: []string{"control:fixture"}, EvidenceRefs: refs},
		})
		state.findingSources[id] = reviewFindingSource{CanonicalID: fmt.Sprintf("finding:%d:0", 100+i), DomainID: workContextTestChild, EventID: "mandatory-review-" + id, Kind: WorkflowActionCompleted, Seq: int64(100 + i)}
	}
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		scope, err := beginWrite(t.Context(), tx.tx)
		if err != nil {
			return err
		}
		if err := writeWorkflowReviewFacts(t.Context(), tx.tx, f.workID, state); err != nil {
			return err
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

// Refusals preserve the exact current Review facts and native custody bytes.
func mandatoryRetainedSnapshot(t *testing.T, f workContextFixture) map[string]string {
	t.Helper()
	rows, err := f.store.DatabaseForTesting().Query(`SELECT work_id,state FROM workflow_review_state WHERE work_id=? UNION ALL SELECT lease_id,coalesce(result_json,'')||coalesce(native_plan_json,'') FROM worktree_verify_leases WHERE work_id=? ORDER BY 1`, f.workID, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	snapshot := map[string]string{}
	for rows.Next() {
		var eventID, payload string
		if err := rows.Scan(&eventID, &payload); err != nil {
			t.Fatal(err)
		}
		snapshot[eventID] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// legacyGapStatement builds the bounded statement the legacy-gap findings
// carry: sized so the findings array alone stays under the 64 KiB bound
// while the readings, groups, and research bytes push the whole view over.
func legacyGapStatement(index int) string {
	return fmt.Sprintf("the small generic finding %02d carries its sized statement ", index) + strings.Repeat("g", 960)
}

// mandatoryOverflowRefs selects the generic findings the optional
// overflow fixture holds, so a research declaration under test keeps
// that optional population in its resulting view.
func mandatoryOverflowRefs(t *testing.T, f workContextFixture) []string {
	t.Helper()
	var seqs []int64
	rows, err := f.store.DatabaseForTesting().Query(`SELECT r.effect_seq FROM effect_receipts r WHERE r.subject_id=? AND r.kind=? AND EXISTS(SELECT 1 FROM work_context_findings f WHERE f.work_id=r.subject_id AND f.source_seq=r.effect_seq AND f.finding_class='generic') ORDER BY r.effect_seq`, f.workID, WorkflowWorkContextRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 2 {
		t.Fatalf("mandatory overflow findings-bearing declarations = %d, want two", len(seqs))
	}
	refs := make([]string, 0, 32)
	for _, seq := range seqs {
		for ordinal := 0; ordinal < 16; ordinal++ {
			refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, ordinal))
		}
	}
	// The research entry shares the 32-slot bound with the optional
	// generic findings, so the over-bound scenario selects 31 of them.
	return refs[:31]
}

// seedMandatoryResearchOverflow drives the current view past the 64 KiB
// whole-view byte bound with an already-admitted historical declaration
// whose 32 heavy findings are optional context a later declaration can drop or replace.
func seedMandatoryResearchOverflow(t *testing.T, f workContextFixture, key string) ResearchPack {
	t.Helper()
	pack := seedResearchContextPack(t, f, key)
	declare := func() {
		t.Helper()
		findings := make([]map[string]any, 0, 16)
		for i := 0; i < 16; i++ {
			findings = append(findings, researchBoundFinding(researchBoundStatement(1024), 8))
		}
		if err := f.recordWorkContext(t, map[string]any{"context_findings": findings}); err != nil {
			t.Fatalf("mandatory overflow declaration refused: %v", err)
		}
	}
	declare()
	declare()
	var seqs []int64
	rows, err := f.store.DatabaseForTesting().Query(`SELECT r.effect_seq FROM effect_receipts r WHERE r.subject_id=? AND r.kind=? AND EXISTS(SELECT 1 FROM work_context_findings f WHERE f.work_id=r.subject_id AND f.source_seq=r.effect_seq AND f.finding_class='generic') ORDER BY r.effect_seq`, f.workID, WorkflowWorkContextRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 2 {
		t.Fatalf("mandatory overflow findings-bearing declarations = %d, want two", len(seqs))
	}
	refs := make([]string, 0, 32)
	for _, seq := range seqs {
		for ordinal := 0; ordinal < 16; ordinal++ {
			refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, ordinal))
		}
	}
	// The final over-bound selection rides the historical fold route on the
	// pinned workflow identity, never as a live action.
	recordHistoricalWorkContext(t, f, map[string]any{"finding_refs": refs, "context_findings": []map[string]any{}})
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err == nil {
		t.Fatalf("the optional overflow fixture view measures under the bound: %+v", view)
	}
	return pack
}

// A research declaration whose resulting shared view refuses leaves no
// effect: the same-action binding, the declaration event, and the work
// version all roll back, and the operation can re-attempt cleanly.
func TestWorkContextResearchDeclarationRollback(t *testing.T) {
	t.Parallel()
	t.Run("a refused over-bound declaration rolls back binding, event, and version", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-rollback")
		defer fixture.store.Close()
		pack := seedMandatoryResearchOverflow(t, fixture, "rollback")
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		eventsBefore := declarationEventCount(t, fixture)
		err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          mandatoryOverflowRefs(t, fixture),
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}})
		if err == nil {
			t.Fatalf("the over-bound research declaration committed; want the post-append refusal with zero effects")
		}
		if !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("rollback refusal error = %v, want %s", err, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("refused declaration moved the version to %d, want %d", got, versionBefore)
		}
		if got := declarationEventCount(t, fixture); got != eventsBefore {
			t.Fatalf("refused declaration left %d declaration events, want %d", got, eventsBefore)
		}
		var pins int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, pack.PackID, fixture.workID).Scan(&pins); err != nil {
			t.Fatal(err)
		}
		if pins != 0 {
			t.Fatalf("the rolled-back same-action binding left %d consumer pins", pins)
		}
		// A later bounded declaration that repairs the generic overflow first
		// leaves the operation free to re-attempt with research.
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "observation", "the bounded repair declaration", ""),
		}}); err != nil {
			t.Fatalf("repair declaration refused: %v", err)
		}
		if err := recordResearchDeclarationTx(t, fixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}}); err != nil {
			t.Fatalf("re-attempted research declaration refused after the repair: %v", err)
		}
	})

	t.Run("a refused declaration rolls its idempotency receipt back with the callback transaction", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-research-idem-rollback")
		defer fixture.store.Close()
		pack := seedMandatoryResearchOverflow(t, fixture, "rollback-idem")
		key := MutationIdempotencyKey{PrincipalRef: "principal/operator", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "research-mirror-" + fixture.workID}
		fields := map[string]any{
			"finding_refs":          mandatoryOverflowRefs(t, fixture),
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f0", workContextTestChild)},
		}
		bindings := []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}}
		if err := recordResearchDeclarationTxReceipt(t, fixture, fields, bindings, &key); !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("over-bound mirror error = %v, want %s refusing whole", err, KindLimitExceeded)
		}
		if _, found, lookupErr := fixture.store.LookupMutationIdempotency(context.Background(), key); lookupErr != nil || found {
			t.Fatalf("the refused transaction left its idempotency receipt behind: found=%t err=%v", found, lookupErr)
		}
		// The freed key re-executes instead of replaying: the identical
		// request re-derives the same over-bound view and refuses on its own
		// merits, with no residue from the first attempt.
		if err := recordResearchDeclarationTxReceipt(t, fixture, fields, bindings, &key); !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("same-key re-execution error = %v, want the same %s refusal with no residue", err, KindLimitExceeded)
		}
		// A request that can commit persists its receipt with the committed
		// transaction. The replay short-circuit of the authenticated
		// mutation boundary is that boundary's own owner; this mirror proves
		// only same-transaction receipt rollback and commit persistence.
		commitFixture := seedWorkContextFixture(t, "ctx-research-idem-commit")
		defer commitFixture.store.Close()
		commitPack := seedResearchContextPack(t, commitFixture, "idem-commit")
		bindResearchPack(t, commitFixture, commitPack.PackID, 1, true)
		commitKey := MutationIdempotencyKey{PrincipalRef: "principal/operator", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "research-mirror-" + commitFixture.workID}
		if err := recordResearchDeclarationTxReceipt(t, commitFixture, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(commitPack.PackID, 1, "f1", workContextTestChild)},
		}, nil, &commitKey); err != nil {
			t.Fatalf("committable mirror refused: %v", err)
		}
		if _, found, lookupErr := commitFixture.store.LookupMutationIdempotency(context.Background(), commitKey); lookupErr != nil || !found {
			t.Fatalf("the committed transaction lost its idempotency receipt: found=%t err=%v", found, lookupErr)
		}
	})
}

func TestWorkContextResearchAdmission(t *testing.T) {
	t.Parallel()

	t.Run("the current action declares and retains the research selection field", func(t *testing.T) {
		registered, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
		if !ok {
			t.Fatal("current workflow.break_fix is not registered")
		}
		fixture := seedWorkContextFixtureWithDefinition(t, "ctx-research-public-current", registered)
		defer fixture.store.Close()
		pack := seedResearchContextPack(t, fixture, "public")
		bindResearchPack(t, fixture, pack.PackID, 1, true)
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		err := fixture.recordWorkContext(t, map[string]any{
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		})
		if err != nil {
			t.Fatalf("current research selection declaration refused: %v", err)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore+1 {
			t.Fatalf("research declaration moved the version to %d, want %d", got, versionBefore+1)
		}
		view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		if err != nil || view == nil || len(view.ResearchFindings) != 1 || view.ResearchFindings[0].State != WorkContextResearchStateResolved {
			t.Fatalf("current research selection was not retained as resolved: %+v err=%v", view, err)
		}
	})
}
