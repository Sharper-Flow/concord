package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// knowledgeSourceSetProof is the record a pre-transaction source-set
// verification leaves behind. The verification resolves each source's git
// head and reads the committed law folder at that explicit commit into an
// immutable in-memory snapshot (law-folder authority: folder membership
// supplies acceptance). A git probe can never run inside an open transaction
// (CD-0195 D2) and the store pools one connection, so the consequential law
// boundary proves its reading with this value in the call's context instead:
// the transaction-scoped check refuses a mandated-law read at a designated
// home that carries no proof, and it re-validates the proof against the
// source set and the bounded small-file content read it records. A home whose
// head moved between the verification and the transaction keeps the proof
// only when the law folder and registry content at the new head are the
// content the snapshot was read from (PM1 §3.3); moved law content
// invalidates it.
type knowledgeSourceSetProof struct {
	productID string
	sources   []KnowledgeHome
	snapshots map[string]*lawFolderSnapshot
	// revisions keeps PM1 §3.3's two revisions apart per source. The head is
	// the git head whose law content the verification itself verified; the
	// law folder and registry OIDs are the content identity the snapshot was
	// read from. A commit that moves the head without touching that content
	// is part of the verified state and admits the transaction; the
	// transaction admission compares each later read against these OIDs, not
	// against the head alone.
	revisions map[string]knowledgeSourceVerifiedRevision
	// Standalone failures are demand-driven: a mandate consumes refreshErr.
	// Neither a load failure nor anything else blocks a law-free legacy
	// transition that shares the preparation; the designated path refuses
	// eagerly instead (a law boundary has no degraded form, CD-0200 D3).
	refreshErr error
}

// knowledgeSourceVerifiedRevision is the bounded content proof of one source:
// the verified head plus the law-folder tree OID and the Domain registry blob
// OID at that head. Git object identity makes the pair a law-folder/registry
// digest: they change exactly when the committed law or registry content
// changes.
type knowledgeSourceVerifiedRevision struct {
	head        string
	lawTreeOID  string
	registryOID string
}

type knowledgeSourceSetProofKey struct{}

func withKnowledgeSourceSetProof(ctx context.Context, proof *knowledgeSourceSetProof) context.Context {
	return context.WithValue(ctx, knowledgeSourceSetProofKey{}, proof)
}

func knowledgeSourceSetProofFrom(ctx context.Context) *knowledgeSourceSetProof {
	proof, _ := ctx.Value(knowledgeSourceSetProofKey{}).(*knowledgeSourceSetProof)
	return proof
}

func knowledgeSourceProofKey(home KnowledgeHome) string {
	return home.HomeProjectID + "/" + home.HomeLocatorID
}

// loadLawFolderSnapshotAtHome resolves one home's head and reads the
// committed law folder at that explicit commit. It must run before the
// consequential transaction opens: both steps are git subprocesses the pool
// lock must never hold (CD-0195 D2). The snapshot never resolves a ref on
// its own, so the resolved head here is the commit the transaction
// revalidates against.
func loadLawFolderSnapshotAtHome(ctx context.Context, home KnowledgeHome) (*lawFolderSnapshot, error) {
	head, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		return nil, err
	}
	return loadLawFolderSnapshot(ctx, home.RepoPath, head)
}

// verifyLawSourceSnapshot verifies one source the way the boundary consumes
// it: the committed law folder snapshot at an explicit head, the legislated
// reference admission over that snapshot (CD-0159), and the bounded content
// identity the transaction revalidates against. The head may move while the
// verification runs; PM1 §3.3 admits a move whose law folder and registry
// content are unchanged and re-verifies at the new head when it is not.
func (s *Store) verifyLawSourceSnapshot(ctx context.Context, home KnowledgeHome) (*lawFolderSnapshot, knowledgeSourceVerifiedRevision, error) {
	const attempts = 3
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		head, err := resolveKnowledgeHead(ctx, home)
		if err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		snapshot, err := loadLawFolderSnapshot(ctx, home.RepoPath, head)
		if err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		if err := s.validateSnapshotLegislatedAuthority(ctx, snapshot); err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		revision, err := lawSourceOIDs(ctx, home.RepoPath, head)
		if err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		revision.head = head
		current, err := resolveKnowledgeHead(ctx, home)
		if err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		if current == head {
			return snapshot, revision, nil
		}
		currentOIDs, err := lawSourceOIDs(ctx, home.RepoPath, current)
		if err != nil {
			return nil, knowledgeSourceVerifiedRevision{}, err
		}
		if currentOIDs.lawTreeOID == revision.lawTreeOID && currentOIDs.registryOID == revision.registryOID {
			revision.head = current
			return snapshot, revision, nil
		}
		lastErr = newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge source "+knowledgeSourceProofKey(home)+" moved while its verification ran", true, "verify the knowledge sources again before the consequential transaction opens")
		// The law content moved under the verification; re-verify at the
		// head the content actually sits at.
		if moved, err := loadLawFolderSnapshot(ctx, home.RepoPath, current); err == nil {
			if err := s.validateSnapshotLegislatedAuthority(ctx, moved); err != nil {
				return nil, knowledgeSourceVerifiedRevision{}, err
			}
			if movedOIDs, err := lawSourceOIDs(ctx, home.RepoPath, current); err == nil {
				head, snapshot, revision = current, moved, movedOIDs
				revision.head = current
			}
		}
	}
	return nil, knowledgeSourceVerifiedRevision{}, lastErr
}

// validateSnapshotLegislatedAuthority is the owning admission boundary for
// declared legislative references: before a committed law folder reaches a
// consequential transaction, every legislated law it carries resolves its
// declared reference through the retained CD-0159 rules — the frozen default
// cohort, or one live work whose exact historical contract is approved,
// Product-changing, and admits the law. A held negative refuses the
// verification; an unknown declared work is unverified, not negative, and
// keeps its declared tier. The manifest read and the home role resolution
// happen outside the transaction, so no Git work runs inside it.
func (s *Store) validateSnapshotLegislatedAuthority(ctx context.Context, snapshot *lawFolderSnapshot) error {
	legislated := false
	for _, lawID := range snapshot.LawIDs() {
		if record, _ := snapshot.Law(lawID); record.Authority.Tier == "legislated" {
			legislated = true
			break
		}
	}
	if !legislated {
		return nil
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot begin the law authority admission", true, "retry once the database is readable", err)
	}
	defer tx.Rollback() //nolint:errcheck // a read-only transaction closes by rollback; its queries already returned.
	for _, lawID := range snapshot.LawIDs() {
		record, _ := snapshot.Law(lawID)
		if _, err := resolveKnowledgeLegislatedAuthorityTx(ctx, tx, record); err != nil {
			return lawFileFailure(record.Path, err)
		}
	}
	return nil
}

