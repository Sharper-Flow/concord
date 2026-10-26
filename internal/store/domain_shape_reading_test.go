package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func shapeReading(t *testing.T, s *Store, product string, selector DomainShapeSelector) (WorkContextReading, []byte) {
	t.Helper()
	result, err := s.QueryDomainShape(context.Background(), product, selector)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return WorkContextReading{DomainID: "fixture-law", Reason: "Read the selected owning facts and governing law", Source: WorkContextReadingSource{
		Kind: WorkContextSourceDomainShape, SourceID: result.HomeProjectID, CommitOID: result.KnowledgeCommit, ContentHash: domainShapeBytesHash(raw), Selector: &selector,
	}}, raw
}

func TestDomainShapeReadingClosedSource(t *testing.T) {
	valid := `{"kind":"domain_shape","source_id":"project-a","commit_oid":"` + strings.Repeat("a", 40) + `","content_hash":"sha256:` + strings.Repeat("b", 64) + `","selector":{"path":"src"}}`
	entry := func(source string) string {
		return `{"domain_id":"fixture-law","reason":"bounded selected reading","source":` + source + `}`
	}
	var reading WorkContextReading
	if err := json.Unmarshal([]byte(entry(valid)), &reading); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkContextReading(reading); err != nil {
		t.Fatal(err)
	}
	// The closed union belongs to reading entries; oracle recipe sources share
	// the source type and must keep decoding their retained producer facts.
	var oracle NativeOracleResult
	if err := json.Unmarshal([]byte(`{"recipe_source":{"kind":""}}`), &oracle); err != nil {
		t.Fatalf("reading closure leaked into oracle recipe_source: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"selector":{"path":"src"}`, `"selector":null`, 1),
		strings.Replace(valid, `"selector":{"path":"src"}`, `"selector":{}`, 1),
		strings.Replace(valid, `"path":"src"`, `"path":"src","subject":"other"`, 1),
		strings.Replace(valid, `"path":"src"`, `"path":"../src"`, 1),
		strings.Replace(valid, `"path":"src"`, `"unknown":"src"`, 1),
		strings.Replace(valid, `"path":"src"`, `"Path":"src"`, 1),
		strings.Replace(valid, `"path":"src"`, `"path":"src","subject":""`, 1),
		strings.Replace(valid, `"kind":"domain_shape"`, `"kind":"domain_shape","law_id":""`, 1),
		strings.Replace(valid, `"kind":"domain_shape"`, `"kind":"domain_shape","project_id":null`, 1),
		strings.Replace(valid, `"kind":"domain_shape"`, `"kind":"domain_shape","kind":"knowledge"`, 1),
		strings.Replace(valid, `"kind":"domain_shape"`, `"kind":"repository_file"`, 1),
		strings.Replace(valid, `"kind":"domain_shape"`, `"kind":"knowledge"`, 1),
	} {
		var decoded WorkContextReading
		err := json.Unmarshal([]byte(entry(raw)), &decoded)
		if err == nil {
			err = validateWorkContextReading(decoded)
		}
		if err == nil {
			t.Fatalf("open source admitted: %s", raw)
		}
	}
}

// Reuse the real workflow/claim fixture and both E/F Product homes. Only the
// fixture's model association changes; the action, dispatch and read are real.
func shapeReadingWorkflowFixture(t *testing.T, suffix string) (workContextFixture, KnowledgeHome, string, KnowledgeHome, string) {
	t.Helper()
	f := seedWorkContextFixture(t, "shape-reading-"+suffix)
	a, productA := domainShapeProjectionFixture(t, f.store, "a")
	b, productB := domainShapeProjectionFixture(t, f.store, "b")
	home, product, other, foreign := a, productA, b, productB
	if suffix == "b" {
		home, product, other, foreign = b, productB, a, productA
	}
	if err := f.store.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RebuildKnowledgeIndex(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `DELETE FROM product_projects WHERE project_id='project'`)
		mustTxExec(t, tx, `INSERT INTO product_projects(product_id,project_id,role) VALUES(?,'project','secondary')`, product)
		mustTxExec(t, tx, `DELETE FROM workflow_contract_affected_domains WHERE work_id=?`, f.workID)
		mustTxExec(t, tx, `DELETE FROM workflow_architecture_bindings WHERE work_id=?`, f.workID)
		mustTxExec(t, tx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT ?,1,product_id,content_hash,'fixture-law',content_hash FROM domain_registries WHERE product_id=?`, f.workID, product)
		mustTxExec(t, tx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,'fixture-law')`, f.workID)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reviewSubjectClaim(t, f.store, f.workID)
	return f, home, product, other, foreign
}

func declareShapeReading(t *testing.T, f workContextFixture, reading WorkContextReading) error {
	t.Helper()
	return f.recordWorkContext(t, map[string]any{"required_reading": []WorkContextReading{reading}, "finding_refs": []string{}, "context_findings": []WorkerContextFinding{}})
}

func dispatchShapeReading(t *testing.T, f workContextFixture) WorkerReadingRequest {
	t.Helper()
	job := reviewSubjectReadyRevision(t, f)
	attempt := "attempt-" + f.workID
	packet := workContextDispatchPacket(t, f, attempt, job, currentFixtureView(t, f))
	if err := freshnessDispatch(t, f, attempt, packet); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	window := reviewSubjectWindow(t, f.store, f.workID, attempt)
	return WorkerReadingRequest{WorkID: f.workID, AttemptID: attempt, WorktreeIdentity: window.WorktreeIdentity, Packet: raw, Length: WorkerReadingPageMaxBytes}
}

func TestDomainShapeReadingDelegatedProductIsolation(t *testing.T) {
	for _, suffix := range []string{"a", "b"} {
		t.Run(suffix, func(t *testing.T) {
			f, home, product, other, foreign := shapeReadingWorkflowFixture(t, suffix)
			defer f.store.Close()
			reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Path: "src/entry.go"})
			var selected DomainShapeResult
			if err := json.Unmarshal(expected, &selected); err != nil {
				t.Fatal(err)
			}
			law := "CD-0001"
			if suffix == "b" {
				law = "CD-0002"
			}
			if selected.ProductID != product || len(selected.Facts) != 4 || len(selected.GoverningLaw) != 1 || selected.GoverningLaw[0].LawID != law {
				t.Fatalf("wrong Product facts/law: %+v", selected)
			}
			for _, fact := range selected.Facts {
				if !strings.HasSuffix(fact.Fact.ID, "-"+suffix) || len(fact.Fact.GoverningLawIDs) != 1 || fact.Fact.GoverningLawIDs[0] != law {
					t.Fatalf("foreign fact or law: %+v", fact)
				}
			}
			if err := declareShapeReading(t, f, reading); err != nil {
				t.Fatal(err)
			}
			view := currentFixtureView(t, f)
			if len(view.RequiredReading) != 1 || len(view.DomainGroups) != 1 || view.DomainGroups[0].DomainID != "fixture-law" || len(view.DomainGroups[0].RequiredReadingOrdinals) != 1 {
				t.Fatalf("reading not grouped: %+v", view)
			}
			request := dispatchShapeReading(t, f)
			request.Length = 511
			// A conflicting mutable checkout, even with valid foreign bytes,
			// cannot replace this Product's committed selected content.
			writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+"invariant-"+suffix+".json", runKnowledgeGit(t, other.RepoPath, "show", "HEAD:"+knowledgeFactTreeDir+"invariant-"+strings.TrimPrefix(foreign, "product-")+".json"))
			var actual []byte
			for {
				page, err := f.store.ReadWorkerReading(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if page.ContentHash != reading.Source.ContentHash || page.TotalBytes != len(expected) || !strings.Contains(page.SourceIdentity, home.HomeProjectID) {
					t.Fatalf("wrong pinned identity: %+v", page)
				}
				actual = append(actual, page.Data...)
				if page.EOF {
					break
				}
				request.Offset = page.NextOffset
			}
			if !bytes.Equal(actual, expected) || bytes.Contains(actual, []byte(`"product_id":"`+foreign+`"`)) {
				t.Fatalf("normal and delegated selected bytes differ: %s", actual)
			}
			for _, target := range []string{"archive", projectToolingManifestPath, selected.GoverningLaw[0].Path} {
				request.Offset = 0
				request.Runner = &shapeTamperRunner{GitRunner: CommittedReadGitRunner{}, target: target}
				page, err := f.store.ReadWorkerReading(context.Background(), request)
				if err == nil || len(page.Data) != 0 {
					t.Fatalf("worker exposed tampered %s bytes: %+v %v", target, page, err)
				}
			}
			t.Logf("%s normal -> declaration -> dispatch -> worker_read: %d identical selected bytes", product, len(actual))
		})
	}
}

