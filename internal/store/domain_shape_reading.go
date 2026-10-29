package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// UnmarshalJSON keeps a required reading's source union closed even on internal
// replay and worker-read paths that do not pass through the public authoring
// schema. The closure belongs to the reading entry: oracle law and recipe
// sources share the source type and keep their own closed-arm validators.
func (reading *WorkContextReading) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		DomainID             string          `json:"domain_id"`
		Reason               string          `json:"reason"`
		ProductWideRationale string          `json:"product_wide_rationale,omitempty"`
		Source               json.RawMessage `json:"source"`
	}
	if err := decodeStrictKnowledgeJSON(raw, &decoded); err != nil {
		return err
	}
	source, err := decodeClosedReadingSource(decoded.Source)
	if err != nil {
		return err
	}
	*reading = WorkContextReading{DomainID: decoded.DomainID, Reason: decoded.Reason, ProductWideRationale: decoded.ProductWideRationale, Source: source}
	return nil
}

func decodeClosedReadingSource(raw []byte) (WorkContextReadingSource, error) {
	var decoded WorkContextReadingSource
	if err := decodeStrictKnowledgeJSON(raw, &decoded); err != nil {
		return decoded, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return decoded, err
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
			return decoded, fmt.Errorf("domain_shape requires exactly one selector field")
		}
		for field := range selector {
			if field != "path" && field != "domain_id" && field != "subject" {
				return decoded, fmt.Errorf("domain_shape selector forbids %s", field)
			}
		}
	default:
		return decoded, fmt.Errorf("unknown work-context reading source kind %q", decoded.Kind)
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return decoded, fmt.Errorf("reading source kind %q forbids %s", decoded.Kind, field)
		}
	}
	return decoded, nil
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
		commitOID: selected.KnowledgeCommit, contentHash: reading.Source.ContentHash, sourceSetDigest: knowledgeSourceSetDigest(sources), shape: &selected, shapeDomain: reading.DomainID}
	if err := sampleDomainShapeSourceAuthority(ctx, q, home, product, &w); err != nil {
		return w, err
	}
	if err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid,scanned_content_digest,projection_version,complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&w.watermarkCommit, &w.watermarkDigest, &w.watermarkVersion, &w.watermarkComplete); err != nil {
		return w, err
	}
	return w, nil
}

// A held packet already pins the selection. Sampling rechecks source authority,
// not a projection that may now describe a later committed Product revision.
func sampleHeldDomainShapeReading(ctx context.Context, q queryer, product string, reading WorkContextReading) (dispatchReadingWitness, error) {
	var w dispatchReadingWitness
	if err := validateWorkContextReading(reading); err != nil {
		return w, err
	}
	home, err := resolveKnowledgeQueryHome(ctx, q, product, "", KnowledgeHome{}, workerDispatchReadingResolutionOp)
	if err != nil {
		return w, err
	}
	sources, err := resolveKnowledgeQuerySources(ctx, q, product, workerDispatchReadingResolutionOp)
	if err != nil {
		return w, err
	}
	if home.HomeProjectID != reading.Source.SourceID || !slices.ContainsFunc(sources, func(source KnowledgeHome) bool {
		return source.HomeProjectID == home.HomeProjectID && source.HomeLocatorID == home.HomeLocatorID
	}) {
		return w, newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "domain_shape source is not the Product's registered designated home", false, "restore the original Product home registration")
	}
	w = dispatchReadingWitness{arm: WorkContextSourceDomainShape, identity: home.HomeProjectID + "/domain_shape@" + reading.Source.CommitOID + "/" + reading.Source.ContentHash,
		sourceProject: home.HomeProjectID, sourceLocator: home.HomeLocatorID, sourceRepo: home.RepoPath,
		commitOID: reading.Source.CommitOID, contentHash: reading.Source.ContentHash, sourceSetDigest: knowledgeSourceSetDigest(sources), shapeDomain: reading.DomainID,
		shape: &DomainShapeResult{ProductID: product, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, Selector: *reading.Source.Selector}}
	err = sampleDomainShapeSourceAuthority(ctx, q, home, product, &w)
	return w, err
}

