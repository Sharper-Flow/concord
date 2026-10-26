package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// UnmarshalJSON keeps the source union closed even on internal replay and
// worker-read paths that do not pass through the public authoring schema.
func (source *WorkContextReadingSource) UnmarshalJSON(raw []byte) error {
	type wire WorkContextReadingSource
	var decoded wire
	if err := decodeStrictKnowledgeJSON(raw, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	allowed := []string{"kind"}
	switch decoded.Kind {
	case WorkContextSourceRepositoryFile:
		allowed = append(allowed, "project_id", "path", "commit_oid")
	case WorkContextSourceKnowledge:
		allowed = append(allowed, "source_id", "law_id", "content_hash")
	case WorkContextSourceDomainShape:
		allowed = append(allowed, "source_id", "commit_oid", "content_hash", "selector")
		var selector map[string]json.RawMessage
		if err := json.Unmarshal(fields["selector"], &selector); err != nil || len(selector) != 1 {
			return fmt.Errorf("domain_shape requires exactly one selector field")
		}
		for field := range selector {
			if field != "path" && field != "domain_id" && field != "subject" {
				return fmt.Errorf("domain_shape selector forbids %s", field)
			}
		}
	default:
		return fmt.Errorf("unknown work-context reading source kind %q", decoded.Kind)
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return fmt.Errorf("reading source kind %q forbids %s", decoded.Kind, field)
		}
	}
	*source = WorkContextReadingSource(decoded)
	return nil
}

func validateDomainShapeReadingSource(source WorkContextReadingSource) error {
	if source.ProjectID != "" || source.Path != "" || source.LawID != "" ||
		len(source.SourceID) < 2 || len(source.SourceID) > workContextProjectIDMaxBytes ||
		!workContextCommitOID(source.CommitOID) || !workflowDigest(source.ContentHash) || source.Selector == nil {
		return workContextFailure(KindInvalidPayload, "work_context", "domain_shape requires only source_id, commit_oid, content_hash and an explicit selector", "pin the selected Product home, commit and exact selected JSON hash")
	}
	return validateDomainShapeSelector(*source.Selector)
}