// lawSourceOIDs reads the committed law-folder tree OID and the Domain
// registry blob OID at one explicit commit through the pinned law runner, so
// the transaction proof carries a bounded law-content digest instead of
// pinning the whole head. A home with no law folder contributes its absence,
// which is itself content identity.
func lawSourceOIDs(ctx context.Context, repo, commitOID string) (knowledgeSourceVerifiedRevision, error) {
	revision := knowledgeSourceVerifiedRevision{lawTreeOID: "absent", registryOID: "absent"}
	runner := lawFolderGitRunner{}
	out, err := runner.Run(ctx, repo, "ls-tree", "-z", commitOID, "--", strings.TrimSuffix(lawFolderRoot, "/"), knowledgeRegistryPath)
	if err != nil {
		return revision, wrapFailure(KindGitUnreachable, "check_mandated_laws", "cannot read the committed law content identity", true, "confirm the git home and commit are reachable", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return revision, wrapFailure(KindInvalidNoteProof, "check_mandated_laws", "the law content identity tree is malformed", false, "repair the committed law tree", err)
	}
	for _, entry := range entries {
		switch entry.path {
		case strings.TrimSuffix(lawFolderRoot, "/"):
			revision.lawTreeOID = entry.kind + ":" + entry.oid
		case knowledgeRegistryPath:
			revision.registryOID = entry.kind + ":" + entry.oid
		}
	}
	return revision, nil
}

// lawSnapshotForSource returns the verified law-folder snapshot of one source
// home from the transaction's proof. A missing proof or a source the proof
// does not bind is a typed refusal: the caller skipped the pre-transaction
// verification, or verified a different source set.
func lawSnapshotForSource(ctx context.Context, home KnowledgeHome, op string) (*lawFolderSnapshot, error) {
	proof := knowledgeSourceSetProofFrom(ctx)
	if proof == nil {
		return nil, newFailure(KindInvalidOperation, op, "the knowledge home was not verified before this transaction", false, "verify the Product's knowledge home before the consequential transaction opens")
	}
	snapshot, ok := proof.snapshots[knowledgeSourceProofKey(home)]
	if !ok {
		// A standalone verification records its failure for the consumer
		// that demands the snapshot; that cause outranks the bare absence.
		if proof.refreshErr != nil {
			return nil, proof.refreshErr
		}
		return nil, newFailure(KindInvalidOperation, op, "the knowledge home was not verified before this transaction", false, "verify the Product's knowledge home before the consequential transaction opens")
	}
	return snapshot, nil
}

// sourceBoundInProof reports whether the pre-transaction proof carries a
// verified snapshot for this source.
func sourceBoundInProof(ctx context.Context, home KnowledgeHome) bool {
	proof := knowledgeSourceSetProofFrom(ctx)
	return proof != nil && proof.snapshots[knowledgeSourceProofKey(home)] != nil
}

// PrepareWorkLawProof verifies, before any transaction for the work opens,
// the committed law and Domain registry that transaction may read. Every
// admission fold compares the active contract's law pins with current law
// (CD-0036), and a step that declares confirm_premise folds the
// investigation artifact's Domain refs against the committed registry. The
// verification resolves git heads and reads the committed law folder —
// subprocesses the pooled connection must never hold inside a transaction
// (CD-0195 D2). A context that already carries a proof passes through; the
// transaction revalidates it. A work whose contract binds no law and no
// Domain, at a step that folds no registry, needs no snapshot. Identity and
// ambiguity refusals stay with the caller's transaction path.
func (s *Store) PrepareWorkLawProof(ctx context.Context, workID string) (context.Context, error) {
	if s == nil || s.db == nil || knowledgeSourceSetProofFrom(ctx) != nil {
		return ctx, nil
	}
	if s.workStepFoldsRegistry(ctx, workID) {
		return s.EstablishKnowledgeSourceSetProof(ctx, workID)
	}
	versions, err := activeWorkflowContractVersions(ctx, s.db, workID)
	if err != nil || len(versions) != 1 {
		return ctx, nil
	}
	var mandates, modifies string
	if err := s.db.QueryRowContext(ctx, `SELECT spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, versions[0]).Scan(&mandates, &modifies); err != nil {
		return ctx, nil
	}
	var mandated, modified []string
	if json.Unmarshal([]byte(mandates), &mandated) != nil || json.Unmarshal([]byte(modifies), &modified) != nil {
		return ctx, nil
	}
	var bound bool
	_ = s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, versions[0]).Scan(&bound)
	if len(mandated) == 0 && len(modified) == 0 && !bound {
		return ctx, nil
	}
	return s.EstablishKnowledgeSourceSetProof(ctx, workID)
}

// EstablishKnowledgeSourceSetProof reads the designated knowledge homes of the
// Product the work's canonical law home belongs to into per-home law-folder
// snapshots, and returns the context that proves the reading to the
// transaction-scoped law boundary entries. Project-qualified law references
// in the work's active contract widen the verified source set with those
// Projects' canonical homes, so a cross-Project mandate resolves through
// verified sources without ever letting a bare ID cross a home. It must run
// before the consequential transaction opens: the snapshot needs the
// connection the transaction would hold, and a git probe must never run
// inside one (CD-0195 D2). A standalone home keeps demand-driven preparation
// before its transaction. A designated Product home requires a verified
// snapshot even when it is the only source: an empty mandate still compares
// the entire inventory.
func (s *Store) EstablishKnowledgeSourceSetProof(ctx context.Context, workID string) (context.Context, error) {
	if s == nil || s.db == nil {
		return ctx, newFailure(KindUnavailable, "check_mandated_laws", "store is not open", false, "open a store before verifying mandated laws")
	}
	// Leave an unresolved law home to its owning boundary clause, rather than
	// moving its refusal ahead of the gate's ordered clauses.
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, s.db, workID)
	if err != nil {
		return ctx, nil
	}
	qualified, err := contractQualifiedLawProjects(ctx, s.db, workID, homeProjectID)
	if err != nil {
		return ctx, err
	}
	return s.establishKnowledgeSourceSetProofAtHomes(ctx, homeProjectID, homeLocatorID, qualified)
}

// contractQualifiedLawProjects lists the Projects the work's active contract
// names through the qualified project_id/law_id reference form (CD-0200),
// excluding the law home's own Project. The mandate is the verification
// demand (CD-0082 D1): its named sources verify beside the Product's own.
func contractQualifiedLawProjects(ctx context.Context, q queryer, workID, homeProjectID string) ([]string, error) {
	versions, err := activeWorkflowContractVersions(ctx, q, workID)
	if err != nil || len(versions) != 1 {
		return nil, err
	}
	var mandates, modifies string
	if err := q.QueryRowContext(ctx, `SELECT spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, versions[0]).Scan(&mandates, &modifies); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the active workflow contract's law mandate", true, "retry once the workflow scope is readable", err)
	}
	var mandated, modified []string
	if json.Unmarshal([]byte(mandates), &mandated) != nil || json.Unmarshal([]byte(modifies), &modified) != nil {
		return nil, newFailure(KindInvariantViolation, "check_mandated_laws", "the active workflow contract's law lists are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	seen := map[string]bool{homeProjectID: true}
	projects := []string{}
	for _, list := range [][]string{mandated, modified} {
		for _, reference := range list {
			projectID, _, qualified, err := parseQualifiedKnowledgeID("check_mandated_laws", reference)
			if err != nil {
				return nil, err
			}
			if !qualified || seen[projectID] {
				continue
			}
			seen[projectID] = true
			projects = append(projects, projectID)
		}
	}
	return projects, nil
}

// EstablishKnowledgeSourceSetProofForProject verifies the same law home before
// a capture has written its primary membership. Initialization revalidates the
// proof against that membership inside the capture's transaction.
func (s *Store) EstablishKnowledgeSourceSetProofForProject(ctx context.Context, projectID string) (context.Context, error) {
	if s == nil || s.db == nil {
		return ctx, newFailure(KindUnavailable, "check_mandated_laws", "store is not open", false, "open a store before verifying mandated laws")
	}
	project, locator, err := workflowLawHomeForProject(ctx, s.db, projectID)
	if err != nil {
		return ctx, nil // Initialization owns absence and ambiguity admission.
	}
	return s.establishKnowledgeSourceSetProofAtHome(ctx, project, locator)
}

func (s *Store) establishKnowledgeSourceSetProofAtHome(ctx context.Context, homeProjectID, homeLocatorID string) (context.Context, error) {
	return s.establishKnowledgeSourceSetProofAtHomes(ctx, homeProjectID, homeLocatorID, nil)
}

func (s *Store) establishKnowledgeSourceSetProofAtHomes(ctx context.Context, homeProjectID, homeLocatorID string, qualifiedProjects []string) (context.Context, error) {
	productID, _, err := resolveKnowledgeSourceRole(ctx, s.db, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return ctx, nil
	}
	if productID == "" {
		homes, err := projectCanonicalHomeCandidates(ctx, s.db, homeProjectID)
		if err != nil {
			return ctx, nil
		}
		for _, home := range homes {
			if home.HomeLocatorID != homeLocatorID {
				continue
			}
			sources := []KnowledgeHome{home}
			sources = append(sources, s.qualifiedSourceHomes(ctx, home, qualifiedProjects)...)
			proof := &knowledgeSourceSetProof{sources: sources, snapshots: map[string]*lawFolderSnapshot{}, revisions: map[string]knowledgeSourceVerifiedRevision{}}
			for _, source := range sources {
				snapshot, revision, err := s.verifyLawSourceSnapshot(ctx, source)
				if err != nil {
					// Standalone failures are demand-driven: a mandate or an
					// inventory read consumes this, a law-free transition
					// keeps its preparation.
					proof.refreshErr = err
					break
				}
				proof.snapshots[knowledgeSourceProofKey(source)] = snapshot
				proof.revisions[knowledgeSourceProofKey(source)] = revision
			}
			return withKnowledgeSourceSetProof(ctx, proof), nil
		}
		return ctx, nil
	}
	sources, err := resolveKnowledgeQuerySources(ctx, s.db, productID, "check_mandated_laws")
	if err != nil {
		return ctx, nil // The transaction's boundary owns source-resolution refusal.
	}
	seenHomes := map[string]bool{}
	for _, source := range sources {
		seenHomes[knowledgeSourceProofKey(source)] = true
	}
	for _, projectID := range qualifiedProjects {
		candidates, err := projectCanonicalHomeCandidates(ctx, s.db, projectID)
		if err != nil {
			return ctx, err
		}
		if len(candidates) == 0 {
			return ctx, newFailure(KindProjectionNotFound, "check_mandated_laws", "the contract's qualified law mandate names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before mandating through it")
		}
		if key := knowledgeSourceProofKey(candidates[0]); !seenHomes[key] {
			seenHomes[key] = true
			sources = append(sources, candidates[0])
		}
	}
	proof := &knowledgeSourceSetProof{productID: productID, sources: sources, snapshots: make(map[string]*lawFolderSnapshot, len(sources)), revisions: make(map[string]knowledgeSourceVerifiedRevision, len(sources))}
	for _, source := range sources {
		// An unreachable or malformed law home refuses the consequential
		// transaction outright — a law boundary has no degraded form
		// (CD-0200 D3).
		snapshot, revision, err := s.verifyLawSourceSnapshot(ctx, source)
		if err != nil {
			return ctx, err
		}
		proof.snapshots[knowledgeSourceProofKey(source)] = snapshot
		proof.revisions[knowledgeSourceProofKey(source)] = revision
	}
	return withKnowledgeSourceSetProof(ctx, proof), nil
}

// qualifiedSourceHomes resolves the canonical homes a qualified mandate names,
// beside the law home the boundary was called with. Each home verifies before
// the transaction opens, so cross-Project references resolve through verified
// sources only.
func (s *Store) qualifiedSourceHomes(ctx context.Context, home KnowledgeHome, qualifiedProjects []string) []KnowledgeHome {
	sources := []KnowledgeHome{}
	for _, projectID := range qualifiedProjects {
		if projectID == home.HomeProjectID {
			continue
		}
		candidates, err := projectCanonicalHomeCandidates(ctx, s.db, projectID)
		if err != nil || len(candidates) == 0 {
			// The demand-driven standalone path answers the missing source at
			// consumption; the snapshot lookup names it then.
			continue
		}
		sources = append(sources, candidates[0])
	}
	return sources
}

// requireKnowledgeSourceSetProof refuses a transaction-scoped mandated-law
// read at a designated home that carries no proof, or a proof that no
// longer matches the transaction's facts: the verified source set digest —
// the Product's designated homes plus the canonical homes the active
// contract's qualified mandate names — each source's resolved canonical
// location, and each source's law content compared against the content the
// verified snapshot was read from. The head and content re-reads use only
// small file reads (resolveKnowledgeHeadCheap and the cheap object walk), so
// the re-validation stays inside CD-0195 D2. The refusal is the completion
// gate's answer when the caller skipped the pre-transaction verification, or
// when a source changed after it.
func requireKnowledgeSourceSetProof(ctx context.Context, q queryer, homeProjectID, homeLocatorID string) error {
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	if productID == "" {
		return requireStandaloneSourceProof(ctx, q, homeProjectID, homeLocatorID)
	}
	sources, err := verifiedKnowledgeSourceSet(ctx, q, productID)
	if err != nil {
		return err
	}
	return requireKnowledgeSourcesProof(ctx, q, productID, sources)
}

// requireStandaloneSourceProof keeps a standalone home's demand-driven
// preparation: without a proof, or with a recorded verification failure, the
// consumer that reads the snapshot answers. A present proof still revalidates
// every source it bound, so moved committed law never feeds a stale snapshot.
func requireStandaloneSourceProof(ctx context.Context, q queryer, homeProjectID, homeLocatorID string) error {
	proof := knowledgeSourceSetProofFrom(ctx)
	if proof == nil || proof.refreshErr != nil {
		return nil
	}
	live := make([]KnowledgeHome, 0, len(proof.sources))
	for i, source := range proof.sources {
		key := knowledgeSourceProofKey(source)
		if i == 0 && (source.HomeProjectID != homeProjectID || source.HomeLocatorID != homeLocatorID) {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "the knowledge home changed after its verification", false, "verify the knowledge home again before the consequential transaction opens")
		}
		candidates, err := projectCanonicalHomeCandidates(ctx, q, source.HomeProjectID)
		if err != nil {
			return err
		}
		found := false
		for _, candidate := range candidates {
			if candidate.HomeLocatorID == source.HomeLocatorID {
				live = append(live, candidate)
				found = true
				break
			}
		}
		if !found {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge home "+key+" resolved to a different canonical location after its verification", false, "verify the knowledge home again before the consequential transaction opens")
		}
	}
	return requireKnowledgeSourcesProof(ctx, q, "", live)
}

// verifiedKnowledgeSourceSet resolves the transaction's law source set: the
// Product's designated homes plus every source the pre-transaction
// verification bound (the qualified-mandate widening), so a qualified
// reference resolves through verified sources and the source-set digest
// compares like for like.
func verifiedKnowledgeSourceSet(ctx context.Context, q queryer, productID string) ([]KnowledgeHome, error) {
	sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_mandated_laws")
	if err != nil {
		return nil, err
	}
	proof := knowledgeSourceSetProofFrom(ctx)
	if proof == nil {
		return sources, nil
	}
	seen := map[string]bool{}
	for _, source := range sources {
		seen[knowledgeSourceProofKey(source)] = true
	}
	for _, source := range proof.sources {
		if !seen[knowledgeSourceProofKey(source)] {
			seen[knowledgeSourceProofKey(source)] = true
			sources = append(sources, source)
		}
	}
	return sources, nil
}

// Both Product and standalone inventory reads revalidate the same canonical
// location and small-file content proof in the transaction.
func requireKnowledgeSourcesProof(ctx context.Context, q queryer, productID string, sources []KnowledgeHome) error {
	proof := knowledgeSourceSetProofFrom(ctx)
	if proof == nil {
		return newFailure(KindInvalidOperation, "check_mandated_laws", "the knowledge home was not verified before this transaction", false, "verify the Product's knowledge home before the consequential transaction opens")
	}
	if proof.refreshErr != nil {
		return proof.refreshErr
	}
	if proof.productID != productID || knowledgeSourceSetDigest(proof.sources) != knowledgeSourceSetDigest(sources) {
		return newFailure(KindInvalidOperation, "check_mandated_laws", "the knowledge home changed after its verification", false, "verify the Product's knowledge home again before the consequential transaction opens")
	}
	current := make(map[string]KnowledgeHome, len(sources))
	for _, source := range sources {
		current[knowledgeSourceProofKey(source)] = source
	}
	for _, source := range proof.sources {
		key := knowledgeSourceProofKey(source)
		// The proof binds each source's resolved canonical location with its
		// identity. A locator update between the verification and the
		// transaction moves the canonical authority while the verified
		// snapshot still names the old repository's commit, so the admission
		// revalidates the resolved location before it trusts the snapshot.
		if live, ok := current[key]; !ok || live.RepoPath != source.RepoPath || live.HeadRef != source.HeadRef {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge home "+key+" resolved to a different canonical location after its verification", false, "verify the Product's knowledge home again before the consequential transaction opens")
		}
		verified := proof.revisions[key]
		snapshot := proof.snapshots[key]
		if snapshot == nil || verified.head == "" {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge home "+key+" carries no verified law snapshot", false, "verify the Product's knowledge home again before the consequential transaction opens")
		}
		head, headErr := resolveKnowledgeHeadCheap(source.RepoPath, source.HeadRef)
		if headErr != nil {
			return wrapFailure(KindInvalidOperation, "check_mandated_laws", "the git head of knowledge home "+key+" cannot be re-validated inside the transaction", false, "verify the Product's knowledge home again before the consequential transaction opens", headErr)
		}
		if head == verified.head {
			continue
		}
		// PM1 §3.3: the head may have moved past the verified revision by a
		// commit that touches no law content — that commit is itself part of
		// the verified state. The cheap content walk compares the law folder
		// and registry OIDs, never the head alone.
		cheap, cheapErr := resolveLawSourceOIDsCheap(source.RepoPath, source.HeadRef)
		if cheapErr != nil {
			return wrapFailure(KindInvalidOperation, "check_mandated_laws", "the law content of knowledge home "+key+" cannot be re-validated inside the transaction", false, "verify the Product's knowledge home again before the consequential transaction opens", cheapErr)
		}
		if cheap.head != head {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge home "+key+" cannot be re-validated against its resolved head inside the transaction", false, "verify the Product's knowledge home again before the consequential transaction opens")
		}
		if cheap.lawTreeOID == verified.lawTreeOID && cheap.registryOID == verified.registryOID {
			continue
		}
		return newFailure(KindInvalidOperation, "check_mandated_laws", "knowledge home "+key+" moved its committed law content after its verification", false, "verify the Product's knowledge home again before the consequential transaction opens")
	}
	return nil
}

func checkMandatedLawsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string, allowAmendment bool) error {
	if len(mandated) == 0 {
		return validateLawModificationSubset(mandated, modified)
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	return checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsTxAtHome(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if tx == nil {
		return newFailure(KindUnavailable, "check_mandated_laws", "transaction is not open", false, "open a mutation transaction")
	}
	if err := requireKnowledgeSourceSetProof(ctx, tx, homeProjectID, homeLocatorID); err != nil {
		return err
	}
	return checkMandatedLawsQuery(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsQuery(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if homeProjectID == "" || homeLocatorID == "" {
		return newFailure(KindUnknownScope, "check_mandated_laws", "canonical law home is incomplete", false, "resolve one canonical Git knowledge home")
	}
	if len(mandated) > 32 || len(modified) > 32 {
		return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return err
	}
	if len(mandated) == 0 {
		return nil
	}
	if proof := knowledgeSourceSetProofFrom(ctx); proof != nil && proof.refreshErr != nil {
		return proof.refreshErr
	}
	// CD-0200 D4/D6: a bare mandated ID resolves in the Product's designated
	// home, and a qualified project_id/law_id form resolves in its named
	// Project's canonical home. Bare IDs alone keep the single-home path below.
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	anyQualified := false
	for _, id := range mandated {
		if _, _, qualified, parseErr := parseQualifiedKnowledgeID("check_mandated_laws", id); parseErr != nil {
			return parseErr
		} else if qualified {
			anyQualified = true
		}
	}
	var sources []KnowledgeHome
	if productID != "" {
		resolved, err := verifiedKnowledgeSourceSet(ctx, q, productID)
		if err != nil {
			return err
		}
		sources = resolved
		if anyQualified {
			return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
		}
	} else if anyQualified {
		// A qualified reference without a Product designation resolves
		// through the named Project's own canonical locator, beside the
		// resolved home the boundary was called with.
		sources, err := qualifiedMandateSources(ctx, q, homeProjectID, homeLocatorID, mandated)
		if err != nil {
			return err
		}
		return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
	}
	return checkMandatedLawsAtSingleHome(ctx, q, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func qualifiedMandateSources(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated []string) ([]KnowledgeHome, error) {
	sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
	seenProjects := map[string]bool{homeProjectID: true}
	for _, id := range mandated {
		projectID, _, qualified, parseErr := parseQualifiedKnowledgeID("check_mandated_laws", id)
		if parseErr != nil {
			return nil, parseErr
		}
		if !qualified || seenProjects[projectID] {
			continue
		}
		candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "qualified mandate names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before mandating through it")
			failure.CandidateIDs = []string{id}
			return nil, failure
		}
		seenProjects[projectID] = true
		sources = append(sources, candidates[0])
	}
	return sources, nil
}

// checkMandatedLawsAtSingleHome answers the mandate from the verified
// law-folder snapshot of one home. Every snapshot law is accepted by
// construction: the folder is the acceptance authority, so a mandated ID the
// snapshot does not carry is not current law.
func checkMandatedLawsAtSingleHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	source := KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}
	snapshot, err := lawSnapshotForSource(ctx, source, "check_mandated_laws")
	if err != nil {
		return err
	}
	accepted := map[string]bool{}
	authority := map[string]string{}
	for _, id := range mandated {
		if record, ok := snapshot.Law(bareLawID(id)); ok {
			accepted[id] = true
			authority[id] = record.Authority.Tier
		}
	}
	missing := make([]string, 0)
	for _, id := range mandated {
		if !accepted[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		return mandatedLawProjectionFailure(ctx, q, []KnowledgeHome{source}, missing)
	}
	if err := checkSingleHomeLawConflicts(ctx, snapshot, mandated, modified, authority, allowAmendment); err != nil {
		return err
	}
	return nil
}

// bareLawID strips a qualified project_id/law_id reference to the stable ID
// the law folder carries. Callers have already parsed the reference, so a
// malformed reference cannot reach this.
func bareLawID(reference string) string {
	if _, lawID, _, err := parseQualifiedKnowledgeID("check_mandated_laws", reference); err == nil {
		return lawID
	}
	return reference
}

// mandatedLawProjectionFailure diagnoses a missing mandated ID without
// probing Git inside the consequential transaction. The law folder snapshot
// carries only law, so an absent ID is either outside the folder or not
// committed at the verified revision; a matching archived work note names
// the not-law record the caller mandated instead.
func mandatedLawProjectionFailure(ctx context.Context, q queryer, sources []KnowledgeHome, missing []string) error {
	commits := make([]string, 0, len(sources))
	for _, source := range sources {
		commit := "unverified"
		if snapshot, err := lawSnapshotForSource(ctx, source, "check_mandated_laws"); err == nil {
			commit = snapshot.CommitOID()
		}
		commits = append(commits, source.HomeProjectID+"/"+source.HomeLocatorID+"@"+commit)
	}
	absent := []string{}
	nonaccepted := []string{}
	for _, reference := range missing {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID("check_mandated_laws", reference)
		if err != nil {
			return err
		}
		found := false
		for _, source := range sources {
			if qualified && source.HomeProjectID != projectID {
				continue
			}
			var kind, status string
			err := q.QueryRowContext(ctx, `SELECT type,outcome_tag FROM archived_work WHERE home_project_id=? AND home_locator_id=? AND id=?`, source.HomeProjectID, source.HomeLocatorID, lawID).Scan(&kind, &status)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot inspect a mandated knowledge record", true, "retry once the archived note projection is readable", err)
			}
			found = true
			identity := reference
			if len(sources) > 1 {
				identity = source.HomeProjectID + "/" + lawID
			}
			nonaccepted = append(nonaccepted, identity+" ("+kind+", "+status+")")
		}
		if !found {
			absent = append(absent, reference)
		}
	}
	details := []string{}
	if len(absent) > 0 {
		details = append(details, "mandated IDs are not law files in the committed law folder at the checked snapshot commit: "+strings.Join(absent, ","))
	}
	if len(nonaccepted) > 0 {
		details = append(details, "mandated records are archived notes, not law files in the committed law folder: "+strings.Join(nonaccepted, ","))
	}
	details = append(details, "checked law snapshot commits: "+strings.Join(commits, ","))
	failure := newFailure(KindProjectionNotFound, "check_mandated_laws", strings.Join(details, "; "), false, "verify the mandate IDs against the committed law folder at the checked snapshot commit; a missing law is enacted by adding its file under the law folder through an ordinary reviewed pull request")
	failure.CandidateIDs = missing
	return failure
}

// checkSingleHomeLawConflicts reads the explicit conflicts the mandated laws
// themselves declare in the verified snapshot. Each conflicts_with relation
// is authored on its declaring record, so the declaring record is always
// known; the pair is checked in the canonical source/target order the
// boundary has always answered with.
func checkSingleHomeLawConflicts(ctx context.Context, snapshot *lawFolderSnapshot, mandated, modified []string, authority map[string]string, allowAmendment bool) error {
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	ordered := slices.Clone(mandated)
	sort.Strings(ordered)
	seenPairs := map[string]bool{}
	for _, id := range ordered {
		record, ok := snapshot.Law(bareLawID(id))
		if !ok {
			continue
		}
		for _, relation := range record.LawRelations {
			if relation.Kind != "conflicts_with" {
				continue
			}
			targetReference, known := mandatedReferenceForID(mandated, relation.TargetID)
			if !known {
				continue
			}
			sourceReference, targetReference := id, targetReference
			if sourceReference > targetReference {
				sourceReference, targetReference = targetReference, sourceReference
			}
			pairKey := sourceReference + "\x00" + targetReference
			if seenPairs[pairKey] {
				continue
			}
			seenPairs[pairKey] = true
			sourceEnd := conflictEnd{lawID: sourceReference, modified: modifiedSet[sourceReference], derived: authority[sourceReference] == "derived"}
			targetEnd := conflictEnd{lawID: targetReference, modified: modifiedSet[targetReference], derived: authority[targetReference] == "derived"}
			if err := explicitConflictRefusal(sourceEnd, targetEnd, record.ID, allowAmendment); err != nil {
				return err
			}
		}
	}
	return nil
}

// mandatedReferenceForID returns the mandate reference whose bare law ID is
// lawID, so a conflict endpoint answers under the reference the caller
// mandated with.
func mandatedReferenceForID(mandated []string, lawID string) (string, bool) {
	for _, candidate := range mandated {
		if bareLawID(candidate) == lawID {
			return candidate, true
		}
	}
	return "", false
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

// validateDerivedLawModification holds the tier-scoped half of the
// Product-truth boundary that CD-0222 D5 states: a contract that does not change
// Product truth may revise derived law in-contract, and nothing else. It reads each
// modified law's authority tier from the verified law-folder snapshot through
// the work's canonical law home, so the approve path and the fold answer with
// one source and cannot drift. Project-qualified modified
// references resolve through the same Project-qualified identity as
// the mandated-law boundary (CD-0200 D6). A missing
// name or a non-derived tier is a refusal; the caller's recovery hint names
// the Product-changing route.
func validateDerivedLawModification(ctx context.Context, q queryer, workID string, modified []string) error {
	if len(modified) == 0 {
		return nil
	}
	if len(modified) > 32 {
		return newFailure(KindInvalidPayload, "check_derived_law_modification", "law_modifies exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return err
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	anyQualified := false
	for _, id := range modified {
		if _, _, qualified, parseErr := parseQualifiedKnowledgeID("check_derived_law_modification", id); parseErr != nil {
			return parseErr
		} else if qualified {
			anyQualified = true
		}
	}
	if productID != "" {
		sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_derived_law_modification")
		if err != nil {
			return err
		}
		if anyQualified {
			return validateDerivedLawModificationAcrossSources(ctx, q, sources, modified)
		}
	} else if anyQualified {
		// A qualified reference without a Product designation resolves
		// through the named Project's own canonical locator, beside the
		// resolved home the boundary was called with.
		sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
		seenProjects := map[string]bool{homeProjectID: true}
		for _, id := range modified {
			projectID, _, qualified, parseErr := parseQualifiedKnowledgeID("check_derived_law_modification", id)
			if parseErr != nil {
				return parseErr
			}
			if !qualified || seenProjects[projectID] {
				continue
			}
			candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				failure := newFailure(KindProjectionNotFound, "check_derived_law_modification", "qualified modification names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before modifying through it")
				failure.CandidateIDs = []string{id}
				return failure
			}
			seenProjects[projectID] = true
			sources = append(sources, candidates[0])
		}
		return validateDerivedLawModificationAcrossSources(ctx, q, sources, modified)
	}
	return validateDerivedLawModificationAtHome(ctx, q, homeProjectID, homeLocatorID, modified)
}

func validateDerivedLawModificationAtHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, modified []string) error {
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}, "check_derived_law_modification")
	if err != nil {
		return err
	}
	tiers := make(map[string]string, len(modified))
	for _, id := range modified {
		if record, ok := snapshot.Law(bareLawID(id)); ok {
			tiers[id] = record.Authority.Tier
		}
	}
	return refuseNonDerivedLawModification(modified, tiers)
}

// validateDerivedLawModificationAcrossSources resolves each modified
// reference through the shared law reference resolver and refuses a
// non-derived tier. The tier is read from the verified snapshot of the source
// the reference resolved through, so a shared Product's derived law answers
// from that source's committed folder.
func validateDerivedLawModificationAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, modified []string) error {
	resolutions, missing, err := resolveLawReferences(ctx, q, "check_derived_law_modification", sources, modified)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		return newFailure(KindProjectionNotFound, "check_derived_law_modification", "a modified law is not a file in the committed law folder: "+strings.Join(missing, ","), false, "publish the law file under the law folder through an ordinary reviewed pull request")
	}
	tiers := make(map[string]string, len(modified))
	for reference, r := range resolutions {
		tiers[reference] = r.tier
	}
	return refuseNonDerivedLawModification(modified, tiers)
}

