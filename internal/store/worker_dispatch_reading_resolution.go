package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Every declared required reading resolves before a worker spawns. The
// dispatch preparation samples every declared reading's source identities in
// the same read snapshot as the work version, claim, and canonical context,
// closes that snapshot, then resolves the declared sources with Git under
// one shared fixed deadline, and seals the witnesses. The dispatch admission
// revalidates the sampled identities with SQL only and refuses on any change
// before the dispatch completion. No Git subprocess ever runs under a SQL
// transaction (CD-0195 D2).
//
// Source resolution never mutates the declared readings, never registers a
// source, never fetches from the network, never falls back to the changed
// checkout, never scans arbitrary Git history, and never repins a moved law
// revision. The committed-read policy (lazy fetch and replacement refs
// disabled) rides every resolution Git command, so a pinned source proves
// only local committed content. A reading that cannot resolve refuses with
// a typed failure that names the reading ordinal and its source identity,
// before any dispatch effect exists. Recorded history replays without
// resolution.
//
// Source identity rules:
//
//   - A repository_file reading names the Project that owns the repository.
//     The declared Project must belong to the work's Product scope, and its
//     one canonical-path locator resolves the repository; zero or several
//     canonical locators refuse. The declared commit must resolve as a commit
//     object and the declared path as one ordinary blob at that commit in
//     that repository — never the dispatch checkout.
//   - A knowledge reading's source_id names the Project of the Product's
//     designated knowledge home (CD-0200 D1). The law ID and content hash
//     must join that source's exact projected law revision, and the
//     revision's canonical locator must resolve in the git-derived
//     projection. The committed manifest and blob verify through the
//     existing committed-record proof owner after the snapshot closes.
//   - A domain_shape reading selects the designated home of the work's
//     Product. Its exact bounded JSON, commit and explicit selector must
//     reproduce through the selected-read core and committed source proofs.

// workerDispatchReadingResolutionDeadline is the fixed total budget for the
// whole declared-reading resolution phase — every repository, knowledge and shape
// reading together, not per reading. It is a constant of the owning package,
// not configuration.
const workerDispatchReadingResolutionDeadline = 10 * time.Second

const workerDispatchReadingResolutionOp = "worker_dispatch_reading_resolution"

// dispatchReadingWitness is one declared reading's sealed resolution
// witness: the SQL-sampled source identities plus the Git proof outcome. The
// producers sample canonical home rows for dispatch or a bound worker read;
// no caller value constructs one.
type dispatchReadingWitness struct {
	ordinal  int
	identity string

	arm string

	// repository arm
	projectID string
	locatorID string
	repoPath  string
	commitOID string
	path      string

	// knowledge arm
	sourceProject     string
	sourceLocator     string
	sourceRepo        string
	lawID             string
	contentHash       string
	watermarkCommit   string
	watermarkDigest   string
	watermarkVersion  int64
	watermarkComplete bool
	recordPath        string
	recordCommit      string
	// projectedRecord carries the assembled projected manifest record the
	// committed-record proof verifies; it is derived wholly from the
	// sampled projection rows, so sampledEqual compares the rows above and
	// never this pointer.
	projectedRecord *KnowledgeRecord
	shape           *DomainShapeResult
	shapeDomain     string

	// sampled source authority shared by all arms: the locator row
	// identity, the owning Project's version, the work's Product version,
	// and the identity digest of the designated knowledge home.
	locatorCreatedAt string
	locatorUpdatedAt string
	projectVersion   int64
	productVersion   int64
	sourceSetDigest  string

	resolved bool
}

