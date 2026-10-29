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

// Both Products have committed homes. Only the work's Product association
// changes; declarations and selected-content proofs use their normal owners.
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
	return f, home, product, other, foreign
}

func declareShapeReading(t *testing.T, f workContextFixture, reading WorkContextReading) error {
	t.Helper()
	return f.recordWorkContext(t, map[string]any{"required_reading": []WorkContextReading{reading}, "finding_refs": []string{}, "context_findings": []WorkContextFinding{}})
}

func sampleShapeReading(t *testing.T, f workContextFixture, product string, reading WorkContextReading) domainShapeReadingWitness {
	t.Helper()
	var witness domainShapeReadingWitness
	err := f.store.readSnapshot(t.Context(), func(q queryer) error {
		var err error
		witness, err = sampleDomainShapeReading(t.Context(), q, product, reading)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func TestDomainShapeReadingProductIsolation(t *testing.T) {
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
			if selected.ProductID != product || len(selected.Facts) != 4 || len(selected.GoverningLaw) != 1 || selected.GoverningLaw[0] != law {
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
			witness := sampleShapeReading(t, f, product, reading)
			// A conflicting mutable checkout, even with valid foreign bytes,
			// cannot replace this Product's committed selected content.
			writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+"invariant-"+suffix+".json", runKnowledgeGit(t, other.RepoPath, "show", "HEAD:"+knowledgeFactTreeDir+"invariant-"+strings.TrimPrefix(foreign, "product-")+".json"))
			actual, err := resolveDomainShapeReading(t.Context(), CommittedReadGitRunner{}, &witness)
			if err != nil {
				t.Fatal(err)
			}
			if witness.contentHash != reading.Source.ContentHash || !strings.Contains(witness.identity, home.HomeProjectID) {
				t.Fatalf("wrong pinned identity: %+v", witness)
			}
			if !bytes.Equal(actual, expected) || bytes.Contains(actual, []byte(`"product_id":"`+foreign+`"`)) {
				t.Fatalf("selected and committed proof bytes differ: %s", actual)
			}
			for _, target := range []string{"archive", projectToolingManifestPath, ".concord/docs/decisions/" + law + "-synthetic.md"} {
				raw, err := resolveDomainShapeReading(t.Context(), &shapeTamperRunner{GitRunner: CommittedReadGitRunner{}, target: target}, &witness)
				if err == nil || len(raw) != 0 {
					t.Fatalf("proof exposed tampered %s bytes: %s %v", target, raw, err)
				}
			}
			t.Logf("%s selection -> declaration -> committed proof: %d identical selected bytes", product, len(actual))
		})
	}
}

func TestDomainShapeReadingHeldWitnessSurvivesProjectionRefresh(t *testing.T) {
	for _, suffix := range []string{"a", "b"} {
		t.Run(suffix, func(t *testing.T) {
			f, home, product, other, foreign := shapeReadingWorkflowFixture(t, suffix)
			defer f.store.Close()
			ctx := context.Background()
			reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Path: "src/entry.go"})
			if err := declareShapeReading(t, f, reading); err != nil {
				t.Fatal(err)
			}
			witness := sampleShapeReading(t, f, product, reading)
			writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Unrelated committed Product change.\n")
			commitKnowledgeRepo(t, home.RepoPath, "unrelated Product change")
			if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			current, err := f.store.QueryDomainShape(ctx, product, *reading.Source.Selector)
			if err != nil || current.KnowledgeCommit == reading.Source.CommitOID {
				t.Fatalf("projection did not advance: %+v %v", current, err)
			}
			// The new projection cannot admit a newly sampled stale pin.
			if err := f.store.readSnapshot(ctx, func(q queryer) error {
				_, err := sampleDomainShapeReading(ctx, q, product, reading)
				return err
			}); !hasFailureKind(err, KindProjectionConflict) {
				t.Fatalf("sampling admitted stale selection: %v", err)
			}
			foreignSelection, err := f.store.QueryDomainShape(ctx, foreign, DomainShapeSelector{Path: "src/entry.go"})
			if err != nil || foreignSelection.HomeProjectID != other.HomeProjectID {
				t.Fatalf("foreign home changed: %+v %v", foreignSelection, err)
			}
			actual, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &witness)
			if err != nil {
				t.Fatal(err)
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
	for _, id := range []string{"CD-0001", "CD-0003", "CD-0004", "CD-0005", "CD-0006", "CD-0007", "CD-0008", "CD-0009"} {
		fixtures = append(fixtures, manifestFixture{ID: id, Kind: "decision", Path: ".concord/docs/decisions/" + id + "-synthetic.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic law " + id, Summary: "Synthetic owner invariant", RootHomed: id == "CD-0003" || id == "CD-0008" || id == "CD-0009", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "domain-b"}}})
	}
	writeManifestFixture(t, home.RepoPath, fixtures...)
	manifest := composeWorkingTreeManifest(t, home.RepoPath)
	for i := range manifest.Records {
		r := &manifest.Records[i]
		if r.ID == "CD-0004" || r.ID == "CD-0005" || r.ID == "CD-0006" || r.ID == "CD-0007" {
			r.HomeDomainID = "domain-b"
		}
		if r.ID == "CD-0004" || r.ID == "CD-0008" {
			r.AppliesToDomainIDs = []string{"fixture-law"}
		}
		if r.ID == "CD-0007" {
			r.AppliesToDomainIDs = []string{manifest.DomainRegistry.RootDomainID}
		}
	}
	writeManifestShards(t, home.RepoPath, manifest)
	fact, _ := domainFactFixture(DomainFactInvariant)
	fact.ID, fact.OwnerDomainID = "invariant-a", "fixture-law"
	fact.Standing, fact.GoverningLawIDs = "law", []string{"CD-0001", "CD-0005", "CD-0009"}
	fact.Invariant.AppliesToDomainIDs = []string{"fixture-law", "domain-b"}
	writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact)))
	commitKnowledgeRepo(t, home.RepoPath, "synthetic scoped applicable and explicitly cited law")
	wantIDs := "CD-0001,CD-0004,CD-0005,CD-0008,CD-0009"
	t.Run("manifest-membership", func(t *testing.T) {
		selection := DomainShapeResult{DomainIDs: []string{"fixture-law"}, Facts: []DomainShapeFact{{Fact: fact}}}
		if ids := domainShapeManifestGoverningLaw(manifest, selection); strings.Join(ids, ",") != wantIDs {
			t.Fatalf("manifest law selection differs: %v", ids)
		}
	})
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
	var selected DomainShapeResult
	if err := json.Unmarshal(expected, &selected); err != nil {
		t.Fatal(err)
	}
	if ids := selected.GoverningLaw; strings.Join(ids, ",") != wantIDs {
		t.Fatalf("SQL law selection differs: %v", ids)
	}
	if err := declareShapeReading(t, f, reading); err != nil {
		t.Fatal(err)
	}
	witness := sampleShapeReading(t, f, product, reading)
	writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Later Product revision.\n")
	commitKnowledgeRepo(t, home.RepoPath, "advance synthetic law selection projection")
	if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	raw, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &witness)
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatalf("immutable law membership/metadata differ from SQL selected bytes: %s %v", raw, err)
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
	witness := sampleShapeReading(t, f, product, reading)
	runner := &shapeReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "Advance during immutable read.\n")
		commitKnowledgeRepo(t, home.RepoPath, "advance while an original reading is held")
		if err := f.store.RebuildKnowledgeIndex(ctx, home); err != nil {
			t.Fatal(err)
		}
	}}
	raw, err := resolveDomainShapeReading(ctx, runner, &witness)
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatalf("projection-only race replaced or refused held bytes: %s %v", raw, err)
	}
}