func TestDomainShapeReadingHeldDispatchSurvivesProjectionRefresh(t *testing.T) {
	for _, suffix := range []string{"a", "b"} {
		t.Run(suffix, func(t *testing.T) {
			f, home, product, other, foreign := shapeReadingWorkflowFixture(t, suffix)
			defer f.store.Close()
			ctx := context.Background()
			reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Path: "src/entry.go"})
			if err := declareShapeReading(t, f, reading); err != nil {
				t.Fatal(err)
			}
			request := dispatchShapeReading(t, f)
			request.Length = 311
			writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Unrelated committed Product change.\n")
			commitKnowledgeRepo(t, home.RepoPath, "unrelated Product change")
			if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			current, err := f.store.QueryDomainShape(ctx, product, *reading.Source.Selector)
			if err != nil || current.KnowledgeCommit == reading.Source.CommitOID {
				t.Fatalf("projection did not advance: %+v %v", current, err)
			}
			// The new projection cannot admit the stale pin for a new dispatch.
			if err := f.store.readSnapshot(ctx, func(q queryer) error {
				_, err := sampleDispatchReadingIdentities(ctx, q, f.workID, []WorkContextReading{reading})
				return err
			}); !hasFailureKind(err, KindProjectionConflict) {
				t.Fatalf("dispatch admitted stale selection: %v", err)
			}
			foreignSelection, err := f.store.QueryDomainShape(ctx, foreign, DomainShapeSelector{Path: "src/entry.go"})
			if err != nil || foreignSelection.HomeProjectID != other.HomeProjectID {
				t.Fatalf("foreign home changed: %+v %v", foreignSelection, err)
			}
			var actual []byte
			for {
				page, err := f.store.ReadWorkerReading(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				if page.ContentHash != reading.Source.ContentHash || page.TotalBytes != len(expected) {
					t.Fatalf("held source repinned: %+v", page)
				}
				actual = append(actual, page.Data...)
				if page.EOF {
					break
				}
				request.Offset = page.NextOffset
			}
			if !bytes.Equal(actual, expected) {
				t.Fatal("held reading differs from original selected bytes")
			}
			t.Logf("%s: %d exact original bytes after committed home advance and index refresh", product, len(actual))
		})
	}
}