// sampledEqual compares the SQL-sampled identities two witnesses carry; the
// admission refuses when any of them changed under the resolution phase.
func (w dispatchReadingWitness) sampledEqual(other dispatchReadingWitness) bool {
	return w.arm == other.arm &&
		w.projectID == other.projectID && w.locatorID == other.locatorID && w.repoPath == other.repoPath &&
		w.commitOID == other.commitOID && w.path == other.path &&
		w.sourceProject == other.sourceProject && w.sourceLocator == other.sourceLocator && w.sourceRepo == other.sourceRepo &&
		w.lawID == other.lawID && w.contentHash == other.contentHash &&
		w.watermarkCommit == other.watermarkCommit && w.watermarkDigest == other.watermarkDigest &&
		w.watermarkVersion == other.watermarkVersion && w.watermarkComplete == other.watermarkComplete &&
		w.recordPath == other.recordPath && w.recordCommit == other.recordCommit &&
		w.locatorCreatedAt == other.locatorCreatedAt && w.locatorUpdatedAt == other.locatorUpdatedAt &&
		w.projectVersion == other.projectVersion && w.productVersion == other.productVersion &&
		w.sourceSetDigest == other.sourceSetDigest
}

// repositoryReadingIdentity renders the declared repository source identity
// a refusal names.
func repositoryReadingIdentity(projectID, path, commitOID string) string {
	return projectID + "/" + path + "@" + commitOID
}

// knowledgeReadingIdentity renders the declared knowledge source identity a
// refusal names.
func knowledgeReadingIdentity(sourceID, lawID, contentHash string) string {
	return sourceID + "/" + lawID + "@" + contentHash
}

// readingResolutionFailure wraps one reading's refusal so it names the
// reading ordinal and the declared source identity.
func readingResolutionFailure(ordinal int, identity string, err error) error {
	var failure *Failure
	if failureAs(err, &failure) {
		named := *failure
		named.Detail = fmt.Sprintf("required reading %d (%s): %s", ordinal, identity, failure.Detail)
		return &named
	}
	return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, fmt.Sprintf("required reading %d (%s) cannot resolve", ordinal, identity), true, "retry once the declared sources are readable", err)
}

// sampleDispatchReadingIdentities samples every declared reading's source
// identities with SQL only. It runs inside the dispatch preparation's read
// snapshot, so the sampled identities observe the same snapshot as the work
// version, claim, and canonical context. It performs no Git and no
// filesystem work.
func sampleDispatchReadingIdentities(ctx context.Context, q queryer, workID string, readings []WorkContextReading) ([]dispatchReadingWitness, error) {
	return sampleReadingIdentities(ctx, q, workID, readings, false)
}