func refuseNonDerivedLawModification(modified []string, tiers map[string]string) error {
	nonDerived := make([]string, 0)
	for _, id := range modified {
		tier, ok := tiers[id]
		if !ok {
			return newFailure(KindProjectionNotFound, "check_derived_law_modification", "a modified law is not a file in the committed law folder: "+id, false, "publish the law file under the law folder through an ordinary reviewed pull request")
		}
		if tier != "derived" {
			nonDerived = append(nonDerived, id)
		}
	}
	if len(nonDerived) != 0 {
		return newFailure(KindInvalidPayload, "check_derived_law_modification", "a contract that does not change Product truth may revise only derived law: "+strings.Join(nonDerived, ","), false, "leave law_modifies empty or select a Product-changing workflow")
	}
	return nil
}

func validateLawModificationSubset(mandated, modified []string) error {
	mandate := make(map[string]bool, len(mandated))
	for _, id := range mandated {
		if id == "" || mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate contains an empty or duplicate ID", false, "supply a unique bounded law mandate")
		}
		mandate[id] = true
	}
	seen := map[string]bool{}
	for _, id := range modified {
		if id == "" || seen[id] || !mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law_modifies must be a subset of spec_mandate", false, "declare every modified law in spec_mandate")
		}
		seen[id] = true
	}
	return nil
}