// The result carries law IDs only, so a forged projected law hash or title
// never reaches the result bytes; a forged membership does and must refuse.
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
			var witness domainShapeReadingWitness
			if err := f.store.readSnapshot(ctx, func(q queryer) error {
				var err error
				witness, err = sampleDomainShapeReading(ctx, q, product, forged)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			raw, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &witness)
			if mutation == "metadata" {
				if err != nil || bytes.Contains(raw, []byte("Forged law title")) {
					t.Fatalf("nonprojected law metadata changed selected bytes: %s %v", raw, err)
				}
			} else if !hasFailureKind(err, KindInvalidNoteProof) {
				t.Fatalf("forged projected law membership acquired committed proof: %v", err)
			}
		})
	}
}

func TestDomainShapeReadingPinnedProofAndRefusals(t *testing.T) {
	f, home, product, _, foreign := shapeReadingWorkflowFixture(t, "a")
	defer f.store.Close()
	ctx := context.Background()
	reading, expected := shapeReading(t, f.store, product, DomainShapeSelector{Subject: "invariant-a"})
	sample := func(r WorkContextReading) (domainShapeReadingWitness, error) {
		var witness domainShapeReadingWitness
		err := f.store.readSnapshot(ctx, func(q queryer) error {
			var err error
			witness, err = sampleDomainShapeReading(ctx, q, product, r)
			return err
		})
		return witness, err
	}
	witness, err := sample(reading)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &witness)
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
			raw, err := resolveDomainShapeReading(ctx, runner, &witness)
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
	forgedWitness, err := sample(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &forgedWitness); !hasFailureKind(err, KindInvalidNoteProof) {
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

type shapeReadingProbeRunner struct {
	GitRunner
	once func()
}

func (r *shapeReadingProbeRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if r.once != nil {
		callback := r.once
		r.once = nil
		callback()
	}
	return r.GitRunner.Run(ctx, repo, args...)
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
	witness := sampleShapeReading(t, f, product, reading)
	raw, err := resolveDomainShapeReading(ctx, CommittedReadGitRunner{}, &witness)
	if err != nil || !bytes.Equal(raw, expected) || witness.contentHash != reading.Source.ContentHash {
		t.Fatalf("explicit Domain selection did not survive the approved root group: %s %v", raw, err)
	}
}

// The Domain Shape selector extends the shared source type; the oracle's
// knowledge and repository arms must still refuse it on replay and admission.
func TestOracleSourcesRefuseDomainShapeSelector(t *testing.T) {
	selector := &DomainShapeSelector{Path: "src"}
	law := WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "project-a", LawID: "law-a", ContentHash: "sha256:" + strings.Repeat("b", 64)}
	recipe := WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project-a", Path: "harness/run.sh", CommitOID: strings.Repeat("a", 40)}
	if err := validateOracleLawSource(law); err != nil {
		t.Fatalf("valid law source refused: %v", err)
	}
	if err := validateOracleRecipeSource(recipe); err != nil {
		t.Fatalf("valid recipe source refused: %v", err)
	}
	law.Selector, recipe.Selector = selector, selector
	if err := validateOracleLawSource(law); err == nil {
		t.Fatal("law binding admitted a domain_shape selector")
	}
	if err := validateOracleRecipeSource(recipe); err == nil {
		t.Fatal("recipe source admitted a domain_shape selector")
	}
}