func sampleReadingIdentities(ctx context.Context, q queryer, workID string, readings []WorkContextReading, held bool) ([]dispatchReadingWitness, error) {
	productID, err := architectureBindingProductID(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	var productVersion int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM products WHERE id=?`, productID).Scan(&productVersion); err != nil {
		return nil, wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the work's Product version", true, "retry once the Product projection is readable", err)
	}
	var knowledgeSources []KnowledgeHome
	sourcesResolved := false
	witnesses := make([]dispatchReadingWitness, 0, len(readings))
	for ordinal, reading := range readings {
		witness := dispatchReadingWitness{ordinal: ordinal, arm: reading.Source.Kind, productVersion: productVersion}
		switch reading.Source.Kind {
		case WorkContextSourceRepositoryFile:
			witness.identity = repositoryReadingIdentity(reading.Source.ProjectID, reading.Source.Path, reading.Source.CommitOID)
			witness.projectID = reading.Source.ProjectID
			witness.commitOID = reading.Source.CommitOID
			witness.path = reading.Source.Path
			if err := sampleRepositoryReadingIdentity(ctx, q, productID, &witness); err != nil {
				return nil, readingResolutionFailure(ordinal, witness.identity, err)
			}
		case WorkContextSourceKnowledge:
			witness.identity = knowledgeReadingIdentity(reading.Source.SourceID, reading.Source.LawID, reading.Source.ContentHash)
			witness.sourceProject = reading.Source.SourceID
			witness.lawID = reading.Source.LawID
			witness.contentHash = reading.Source.ContentHash
			if !sourcesResolved {
				resolved, err := resolveKnowledgeQuerySources(ctx, q, productID, workerDispatchReadingResolutionOp)
				if err != nil {
					return nil, err
				}
				knowledgeSources = resolved
				sourcesResolved = true
			}
			if err := sampleKnowledgeReadingIdentity(ctx, q, productID, knowledgeSources, &witness); err != nil {
				return nil, readingResolutionFailure(ordinal, witness.identity, err)
			}
		case WorkContextSourceDomainShape:
			shapeWitness, err := sampleShapeReadingIdentity(ctx, q, workID, productID, reading, held)
			if err != nil {
				return nil, readingResolutionFailure(ordinal, reading.Source.SourceID+"/domain_shape@"+reading.Source.ContentHash, err)
			}
			shapeWitness.ordinal = ordinal
			shapeWitness.productVersion = productVersion
			witness = shapeWitness
		default:
			return nil, readingResolutionFailure(ordinal, witness.identity, newFailure(KindInvalidPayload, workerDispatchReadingResolutionOp, "declares an unknown reading source kind", false, "use the closed reading source union"))
		}
		witnesses = append(witnesses, witness)
	}
	return witnesses, nil
}

func sampleShapeReadingIdentity(ctx context.Context, q queryer, workID, productID string, reading WorkContextReading, held bool) (dispatchReadingWitness, error) {
	if held {
		return sampleHeldDomainShapeReading(ctx, q, productID, reading)
	}
	if err := validateWorkContextDomain(ctx, q, workID, reading.DomainID, reading.ProductWideRationale); err != nil {
		return dispatchReadingWitness{}, err
	}
	return sampleDomainShapeReading(ctx, q, productID, reading)
}

func sampleRepositoryReadingIdentity(ctx context.Context, q queryer, productID string, witness *dispatchReadingWitness) error {
	var member int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?`, productID, witness.projectID).Scan(&member); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "declares Project "+witness.projectID+" outside the work's Product scope", false, "declare a repository reading from a Project of the work's Product")
		}
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot verify the declared Project's Product membership", true, "retry once the Product scope is readable", err)
	}
	locatorID, repoPath, err := uniqueProjectRepositoryLocator(ctx, q, witness.projectID)
	if err != nil {
		return err
	}
	witness.locatorID, witness.repoPath = locatorID, repoPath
	if err := q.QueryRowContext(ctx, `SELECT created_at, updated_at FROM project_locators WHERE project_id=? AND locator_id=?`, witness.projectID, locatorID).Scan(&witness.locatorCreatedAt, &witness.locatorUpdatedAt); err != nil {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the declared Project's canonical locator", true, "retry once the Project locator is readable", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, witness.projectID).Scan(&witness.projectVersion); err != nil {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the declared Project's version", true, "retry once the Project projection is readable", err)
	}
	return nil
}