// lawReferenceResolution is one reference's verified law: its source home,
// standing, tier, and whole-file digest.
type lawReferenceResolution struct {
	source KnowledgeHome
	status string
	tier   string
	hash   string
	lawID  string
}

// resolveLawReferences resolves each reference against the verified
// law-folder snapshot of the source that holds it. Bare references resolve in
// the workflow home. Qualified references resolve only in their named
// Project's canonical home. Callers supply those homes.
func resolveLawReferences(ctx context.Context, q queryer, op string, sources []KnowledgeHome, references []string) (map[string]lawReferenceResolution, []string, error) {
	resolved := make(map[string]lawReferenceResolution, len(references))
	missing := make([]string, 0)
	for _, reference := range references {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID(op, reference)
		if err != nil {
			return nil, nil, err
		}
		var match *lawReferenceResolution
		for index, source := range sources {
			if qualified && source.HomeProjectID != projectID || !qualified && index != 0 {
				continue
			}
			snapshot, err := lawSnapshotForSource(ctx, source, op)
			if err != nil {
				if sourceBoundInProof(ctx, source) {
					return nil, nil, err
				}
				// A source the verification does not bind answers as missing
				// law, never as an imported answer from an unverified home.
				continue
			}
			record, ok := snapshot.Law(lawID)
			if !ok {
				continue
			}
			match = &lawReferenceResolution{source: source, status: record.Status, tier: record.Authority.Tier, hash: record.SHA256, lawID: lawID}
			break
		}
		if match == nil {
			missing = append(missing, reference)
		} else {
			resolved[reference] = *match
		}
	}
	return resolved, missing, nil
}