func TestDomainShapeReadingReconstructsGoverningLawMembership(t *testing.T) {
	f, home, product, _, _ := shapeReadingWorkflowFixture(t, "a")
	defer f.store.Close()
	ctx := context.Background()
	fixtures := []manifestFixture{}
	for _, id := range []string{"CD-0001", "CD-0003", "CD-0004", "CD-0005", "CD-0006"} {
		fixtures = append(fixtures, manifestFixture{ID: id, Kind: "decision", Path: ".concord/docs/decisions/" + id + "-synthetic.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic law " + id, Summary: "Synthetic owner invariant", RootHomed: id == "CD-0003", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "domain-b"}}})
	}
	writeManifestFixture(t, home.RepoPath, fixtures...)
	manifest := composeWorkingTreeManifest(t, home.RepoPath)
	for i := range manifest.Records {
		r := &manifest.Records[i]
		if r.ID == "CD-0004" || r.ID == "CD-0005" || r.ID == "CD-0006" {
			r.HomeDomainID = "domain-b"
		}
		if r.ID == "CD-0004" {
			r.AppliesToDomainIDs = []string{"fixture-law"}
		}
	}
	writeManifestShards(t, home.RepoPath, manifest)
	fact, _ := domainFactFixture(DomainFactInvariant)
	fact.ID, fact.OwnerDomainID = "invariant-a", "fixture-law"
	fact.Standing, fact.GoverningLawIDs = "law", []string{"CD-0001", "CD-0005"}
	fact.Invariant.AppliesToDomainIDs = []string{"fixture-law", "domain-b"}
	writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact)))
	commitKnowledgeRepo(t, home.RepoPath, "synthetic inherited applicable and explicitly cited law")
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
	var selected DomainShapeResult
	if err := json.Unmarshal(expected, &selected); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, law := range selected.GoverningLaw {
		ids = append(ids, law.LawID)
	}
	if strings.Join(ids, ",") != "CD-0001,CD-0003,CD-0004,CD-0005" {
		t.Fatalf("SQL law selection differs: %v", ids)
	}
	if err := declareShapeReading(t, f, reading); err != nil {
		t.Fatal(err)
	}
	request := dispatchShapeReading(t, f)
	writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Later Product revision.\n")
	commitKnowledgeRepo(t, home.RepoPath, "advance synthetic law selection projection")
	if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	page, err := f.store.ReadWorkerReading(ctx, request)
	if err != nil || !page.EOF || !bytes.Equal(page.Data, expected) {
		t.Fatalf("immutable law membership/metadata differ from SQL selected bytes: %+v %v", page, err)
	}
}