// sampleDomainShapeReading uses the same selected-read core as the normal read.
// It samples SQL only; the dispatch owner proves Git after closing its snapshot.
func sampleDomainShapeReading(ctx context.Context, q queryer, product string, reading WorkContextReading) (dispatchReadingWitness, error) {
	var w dispatchReadingWitness
	if err := validateWorkContextReading(reading); err != nil {
		return w, err
	}
	home, err := resolveKnowledgeQueryHome(ctx, q, product, "", KnowledgeHome{}, workerDispatchReadingResolutionOp)
	if err != nil {
		return w, err
	}
	if home.HomeProjectID != reading.Source.SourceID {
		return w, newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "domain_shape source_id is not the work Product's designated home Project", false, "select the work Product's designated home")
	}
	sources, err := resolveKnowledgeQuerySources(ctx, q, product, workerDispatchReadingResolutionOp)
	if err != nil {
		return w, err
	}
	if !slices.ContainsFunc(sources, func(source KnowledgeHome) bool {
		return source.HomeProjectID == home.HomeProjectID && source.HomeLocatorID == home.HomeLocatorID
	}) {
		return w, newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "domain_shape home is outside the Product knowledge-source set", false, "restore the Product home registration")
	}
	selected, err := selectedDomainShapeTx(ctx, q, home, product, *reading.Source.Selector)
	if err != nil {
		return w, err
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return w, err
	}
	if selected.KnowledgeCommit != reading.Source.CommitOID || domainShapeBytesHash(raw) != reading.Source.ContentHash {
		return w, newFailure(KindProjectionConflict, workerDispatchReadingResolutionOp, "domain_shape selection does not match the pinned commit and exact JSON hash", false, "read and explicitly pin the current selection")
	}
	// A root grouping explicitly covers the Product. A child grouping cannot
	// conceal facts or mixed-root owners belonging to other affected Domains.
	var root string
	if err := q.QueryRowContext(ctx, `SELECT root_domain_id FROM domain_registries WHERE product_id=?`, product).Scan(&root); err != nil {
		return w, err
	}
	if reading.DomainID != root && (len(selected.DomainIDs) != 1 || selected.DomainIDs[0] != reading.DomainID) {
		return w, newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "domain_shape selection does not fit the declared Domain group", false, "select one owning Domain or explicitly declare the approved Product-wide group")
	}
	w = dispatchReadingWitness{arm: WorkContextSourceDomainShape, identity: home.HomeProjectID + "/domain_shape@" + reading.Source.CommitOID + "/" + reading.Source.ContentHash,
		sourceProject: home.HomeProjectID, sourceLocator: home.HomeLocatorID, sourceRepo: home.RepoPath,
		commitOID: selected.KnowledgeCommit, contentHash: reading.Source.ContentHash, sourceSetDigest: knowledgeSourceSetDigest(sources), shape: &selected}
	if err := q.QueryRowContext(ctx, `SELECT created_at,updated_at FROM project_locators WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&w.locatorCreatedAt, &w.locatorUpdatedAt); err != nil {
		return w, err
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, home.HomeProjectID).Scan(&w.projectVersion); err != nil {
		return w, err
	}
	if err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid,scanned_content_digest,projection_version,complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&w.watermarkCommit, &w.watermarkDigest, &w.watermarkVersion, &w.watermarkComplete); err != nil {
		return w, err
	}
	return w, nil
}

func domainShapeReadingProofFailure() error {
	return newFailure(KindInvalidNoteProof, workerDispatchReadingResolutionOp, "domain_shape projected content differs from its pinned committed inputs", false, "rebuild the Product knowledge projection and explicitly refresh the selection")
}

// resolveDomainShapeReading proves the selected response, not a second lookup.
// The canonical shard/architecture owners decode immutable inputs. The existing
// pure selection owner must reproduce the sampled selection from those inputs.
// Neither the checkout nor a moving default reference supplies any bytes.
func resolveDomainShapeReading(ctx context.Context, runner GitRunner, w *dispatchReadingWitness) ([]byte, error) {
	if w.shape == nil {
		return nil, domainShapeReadingProofFailure()
	}
	s := w.shape
	kind, err := runner.Run(ctx, w.sourceRepo, "cat-file", "-t", w.commitOID)
	if err != nil || strings.TrimSpace(string(kind)) != "commit" {
		return nil, domainShapeReadingProofFailure()
	}
	manifest, shards, missing, err := readKnowledgeManifestShardsRunner(ctx, runner, w.sourceRepo, w.commitOID, manifestSharedHomeRole)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, domainShapeReadingProofFailure()
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		return nil, err
	}
	projection := newDomainShapeProjection(w.commitOID, manifest, architecture)
	snapshot := &projection.Snapshot
	if err := proveDomainShapeReadingTooling(ctx, runner, w.sourceRepo, s, snapshot); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(shards.facts))
	for name := range shards.facts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		raw := shards.facts[name]
		var fact DomainFact
		if err := decodeStrictKnowledgeJSON(raw, &fact); err != nil || name != fact.ID+".json" {
			return nil, domainShapeReadingProofFailure()
		}
		snapshot.Facts = append(snapshot.Facts, fact)
		snapshot.FactContentHashes[fact.ID] = domainShapeBytesHash(raw)
	}
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil || domainShapeBytesHash(rawSnapshot) != s.SnapshotContentHash {
		return nil, domainShapeReadingProofFailure()
	}
	snapshot.ContentHash = s.SnapshotContentHash
	home := KnowledgeHome{HomeProjectID: w.sourceProject, HomeLocatorID: w.sourceLocator}
	proved := selectDomainShape(*snapshot, home, s.ProductID, s.Selector)
	proved.GoverningLaw = s.GoverningLaw
	for _, law := range s.GoverningLaw {
		index := slices.IndexFunc(manifest.Records, func(r KnowledgeRecord) bool { return r.ID == law.LawID })
		if index < 0 {
			return nil, domainShapeReadingProofFailure()
		}
		r := manifest.Records[index]
		if r.Kind != law.Kind || r.Title != law.Title || r.Path != law.Path || r.SHA256 != law.ContentHash || r.Status != "accepted" || law.ScannedCommit != w.commitOID {
			return nil, domainShapeReadingProofFailure()
		}
		if _, err := verifyManifestRecordRunner(ctx, runner, w.sourceRepo, w.commitOID, r, manifestSharedHomeRole); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(proved)
	expected, marshalErr := json.Marshal(s)
	if err != nil || marshalErr != nil || len(raw) > DomainShapeResultMaxBytes || !bytes.Equal(raw, expected) || domainShapeBytesHash(raw) != w.contentHash {
		return nil, domainShapeReadingProofFailure()
	}
	return raw, nil
}

func proveDomainShapeReadingTooling(ctx context.Context, runner GitRunner, repo string, selected *DomainShapeResult, snapshot *DomainShapeSnapshot) error {
	snapshot.ToolingCommit, snapshot.ToolingContentHash = selected.ToolingCommit, selected.ToolingContentHash
	if selected.ToolingContentHash != "" {
		raw, err := readDomainShapePinnedBlob(ctx, runner, repo, selected.ToolingCommit, projectToolingManifestPath)
		if err != nil {
			return err
		}
		if domainShapeBytesHash(raw) != selected.ToolingContentHash {
			return domainShapeReadingProofFailure()
		}
		tooling, err := ParseProjectToolingManifest(raw)
		if err != nil {
			return err
		}
		if tooling.DomainShape != nil {
			snapshot.RequiredChecks = append(snapshot.RequiredChecks, tooling.DomainShape.RequiredChecks...)
			slices.Sort(snapshot.RequiredChecks)
		}
	} else if selected.ToolingCommit != "" {
		listing, err := runner.Run(ctx, repo, "ls-tree", "-z", selected.ToolingCommit, "--", projectToolingManifestPath)
		if err != nil || len(listing) != 0 {
			return domainShapeReadingProofFailure()
		}
	}
	return nil
}

func readDomainShapePinnedBlob(ctx context.Context, runner GitRunner, repo, commit, path string) ([]byte, error) {
	w := dispatchReadingWitness{repoPath: repo, commitOID: commit, path: path}
	if err := resolveRepositoryReading(ctx, runner, &w); err != nil {
		return nil, err
	}
	return runner.Run(ctx, repo, "cat-file", "blob", commit+":"+path)
}