// conflictEnd is one endpoint of an explicit conflict as the amendment path
// sees it: whether the contract revises it and whether its tier is derived.
// lawID is the name law_modifies takes: the bare law ID at a single home, the
// mandate reference across sources.
type conflictEnd struct {
	lawID    string
	modified bool
	derived  bool
}

// explicitConflictRefusal decides one explicit conflict between mandated
// laws. Completion carries no amendment allowance and refuses every conflict.
// At approval only the record that declares the conflict can withdraw it, so
// the contract must revise that record (CD-0015) and may resolve it inside
// the contract only under the tier rule (CD-0159 D4). The folder snapshot
// reads each relation from its declaring record, so the declaring record is
// always known.
func explicitConflictRefusal(source, target conflictEnd, declarer string, allowAmendment bool) error {
	detail := fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", source.lawID, target.lawID)
	if !allowAmendment {
		return newFailure(KindRelationConflict, "check_mandated_laws", detail, false, "resolve the Git law conflict or declare and approve the amendment path")
	}
	switch declarer {
	case source.lawID:
		return declaredConflictRefusal(detail, source, target)
	case target.lawID:
		return declaredConflictRefusal(detail, target, source)
	}
	return newFailure(KindRelationConflict, "check_mandated_laws", detail, false, "resolve the Git law conflict or declare and approve the amendment path")
}