func sampleDomainShapeSourceAuthority(ctx context.Context, q queryer, home KnowledgeHome, product string, w *dispatchReadingWitness) error {
	if err := q.QueryRowContext(ctx, `SELECT created_at,updated_at FROM project_locators WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&w.locatorCreatedAt, &w.locatorUpdatedAt); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, home.HomeProjectID).Scan(&w.projectVersion); err != nil {
		return err
	}
	var member bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, product, home.HomeProjectID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "domain_shape home Project is outside the work Product", false, "restore the Product's home Project membership")
	}
	return nil
}

func domainShapeReadingProofFailure() error {
	return newFailure(KindInvalidNoteProof, workerDispatchReadingResolutionOp, "domain_shape selected content differs from its pinned committed inputs", false, "restore the original local committed inputs; never repin an authorized packet")
}

// resolveDomainShapeReading proves the selected response, not a second lookup.
// The canonical shard/architecture owners decode immutable inputs. The existing
// pure selection owner must reproduce the sampled selection from those inputs.
// Neither the checkout nor a moving default reference supplies any bytes.
func resolveDomainShapeReading(ctx context.Context, runner GitRunner, w *dispatchReadingWitness) ([]byte, error) {
	if w.shape == nil {
		return nil, domainShapeReadingProofFailure()
	}
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
	if err := reconstructDomainShapeReadingSnapshot(ctx, runner, w, shards, snapshot); err != nil {
		return nil, err
	}
	home := KnowledgeHome{HomeProjectID: w.sourceProject, HomeLocatorID: w.sourceLocator}
	proved := selectDomainShape(*snapshot, home, w.shape.ProductID, w.shape.Selector)
	proved.GoverningLaw = domainShapeManifestGoverningLaw(manifest, proved)
	if w.shapeDomain != manifest.DomainRegistry.RootDomainID && (len(proved.DomainIDs) != 1 || proved.DomainIDs[0] != w.shapeDomain) {
		return nil, domainShapeReadingProofFailure()
	}
	if err := proveDomainShapeGoverningLaw(ctx, runner, w, manifest, proved.GoverningLaw); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(proved)
	if err != nil || len(raw) > DomainShapeResultMaxBytes || domainShapeBytesHash(raw) != w.contentHash {
		return nil, domainShapeReadingProofFailure()
	}
	// Dispatch must prove the exact sampled SQL response as well as its hash.
	// Held reads reconstruct without borrowing any moving projection metadata.
	if w.shape.SnapshotContentHash != "" {
		expected, err := json.Marshal(w.shape)
		if err != nil || !bytes.Equal(raw, expected) {
			return nil, domainShapeReadingProofFailure()
		}
	}
	return raw, nil
}

func reconstructDomainShapeReadingSnapshot(ctx context.Context, runner GitRunner, w *dispatchReadingWitness, shards knowledgeShards, snapshot *DomainShapeSnapshot) error {
	if len(shards.facts) != 0 {
		if err := proveDomainShapeReadingTooling(ctx, runner, w.sourceRepo, snapshot); err != nil {
			return err
		}
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
			return domainShapeReadingProofFailure()
		}
		snapshot.Facts = append(snapshot.Facts, fact)
		snapshot.FactContentHashes[fact.ID] = domainShapeBytesHash(raw)
	}
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	snapshot.ContentHash = domainShapeBytesHash(rawSnapshot)
	return nil
}

func proveDomainShapeGoverningLaw(ctx context.Context, runner GitRunner, w *dispatchReadingWitness, manifest KnowledgeManifest, laws []DomainShapeLaw) error {
	for _, law := range laws {
		index := slices.IndexFunc(manifest.Records, func(r KnowledgeRecord) bool { return r.ID == law.LawID })
		if index < 0 {
			return domainShapeReadingProofFailure()
		}
		r := manifest.Records[index]
		if !manifestLawBearingKinds[r.Kind] || r.HomeDomainID == "" || r.SHA256 != law.ContentHash || r.Status != "accepted" {
			return domainShapeReadingProofFailure()
		}
		if _, err := verifyManifestRecordRunner(ctx, runner, w.sourceRepo, w.commitOID, r, manifestSharedHomeRole); err != nil {
			return err
		}
	}
	return nil
}

func proveDomainShapeReadingTooling(ctx context.Context, runner GitRunner, repo string, snapshot *DomainShapeSnapshot) error {
	snapshot.ToolingCommit = snapshot.KnowledgeCommit
	listing, err := runner.Run(ctx, repo, "ls-tree", "-z", snapshot.KnowledgeCommit, "--", projectToolingManifestPath)
	if err != nil {
		return err
	}
	if len(listing) == 0 {
		return nil
	}
	entries, err := parseTreeEntries(listing)
	if err != nil || len(entries) != 1 || entries[0].kind != "blob" || entries[0].mode != "100644" {
		return domainShapeReadingProofFailure()
	}
	raw, err := readDomainShapePinnedBlob(ctx, runner, repo, snapshot.KnowledgeCommit, projectToolingManifestPath)
	if err != nil {
		return err
	}
	tooling, err := ParseProjectToolingManifest(raw)
	if err != nil {
		return err
	}
	snapshot.ToolingContentHash = domainShapeBytesHash(raw)
	if tooling.DomainShape != nil {
		snapshot.RequiredChecks = append(snapshot.RequiredChecks, tooling.DomainShape.RequiredChecks...)
		slices.Sort(snapshot.RequiredChecks)
	}
	return nil
}

// This is the immutable equivalent of domainShapeGoverningLawTx's join: accepted
// law must have a home, and match a selected Domain by home or applicability,
// or an explicit fact citation. Metadata comes only from the pinned manifest.
func domainShapeManifestGoverningLaw(manifest KnowledgeManifest, selection DomainShapeResult) []DomainShapeLaw {
	explicit := map[string]bool{}
	for _, item := range selection.Facts {
		for _, law := range item.Fact.GoverningLawIDs {
			explicit[law] = true
		}
	}
	selected := func(id string) bool { return slices.Contains(selection.DomainIDs, id) }
	laws := []DomainShapeLaw{}
	for _, record := range manifest.Records {
		if record.Status != "accepted" || !manifestLawBearingKinds[record.Kind] || record.HomeDomainID == "" {
			continue
		}
		if explicit[record.ID] || selected(record.HomeDomainID) || slices.ContainsFunc(record.AppliesToDomainIDs, selected) {
			laws = append(laws, DomainShapeLaw{LawID: record.ID, ContentHash: record.SHA256})
		}
	}
	slices.SortFunc(laws, func(a, b DomainShapeLaw) int { return strings.Compare(a.LawID, b.LawID) })
	return laws
}

func readDomainShapePinnedBlob(ctx context.Context, runner GitRunner, repo, commit, path string) ([]byte, error) {
	w := dispatchReadingWitness{repoPath: repo, commitOID: commit, path: path}
	if err := resolveRepositoryReading(ctx, runner, &w); err != nil {
		return nil, err
	}
	return runner.Run(ctx, repo, "cat-file", "blob", commit+":"+path)
}