func sampleKnowledgeReadingIdentity(ctx context.Context, q queryer, productID string, knowledgeSources []KnowledgeHome, witness *dispatchReadingWitness) error {
	witness.sourceSetDigest = knowledgeSourceSetDigest(knowledgeSources)
	var matches []KnowledgeHome
	for _, source := range knowledgeSources {
		if source.HomeProjectID == witness.sourceProject {
			matches = append(matches, source)
		}
	}
	switch len(matches) {
	case 0:
		return newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "source_id "+witness.sourceProject+" does not name the work's Product knowledge home", false, "declare the law reading through the Product's designated home")
	case 1:
	default:
		return newAmbiguousScopeFailure(workerDispatchReadingResolutionOp, "source_id "+witness.sourceProject+" matches several knowledge homes", "designate one canonical knowledge home", knowledgeHomeCandidateIDs(matches))
	}
	home := matches[0]
	witness.sourceLocator = home.HomeLocatorID
	witness.sourceRepo = home.RepoPath
	if err := q.QueryRowContext(ctx, `SELECT created_at, updated_at FROM project_locators WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&witness.locatorCreatedAt, &witness.locatorUpdatedAt); err != nil {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the knowledge source's canonical locator", true, "retry once the Project locator is readable", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, home.HomeProjectID).Scan(&witness.projectVersion); err != nil {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the knowledge source Project's version", true, "retry once the Project projection is readable", err)
	}
	return sampleKnowledgeReadingRevision(ctx, q, home, witness)
}

func sampleKnowledgeReadingRevision(ctx context.Context, q queryer, home KnowledgeHome, witness *dispatchReadingWitness) error {
	subject, subjectErr := resolveWorkflowLawSubjectTx(ctx, q, home.HomeProjectID, home.HomeLocatorID, witness.lawID)
	if subjectErr != nil {
		return subjectErr
	}
	if subject.hash != witness.contentHash {
		return newFailure(KindProjectionConflict, workerDispatchReadingResolutionOp, "the knowledge home's projected law revision "+witness.lawID+" does not carry the pinned content hash", false, "declare the reading at the revision the home projects, or refresh the reading through the existing route")
	}
	var row struct {
		notePath, commitOID, contentHash string
		recordType, title, completedAt   string
		outcomeTag, lessonTags, summary  string
		scopeMode, manifestSchemaVersion string
	}
	scanErr := q.QueryRowContext(ctx, `SELECT note_path, commit_oid, content_hash, type, title, completed_at, outcome_tag, lesson_tags, summary, scope_mode, manifest_schema_version FROM archived_work WHERE home_project_id=? AND home_locator_id=? AND id=?`, home.HomeProjectID, home.HomeLocatorID, witness.lawID).Scan(
		&row.notePath, &row.commitOID, &row.contentHash, &row.recordType, &row.title, &row.completedAt, &row.outcomeTag, &row.lessonTags, &row.summary, &row.scopeMode, &row.manifestSchemaVersion)
	if scanErr == sql.ErrNoRows || scanErr == nil && row.contentHash != witness.contentHash {
		return newFailure(KindProjectionNotFound, workerDispatchReadingResolutionOp, "the pinned law revision's canonical locator is absent from the git-derived projection", false, "declare reachable pinned repository content or refresh the explicit reading through the existing route")
	}
	if scanErr != nil {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the law revision's canonical locator", true, "retry once the git-derived knowledge projection is readable", scanErr)
	}
	witness.recordPath = row.notePath
	witness.recordCommit = row.commitOID
	var watermark struct {
		commit   sql.NullString
		digest   sql.NullString
		version  int64
		complete bool
	}
	watermarkErr := q.QueryRowContext(ctx, `SELECT scanned_commit_oid, scanned_content_digest, projection_version, complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref='HEAD'`, home.HomeProjectID, home.HomeLocatorID).Scan(&watermark.commit, &watermark.digest, &watermark.version, &watermark.complete)
	if watermarkErr == nil {
		witness.watermarkCommit = watermark.commit.String
		witness.watermarkDigest = watermark.digest.String
		witness.watermarkVersion = watermark.version
		witness.watermarkComplete = watermark.complete
	} else if watermarkErr != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, workerDispatchReadingResolutionOp, "cannot read the knowledge source watermark", true, "retry once the knowledge projection is readable", watermarkErr)
	}
	record, recordErr := knowledgeProjectedRecordTx(ctx, q, witness.lawID, row.recordType, row.outcomeTag, row.completedAt, row.title, row.summary, row.contentHash, row.scopeMode, row.lessonTags, home.HomeProjectID, home.HomeLocatorID, row.notePath)
	if recordErr != nil {
		return recordErr
	}
	witness.projectedRecord = &record
	return nil
}

// uniqueProjectRepositoryLocator resolves one Project's canonical repository
// locator. Exactly one canonical-path locator makes the repository
// deterministic; zero or several refuse.
func uniqueProjectRepositoryLocator(ctx context.Context, q queryer, projectID string) (string, string, error) {
	candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
	if err != nil {
		return "", "", err
	}
	switch len(candidates) {
	case 0:
		return "", "", newFailure(KindUnknownScope, workerDispatchReadingResolutionOp, "Project "+projectID+" has no canonical repository locator", false, "register exactly one canonical-path locator for the declared Project")
	case 1:
		return candidates[0].HomeLocatorID, candidates[0].RepoPath, nil
	default:
		return "", "", newAmbiguousScopeFailure(workerDispatchReadingResolutionOp, "Project "+projectID+" has several canonical repository locators", "leave exactly one canonical-path locator on the declared Project", knowledgeHomeCandidateIDs(candidates))
	}
}

// resolveDispatchReadingWitnesses resolves every declared reading's Git
// proof after the read snapshot closed. One fixed deadline bounds the whole
// phase across all readings and all arms; a failed command or the deadline
// refuses with a typed failure that names the ordinal and source identity.
// It mutates only the sealed witnesses' resolved flags.
func resolveDispatchReadingWitnesses(ctx context.Context, runner GitRunner, witnesses []dispatchReadingWitness) error {
	phase, cancel := context.WithTimeout(ctx, workerDispatchReadingResolutionDeadline)
	defer cancel()
	for i := range witnesses {
		witness := &witnesses[i]
		var err error
		switch witness.arm {
		case WorkContextSourceRepositoryFile:
			err = resolveRepositoryReading(phase, runner, witness)
		case WorkContextSourceKnowledge:
			_, err = resolveKnowledgeReading(phase, runner, witness)
		case WorkContextSourceDomainShape:
			_, err = resolveDomainShapeReading(phase, runner, witness)
		default:
			err = newFailure(KindInvalidPayload, workerDispatchReadingResolutionOp, "declares an unknown reading source kind", false, "use the closed reading source union")
		}
		if err == nil {
			witness.resolved = true
			continue
		}
		if phase.Err() != nil {
			return readingResolutionFailure(witness.ordinal, witness.identity, wrapFailure(KindTimeout, workerDispatchReadingResolutionOp, "the declared-reading resolution phase exceeded its fixed shared deadline", true, "retry the dispatch once the declared sources answer", phase.Err()))
		}
		return readingResolutionFailure(witness.ordinal, witness.identity, err)
	}
	return nil
}

// resolveRepositoryReading proves one declared repository reading: the exact
// raw commit resolves as a commit object in the declared Project's
// repository, and the declared path resolves as one ordinary blob whose
// object the repository holds locally — the availability check reads no blob
// bytes and never lazily fetches. The checkout is never read: a pinned blob
// stays valid whatever the working tree later does.
func resolveRepositoryReading(ctx context.Context, runner GitRunner, witness *dispatchReadingWitness) error {
	if _, err := runner.Run(ctx, witness.repoPath, "rev-parse", "--git-dir"); err != nil {
		return wrapFailure(KindGitUnreachable, workerDispatchReadingResolutionOp, "the declared Project's repository is unreachable", true, "retry the dispatch once the declared repository is reachable", err)
	}
	kind, err := runner.Run(ctx, witness.repoPath, "cat-file", "-t", witness.commitOID)
	if err != nil {
		return newFailure(KindProjectionNotFound, workerDispatchReadingResolutionOp, "the declared commit does not resolve in the declared Project's repository", false, "declare a commit the Project's repository holds")
	}
	if strings.TrimSpace(string(kind)) != "commit" {
		return newFailure(KindProjectionNotFound, workerDispatchReadingResolutionOp, "the declared commit OID is not a commit object", false, "declare one raw commit OID of the Project's repository")
	}
	entry, entryErr := gitTreeEntryRunner(ctx, runner, witness.repoPath, witness.commitOID, witness.path)
	if entryErr != nil {
		return entryErr
	}
	if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" {
		return newFailure(KindInvalidPayload, workerDispatchReadingResolutionOp, "the declared path is not an ordinary blob at the declared commit", false, "declare a regular file (100644 or 100755) at the pinned commit")
	}
	if _, err := runner.Run(ctx, witness.repoPath, "cat-file", "-e", witness.commitOID+":"+witness.path); err != nil {
		return newFailure(KindProjectionNotFound, workerDispatchReadingResolutionOp, "the declared blob object is not locally available at the declared commit", false, "declare a source whose exact blob the repository holds locally")
	}
	return nil
}

// resolveKnowledgeReading proves one declared knowledge reading through the
// existing committed-record proof owner: the committed manifest at the
// canonical locator's commit declares the projected record, the projection
// agrees with the declaration, and the declared blob's bytes hash to the
// pinned revision. The proof reads only locally committed content; it never
// consults the current checkout, never rescans history, and never repins the
// declared hash.
func resolveKnowledgeReading(ctx context.Context, runner GitRunner, witness *dispatchReadingWitness) ([]byte, error) {
	if witness.projectedRecord == nil {
		return nil, newFailure(KindInvariantViolation, workerDispatchReadingResolutionOp, "the sampled knowledge witness carries no projected record", false, "prepare the dispatch through the sealed resolution owner")
	}
	return verifyManifestRecordRunner(ctx, runner, witness.sourceRepo, witness.recordCommit, *witness.projectedRecord)
}

func readDispatchReadingContent(ctx context.Context, runner GitRunner, witness dispatchReadingWitness) ([]byte, error) {
	var content []byte
	var err error
	switch witness.arm {
	case WorkContextSourceRepositoryFile:
		err = resolveRepositoryReading(ctx, runner, &witness)
		if err == nil {
			content, err = runner.Run(ctx, witness.repoPath, "cat-file", "blob", witness.commitOID+":"+witness.path)
		}
	case WorkContextSourceKnowledge:
		content, err = resolveKnowledgeReading(ctx, runner, &witness)
	case WorkContextSourceDomainShape:
		content, err = resolveDomainShapeReading(ctx, runner, &witness)
	default:
		err = newFailure(KindInvalidPayload, workerDispatchReadingResolutionOp, "declares an unknown reading source kind", false, "use the closed reading source union")
	}
	if err != nil {
		return nil, readingResolutionFailure(witness.ordinal, witness.identity, err)
	}
	return content, nil
}

// admitWorkerDispatchReadingResolutionTx is the transactional half of the
// reading resolution owner. It revalidates the sealed witnesses against
// current state with SQL only: the declared reading set and every sampled
// source identity must be unchanged, and every prepared reading must carry
// its Git resolution. Any change refuses before the dispatch completion, so
// a changed source can never borrow an earlier proof.
func admitWorkerDispatchReadingResolutionTx(ctx context.Context, tx *sql.Tx, workID string, admittedContext *WorkContextView) error {
	proof := workerDispatchPreparationFrom(ctx)
	var readings []WorkContextReading
	if admittedContext != nil {
		readings = admittedContext.RequiredReading
	}
	if len(proof.proof.readings) != len(readings) {
		return newFailure(KindProjectionConflict, workerDispatchReadingResolutionOp, "the declared reading set changed under the dispatch preparation", true, "prepare the same dispatch again against the current work context")
	}
	if len(readings) == 0 {
		return nil
	}
	sampled, err := sampleDispatchReadingIdentities(ctx, tx, workID, readings)
	if err != nil {
		return err
	}
	for i := range proof.proof.readings {
		sealed := proof.proof.readings[i]
		if !sealed.resolved {
			return newFailure(KindInvariantViolation, workerDispatchReadingResolutionOp, fmt.Sprintf("required reading %d (%s) carries no resolution from its preparation", sealed.ordinal, sealed.identity), false, "prepare the dispatch through the sealed resolution owner")
		}
		if !sealed.sampledEqual(sampled[i]) {
			return newFailure(KindProjectionConflict, workerDispatchReadingResolutionOp, fmt.Sprintf("required reading %d (%s) changed its source identity under the dispatch preparation", sealed.ordinal, sealed.identity), true, "prepare the same dispatch again against the current source registrations")
		}
	}
	return nil
}