// declaredConflictRefusal applies the tier rule to a conflict whose declaring
// record is known, and names that record when the contract cannot resolve it.
func declaredConflictRefusal(detail string, declarer, other conflictEnd) error {
	if conflictResolvesInsideContract(declarer, other) {
		return nil
	}
	var recovery string
	switch {
	case !declarer.derived:
		recovery = "the declaring record is legislated: take the conflict to an operator checkpoint to clarify intent, evolve the law, or accept a scope reduction"
	case !declarer.modified && other.modified && !other.derived:
		recovery = "add the declaring record to law_modifies, with its home Domain among the affected Domains when the contract changes the Product, and leave the legislated end unrevised, or take the conflict to an operator checkpoint"
	case !declarer.modified:
		recovery = "add the declaring record to law_modifies, with its home Domain among the affected Domains when the contract changes the Product"
	default:
		recovery = "the contract revises legislated law: revise only derived law, or take the conflict to an operator checkpoint"
	}
	failure := newFailure(KindRelationConflict, "check_mandated_laws", detail+", declared by "+declarer.lawID, false, recovery)
	failure.CandidateIDs = []string{declarer.lawID}
	return failure
}

// conflictResolvesInsideContract is the tier rule for an explicit conflict
// with a known declaring record. The contract must revise the declaring
// record, that record must be derived, and the other end may be revised only
// if it is derived too: revising legislated law keeps the operator's
// legislative moment.
func conflictResolvesInsideContract(declarer, other conflictEnd) bool {
	if !declarer.modified || !declarer.derived {
		return false
	}
	if other.modified {
		return other.derived
	}
	// The other end stays untouched, whatever its tier. When it is
	// legislated, the derived declaring record is brought into conformance
	// with an untouched legislated commitment, which CD-0159 D4 resolves
	// inside the contract. That rule lives here and nowhere else.
	return true
}