func TestDomainShapeReadingRefreshDuringReadKeepsOriginalBytes(t *testing.T) {
	f, home, product, _, _ := shapeReadingWorkflowFixture(t, "a")
	defer f.store.Close()
	ctx := context.Background()
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
	if err := declareShapeReading(t, f, reading); err != nil {
		t.Fatal(err)
	}
	request := dispatchShapeReading(t, f)
	request.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Advance during immutable read.\n")
		commitKnowledgeRepo(t, home.RepoPath, "advance while the worker holds an original reading")
		if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
			t.Fatal(err)
		}
	}}
	page, err := f.store.ReadWorkerReading(ctx, request)
	if err != nil || !page.EOF || !bytes.Equal(page.Data, expected) {
		t.Fatalf("projection-only race replaced or refused held bytes: %+v %v", page, err)
	}
}

func TestDomainShapeReadingRejectsProjectedLawForgery(t *testing.T) {
	for _, mutation := range []string{"metadata", "membership"} {
		t.Run(mutation, func(t *testing.T) {
			f, home, product, _, _ := shapeReadingWorkflowFixture(t, "a")
			defer f.store.Close()
			ctx := context.Background()
			if err := f.store.Transact(ctx, func(tx *Transaction) error {
				mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
				if mutation == "metadata" {
					mustTxExec(t, tx, `UPDATE law_subjects SET title='Forged law title' WHERE home_project_id=? AND law_id='CD-0001'`, home.HomeProjectID)
				} else {
					mustTxExec(t, tx, `DELETE FROM law_domain_homes WHERE home_project_id=? AND law_id='CD-0001'`, home.HomeProjectID)
				}
				mustTxExec(t, tx, `DELETE FROM write_guard`)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			forged, _ := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
			var witnesses []dispatchReadingWitness
			if err := f.store.readSnapshot(ctx, func(q queryer) error {
				var err error
				witnesses, err = sampleDispatchReadingIdentities(ctx, q, f.workID, []WorkContextReading{forged})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := resolveDispatchReadingWitnesses(ctx, CommittedReadGitRunner{}, witnesses); !hasFailureKind(err, KindInvalidNoteProof) {
				t.Fatalf("forged projected law %s acquired committed proof: %v", mutation, err)
			}
		})
	}
}

func TestDomainShapeReadingPinnedProofAndRefusals(t *testing.T) {
	f, home, product, _, foreign := shapeReadingWorkflowFixture(t, "a")
	defer f.store.Close()
	ctx := context.Background()
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
	sample := func(r WorkContextReading) ([]dispatchReadingWitness, error) {
		var witnesses []dispatchReadingWitness
		err := f.store.readSnapshot(ctx, func(q queryer) error {
			var err error
			witnesses, err = sampleDispatchReadingIdentities(ctx, q, f.workID, []WorkContextReading{r})
			return err
		})
		return witnesses, err
	}
	witnesses, err := sample(reading)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveDispatchReadingWitnesses(ctx, CommittedReadGitRunner{}, witnesses); err != nil {
		t.Fatal(err)
	}
	actual, err := readDispatchReadingContent(ctx, CommittedReadGitRunner{}, witnesses[0])
	if err != nil || !bytes.Equal(expected, actual) {
		t.Fatalf("selected proof differs: %v", err)
	}
	foreignReading, _ := shapeReading(t, f.store, foreign, DomainShapeSelector{Subject: "invariant-b"})
	for name, change := range map[string]func(*WorkContextReading){
		"foreign home":           func(r *WorkContextReading) { *r = foreignReading },
		"stale commit":           func(r *WorkContextReading) { r.Source.CommitOID = strings.Repeat("1", 40) },
		"wrong hash":             func(r *WorkContextReading) { r.Source.ContentHash = "sha256:" + strings.Repeat("1", 64) },
		"changed selector":       func(r *WorkContextReading) { r.Source.Selector = &DomainShapeSelector{Subject: "config_coupling-a"} },
		"outside affected group": func(r *WorkContextReading) { r.DomainID = "domain-b" },
		"mixed group hidden as child": func(r *WorkContextReading) {
			*r, _ = shapeReading(t, f.store, product, DomainShapeSelector{Path: "legacy"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := reading
			change(&r)
			if _, err := sample(r); err == nil {
				t.Fatal("invalid source admitted")
			}
			version := readWorkVersion(t, f.store, f.workID)
			if err := declareShapeReading(t, f, r); err == nil {
				t.Fatal("record_work_context admitted invalid selected source")
			}
			if after := readWorkVersion(t, f.store, f.workID); after != version {
				t.Fatal("refused declaration changed the work version")
			}
		})
	}
	for _, target := range []string{"archive", projectToolingManifestPath, ".concord/docs/decisions/CD-0001-synthetic.md"} {
		t.Run("tampered "+target, func(t *testing.T) {
			runner := &shapeTamperRunner{GitRunner: CommittedReadGitRunner{}, target: target}
			raw, err := readDispatchReadingContent(ctx, runner, witnesses[0])
			if err == nil || len(raw) != 0 || !runner.changed {
				t.Fatalf("tampered committed input exposed selected bytes: changed=%v error=%v", runner.changed, err)
			}
		})
	}
	// Alter projected fact bytes while preserving the recorded source hash.
	// The normal selected hash changes; even a caller who repins it cannot
	// borrow the original committed proof.
	if err := f.store.Transact(ctx, func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `UPDATE knowledge_domain_facts SET fact_json=json_set(fact_json,'$.invariant.statement','Forged projected statement.') WHERE home_project_id=? AND fact_id='invariant-a'`, home.HomeProjectID)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sample(reading); !hasFailureKind(err, KindProjectionConflict) {
		t.Fatalf("projected byte tampering did not refuse the original hash: %v", err)
	}
	forged, _ := shapeReading(t, f.store, product, *reading.Source.Selector)
	forgedWitnesses, err := sample(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveDispatchReadingWitnesses(ctx, CommittedReadGitRunner{}, forgedWitnesses); !hasFailureKind(err, KindInvalidNoteProof) {
		t.Fatalf("repinned forged projection acquired committed proof: %v", err)
	}
}

type shapeTamperRunner struct {
	GitRunner
	target  string
	changed bool
}

func (r *shapeTamperRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	raw, err := r.GitRunner.Run(ctx, repo, args...)
	if err == nil && (r.target == "archive" && len(args) > 0 && args[0] == "archive" || len(args) == 3 && args[0] == "cat-file" && args[1] == "blob" && strings.HasSuffix(args[2], ":"+r.target)) {
		r.changed = true
		return []byte("tampered committed bytes"), nil
	}
	return raw, err
}

func TestDomainShapeReadingDispatchAndWorkerRaceRefusals(t *testing.T) {
	for _, drift := range []string{"locator", "source set", "watermark", "projection", "Product version", "home designation"} {
		t.Run(drift, func(t *testing.T) {
			f, home, product, _, _ := shapeReadingWorkflowFixture(t, "a")
			defer f.store.Close()
			ctx := context.Background()
			reading, _ := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
			if err := declareShapeReading(t, f, reading); err != nil {
				t.Fatal(err)
			}
			job := reviewSubjectReadyRevision(t, f)
			view := currentFixtureView(t, f)
			preparation, err := PrepareWorkerDispatchFreshness(ctx, f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: dispatchSessionWorktree(t, f.store, f.workID)})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.Transact(ctx, func(tx *Transaction) error {
				mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
				switch drift {
				case "locator":
					mustTxExec(t, tx, `UPDATE project_locators SET updated_at='drifted' WHERE project_id=?`, home.HomeProjectID)
				case "source set":
					mustTxExec(t, tx, `INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) SELECT ?,'project','workflow-law-locator','drifted'`, product)
				case "watermark":
					mustTxExec(t, tx, `UPDATE knowledge_index_watermark SET scanned_content_digest='drifted' WHERE home_project_id=?`, home.HomeProjectID)
				case "projection":
					mustTxExec(t, tx, `UPDATE knowledge_domain_facts SET fact_json=json_set(fact_json,'$.invariant.statement','Drifted statement.') WHERE home_project_id=? AND fact_id='invariant-a'`, home.HomeProjectID)
				case "Product version":
					mustTxExec(t, tx, `UPDATE products SET version=version+1 WHERE id=?`, product)
				case "home designation":
					mustTxExec(t, tx, `DELETE FROM product_knowledge_homes WHERE product_id=?`, product)
				}
				mustTxExec(t, tx, `DELETE FROM write_guard`)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			attempt := "attempt-shape-drift"
			err = dispatchBindingAttemptCtx(t, f.store, cd0059DispatchSeed{workID: f.workID, ownerActor: f.owner}, attempt, workContextDispatchPacket(t, f, attempt, job, view), WithWorkerDispatchPreparation(ctx, preparation))
			if err == nil {
				t.Fatal("changed sampled source borrowed an earlier proof")
			}
			checkZeroDispatchEffects(t, f.store, f.workID, attempt)
		})
	}
	t.Run("worker refuses registration change after Git", func(t *testing.T) {
		f, home, product, _, _ := shapeReadingWorkflowFixture(t, "a")
		defer f.store.Close()
		reading, _ := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
		if err := declareShapeReading(t, f, reading); err != nil {
			t.Fatal(err)
		}
		request := dispatchShapeReading(t, f)
		request.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
			if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
				mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
				mustTxExec(t, tx, `UPDATE project_locators SET updated_at='drifted' WHERE project_id=?`, home.HomeProjectID)
				mustTxExec(t, tx, `DELETE FROM write_guard`)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}}
		page, err := f.store.ReadWorkerReading(context.Background(), request)
		if !hasFailureKind(err, KindStaleRequiresReview) || len(page.Data) != 0 {
			t.Fatalf("worker source race exposed content: %v %+v", err, page)
		}
	})
}

func TestDomainShapeReadingExplicitDomainGroup(t *testing.T) {
	f, _, product, _, _ := shapeReadingWorkflowFixture(t, "a")
	defer f.store.Close()
	ctx := context.Background()
	var root string
	if err := f.store.db.QueryRow(`SELECT root_domain_id FROM domain_registries WHERE product_id=?`, product).Scan(&root); err != nil {
		t.Fatal(err)
	}
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{DomainID: "fixture-law"})
	reading.DomainID = root
	reading.ProductWideRationale = "The selected Domain includes a mixed root with more than one owner."
	if err := declareShapeReading(t, f, reading); err == nil {
		t.Fatal("root grouping bypassed the approved affected Domain scope")
	}
	if err := f.store.Transact(ctx, func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, f.workID, root)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	withoutRationale := reading
	withoutRationale.ProductWideRationale = ""
	if err := declareShapeReading(t, f, withoutRationale); err == nil {
		t.Fatal("root grouping omitted its required rationale")
	}
	if err := declareShapeReading(t, f, reading); err != nil {
		t.Fatal(err)
	}
	page, err := f.store.ReadWorkerReading(ctx, dispatchShapeReading(t, f))
	if err != nil || !page.EOF || !bytes.Equal(page.Data, expected) || page.ContentHash != reading.Source.ContentHash {
		t.Fatalf("explicit Domain selection did not survive the approved root group: %+v %v", page, err)
	}
}

// The Domain Shape selector extends the shared source type; the oracle's
// knowledge and repository arms must still refuse it on replay and admission.
func TestOracleSourcesRefuseDomainShapeSelector(t *testing.T) {
	selector := &DomainShapeSelector{Path: "src"}
	law := WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "project-a", LawID: "law-a", ContentHash: "sha256:" + strings.Repeat("b", 64)}
	recipe := WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project-a", Path: "harness/run.sh", CommitOID: strings.Repeat("a", 40)}
	if err := validateWorkerJobLawSource("worker job", law); err != nil {
		t.Fatalf("valid law source refused: %v", err)
	}
	if err := validateOracleRecipeSource(recipe); err != nil {
		t.Fatalf("valid recipe source refused: %v", err)
	}
	law.Selector, recipe.Selector = selector, selector
	if err := validateWorkerJobLawSource("worker job", law); err == nil {
		t.Fatal("law binding admitted a domain_shape selector")
	}
	if err := validateOracleRecipeSource(recipe); err == nil {
		t.Fatal("recipe source admitted a domain_shape selector")
	}
}