type mandatedSourceLawSet struct {
	home          KnowledgeHome
	idToReference map[string]string
}

// checkMandatedLawsAcrossSources checks Project-qualified law references
// (CD-0200). Each reference must resolve to a law file in the verified
// snapshot of the source that holds it. Conflicts read from the declaring
// records inside the snapshot of the source that owns the resolved law.
func checkMandatedLawsAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, mandated, modified []string, allowAmendment bool) error {
	resolutions, missing, err := resolveLawReferences(ctx, q, "check_mandated_laws", sources, mandated)
	if err != nil {
		return err
	}
	resolved := make(map[string]lawReferenceResolution, len(mandated))
	for _, reference := range mandated {
		if r, ok := resolutions[reference]; ok {
			resolved[reference] = r
		}
	}
	if len(missing) != 0 {
		return mandatedLawProjectionFailure(ctx, q, sources, missing)
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	// Each source's conflict scan runs over the bare law IDs that resolved
	// in that source, so a qualified mandate endpoint matches its declared
	// relation.
	perSource := make(map[string]*mandatedSourceLawSet, len(resolved))
	for reference, r := range resolved {
		key := knowledgeSourceProofKey(r.source)
		set := perSource[key]
		if set == nil {
			set = &mandatedSourceLawSet{home: r.source, idToReference: map[string]string{}}
			perSource[key] = set
		}
		set.idToReference[r.lawID] = reference
	}
	for _, set := range perSource {
		if err := checkMandatedSourceLawConflicts(ctx, set, modifiedSet, resolved, allowAmendment); err != nil {
			return err
		}
	}
	return nil
}

func checkMandatedSourceLawConflicts(ctx context.Context, set *mandatedSourceLawSet, modifiedSet map[string]bool, resolved map[string]lawReferenceResolution, allowAmendment bool) error {
	snapshot, err := lawSnapshotForSource(ctx, set.home, "check_mandated_laws")
	if err != nil {
		return err
	}
	lawIDs := make([]string, 0, len(set.idToReference))
	for lawID := range set.idToReference {
		lawIDs = append(lawIDs, lawID)
	}
	sort.Strings(lawIDs)
	seenPairs := map[string]bool{}
	for _, lawID := range lawIDs {
		record, ok := snapshot.Law(lawID)
		if !ok {
			continue
		}
		for _, relation := range record.LawRelations {
			if relation.Kind != "conflicts_with" {
				continue
			}
			targetReference, targetKnown := set.idToReference[relation.TargetID]
			if !targetKnown {
				continue
			}
			declaringReference := set.idToReference[lawID]
			sourceReference, targetReference := declaringReference, targetReference
			if sourceReference > targetReference {
				sourceReference, targetReference = targetReference, sourceReference
			}
			pairKey := sourceReference + "\x00" + targetReference
			if seenPairs[pairKey] {
				continue
			}
			seenPairs[pairKey] = true
			sourceEnd := conflictEnd{lawID: sourceReference, modified: modifiedSet[sourceReference], derived: resolved[sourceReference].tier == "derived"}
			targetEnd := conflictEnd{lawID: targetReference, modified: modifiedSet[targetReference], derived: resolved[targetReference].tier == "derived"}
			if err := explicitConflictRefusal(sourceEnd, targetEnd, declaringReference, allowAmendment); err != nil {
				return err
			}
		}
	}
	return nil
}

// workflowLawHome resolves the workflow's canonical Git law home over whichever
// read handle the caller already holds, per the store's queryer contract. The
// law home follows the work item's primary Project membership: the primary
// Project's Product owns the contract, so a secondary membership in another
// Product must never widen the home choice or make the workflow unrecordable.
func workflowLawHome(ctx context.Context, q queryer, workID string) (string, string, error) {
	var project string
	if err := q.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		if err == sql.ErrNoRows {
			return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
		}
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Project", true, "retry once the workflow scope is readable", err)
	}
	return workflowLawHomeForProject(ctx, q, project)
}

func workflowLawHomeForProject(ctx context.Context, q queryer, projectID string) (string, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id FROM product_knowledge_homes ph JOIN product_projects pp ON pp.product_id=ph.product_id WHERE pp.project_id=? ORDER BY ph.project_id,ph.locator_id`, projectID)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	defer rows.Close()
	var homes [][2]string
	for rows.Next() {
		var project, locator string
		if err := rows.Scan(&project, &locator); err != nil {
			return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
		}
		if len(homes) == 0 || homes[len(homes)-1][0] != project || homes[len(homes)-1][1] != locator {
			homes = append(homes, [2]string{project, locator})
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	if len(homes) == 1 {
		return homes[0][0], homes[0][1], nil
	}
	if len(homes) > 1 {
		candidates := make([]string, 0, len(homes))
		for _, home := range homes {
			candidates = append(candidates, home[0]+"/"+home[1])
		}
		return "", "", newAmbiguousScopeFailure("check_mandated_laws", "workflow resolves to multiple canonical Git law homes", "resolve one Product knowledge home", candidates)
	}
	var project, locator string
	err = q.QueryRowContext(ctx, `SELECT project_id,locator_id FROM project_locators WHERE project_id=? AND kind='canonical_path'`, projectID).Scan(&project, &locator)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Git law home", true, "retry once the workflow scope is readable", err)
	}
	return project, locator, nil
}
