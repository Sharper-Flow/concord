package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
)

// DomainShapeDesignationResult keeps filesystem effects visible even when the
// subsequent configuration transaction fails. Setup does not commit Git files.
type DomainShapeDesignationResult struct {
	ApplyOperationResult
	Setup DomainShapeSetupResult `json:"setup"`
}

type domainShapeDesignationState struct {
	RepoPath string
	SameHome bool
}

func preflightDomainShapeDesignation(ctx context.Context, q queryer, request ProductKnowledgeHomeDesignation) (domainShapeDesignationState, error) {
	var state domainShapeDesignationState
	if request.ProductID == "" || request.ProjectID == "" || request.LocatorID == "" || request.ExpectedVersion < 1 {
		return state, domainShapeSetupError("Product, Project, locator and positive current Product version are required")
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM products WHERE id=?`, request.ProductID).Scan(&version); err != nil {
		return state, err
	}
	if version != request.ExpectedVersion {
		return state, newFailure(KindVersionConflict, "domain_shape_designate", "Product version changed", false, "read the current Product version before retrying")
	}
	if err := verifyKnowledgeHomeEligibility(ctx, q, knowledgeHomePayload{ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID}); err != nil {
		return state, err
	}
	var competing bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_knowledge_homes WHERE project_id=? AND locator_id=? AND product_id<>?)`, request.ProjectID, request.LocatorID, request.ProductID).Scan(&competing); err != nil {
		return state, err
	}
	if competing {
		return state, newFailure(KindProjectionConflict, "domain_shape_designate", "Project locator is already another Product's knowledge home", false, "select an uncontested home")
	}
	if err := q.QueryRowContext(ctx, `SELECT locator_value FROM project_locators WHERE locator_id=? AND project_id=?`, request.LocatorID, request.ProjectID).Scan(&state.RepoPath); err != nil {
		return state, err
	}
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_knowledge_homes WHERE product_id=? AND project_id=? AND locator_id=?)`, request.ProductID, request.ProjectID, request.LocatorID).Scan(&state.SameHome)
	return state, err
}

// DesignateProductDomainShape is operator orchestration, not the replay/config
// route. It preflights before filesystem writes and rechecks inside the event
// transaction. Files and SQLite cannot commit atomically: callers must retain
// Setup on error and serialize setup writers for this home, never undo user files.
// Repeating an already designated identity at its current version adds no event.
func (s *Store) DesignateProductDomainShape(ctx context.Context, request ProductKnowledgeHomeDesignation) (DomainShapeDesignationResult, error) {
	var result DomainShapeDesignationResult
	if s == nil || s.db == nil {
		return result, domainShapeSetupError("store is not open")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return result, err
	}
	state, err := preflightDomainShapeDesignation(ctx, tx, request)
	_ = tx.Rollback()
	if err != nil {
		return result, err
	}
	setupRequest, err := domainShapeOperatorSetupIdentity(state.RepoPath, request.ProductID, request.ProjectID)
	if err != nil {
		return result, err
	}
	plan, err := PrepareDomainShapeSetup(setupRequest)
	if err != nil {
		return result, err
	}
	result.Setup, err = plan.Apply()
	if err != nil {
		return result, fmt.Errorf("Domain Shape setup failed; reported filesystem changes may remain: %w", err)
	}
	event := Event{EventID: operatorEventID("product.knowledge_home_designated", request.ProductID), Kind: "product.knowledge_home_designated", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1}
	err = s.Transact(ctx, func(transaction *Transaction) error {
		return designateDomainShapeTx(ctx, transaction, request, state, event, &result)
	})
	if err != nil {
		result.ApplyOperationResult = ApplyOperationResult{}
		return result, fmt.Errorf("knowledge home designation failed after setup; reported filesystem changes remain and must be reconciled before retry: %w", err)
	}
	return result, nil
}

func designateDomainShapeTx(ctx context.Context, transaction *Transaction, request ProductKnowledgeHomeDesignation, state domainShapeDesignationState, event Event, result *DomainShapeDesignationResult) error {
	tx, err := transactionSQL(transaction, "domain_shape_designate")
	if err != nil {
		return err
	}
	current, err := preflightDomainShapeDesignation(ctx, tx, request)
	if err != nil {
		return err
	}
	if current != state {
		return domainShapeSetupError("knowledge home configuration changed during setup")
	}
	if current.SameHome {
		result.EventIDs = []string{}
		return nil
	}
	reason := request.Reason
	if reason == "" {
		reason = "operator designation"
	}
	event.Payload, err = json.Marshal(knowledgeHomePayload{ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID, Reason: reason, ExpectedVersion: request.ExpectedVersion, ResultingVersion: request.ExpectedVersion + 1})
	if err != nil {
		return err
	}
	result.ApplyOperationResult, err = ApplyOperationTx(ctx, transaction, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
	return err
}

// Repository keys are authored identities, not SQLite IDs. The selected locator
// and competing-home preflight establish ownership; preserve valid existing keys
// rather than renaming an adopting Product to match a database identifier.
func domainShapeOperatorSetupIdentity(repo, product, project string) (DomainShapeSetupRequest, error) {
	request := DomainShapeSetupRequest{RepoPath: repo, ProductKey: product, ProjectKey: project}
	root, err := openDomainShapeSetupRoot(repo)
	if err != nil {
		return request, err
	}
	defer root.Close()
	for _, name := range []string{knowledgeRegistryPath, projectToolingManifestPath} {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return request, err
		}
		if !info.Mode().IsRegular() || info.Size() > 65536 {
			return request, domainShapeSetupError("setup identity must be a bounded regular file")
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return request, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, 65537))
		closeErr := file.Close()
		if readErr != nil {
			return request, readErr
		}
		if closeErr != nil {
			return request, closeErr
		}
		if len(raw) > 65536 {
			return request, domainShapeSetupError("setup identity exceeds byte bound")
		}
		if name == knowledgeRegistryPath {
			var registry KnowledgeDomainRegistry
			if err := decodeStrictKnowledgeJSON(raw, &registry); err != nil {
				return request, err
			}
			if err := validateKnowledgeDomainRegistry(registry); err != nil {
				return request, err
			}
			request.ProductKey = registry.ProductKey
		} else {
			var document map[string]json.RawMessage
			if err := decodeStrictKnowledgeJSON(raw, &document); err != nil {
				return request, err
			}
			if err := json.Unmarshal(document["project"], &request.ProjectKey); err != nil {
				return request, err
			}
		}
	}
	return request, nil
}

// DomainShapeOperatorCheck is an ephemeral operator report, not workflow evidence
// or persisted verification state. The entire JSON report is bounded to 32 KiB.
type DomainShapeOperatorCheck struct {
	Subject       DomainShapeSubject               `json:"subject"`
	ToolingCommit string                           `json:"tooling_commit"`
	ToolingHash   string                           `json:"tooling_hash"`
	RatchetHash   string                           `json:"ratchet_hash"`
	Checks        map[string]DomainShapeEvaluation `json:"checks"`
}

// RunProductDomainShapeChecks uses only the selected Product's designated home.
// Checkout, when supplied, must be a clean exact-subject worktree of that home.
// Commands execute with operator host authority, not an agent capability or a
// sandbox. Callers must exclude concurrent checkout writers during execution.
func (s *Store) RunProductDomainShapeChecks(ctx context.Context, product, checkout string) (result DomainShapeOperatorCheck, err error) {
	result = DomainShapeOperatorCheck{Checks: map[string]DomainShapeEvaluation{}}
	defer func() {
		raw, encodeErr := json.Marshal(result)
		if encodeErr != nil || len(raw) > DomainShapeMaxBytes {
			result = DomainShapeOperatorCheck{}
			err = errors.Join(err, domainShapeSetupError("operator check report exceeds 32768 bytes or cannot be encoded"))
		}
	}()
	home, err := s.ResolveKnowledgeQueryHome(ctx, product, "", KnowledgeHome{}, "domain_shape_check")
	if err != nil || product == "" {
		if err == nil {
			err = domainShapeSetupError("an explicit Product is required")
		}
		return result, err
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		return result, err
	}
	snapshot, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		return result, err
	}
	input, tooling, toolingCommit, toolingHash, ratchetHash, err := prepareDomainShapeOperatorInput(ctx, home, product, snapshot)
	if err != nil {
		return result, err
	}
	result.Subject, result.ToolingCommit, result.ToolingHash, result.RatchetHash = input.Subject, toolingCommit, toolingHash, ratchetHash
	if checkout == "" {
		checkout = home.RepoPath
	}
	if err := verifyDomainShapeCheckout(ctx, home, checkout, snapshot.KnowledgeCommit); err != nil {
		return result, err
	}
	for _, operation := range []string{"boundary", "data_objects", "config_reads", "references"} {
		input.Operation = operation
		if err := verifyDomainShapeCheckout(ctx, home, checkout, snapshot.KnowledgeCommit); err != nil {
			return result, err
		}
		evaluation, err := RunDomainShapeCheck(ctx, ExecDomainShapeCommandRunner{}, checkout, tooling, input)
		if err != nil {
			return result, fmt.Errorf("%s: %w", operation, err)
		}
		result.Checks[operation] = evaluation
		if err := verifyDomainShapeCheckout(ctx, home, checkout, snapshot.KnowledgeCommit); err != nil {
			return result, err
		}
	}
	// A refresh/re-designation racing the command cannot silently qualify a report.
	currentHome, err := s.ResolveKnowledgeQueryHome(ctx, product, "", home, "domain_shape_check")
	if err != nil {
		return result, err
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, currentHome); err != nil {
		return result, err
	}
	current, err := s.ReadDomainShapeSnapshot(ctx, currentHome, product)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(current, snapshot) {
		return result, domainShapeSetupError("Domain Shape snapshot changed during checks")
	}
	_, _, currentCommit, currentHash, currentRatchet, err := prepareDomainShapeOperatorInput(ctx, currentHome, product, current)
	if err != nil {
		return result, err
	}
	if currentCommit != toolingCommit || currentHash != toolingHash || currentRatchet != ratchetHash {
		return result, domainShapeSetupError("pinned check inputs changed during checks")
	}
	for _, evaluation := range result.Checks {
		if evaluation.State == "outstanding" {
			return result, fmt.Errorf("Domain Shape checks found outstanding violations")
		}
	}
	return result, nil
}

func prepareDomainShapeOperatorInput(ctx context.Context, home KnowledgeHome, product string, snapshot DomainShapeSnapshot) (DomainShapeCheckInput, *ProjectToolingManifest, string, string, string, error) {
	input := DomainShapeCheckInput{SchemaVersion: DomainShapeProtocolVersion, Subject: DomainShapeSubject{ProductID: product, ProjectID: home.HomeProjectID, CommitOID: snapshot.KnowledgeCommit, ContentHash: snapshot.ContentHash}, Roots: snapshot.Roots, Facts: snapshot.Facts, DependsOn: []DomainShapeEdge{}, RepositoryAnchors: []DomainFactRepositoryAnchor{}, EnforcementAnchors: []DomainFactEnforcementAnchor{}, RequiredChecks: []string{}, Strict: true}
	commit, hash, checks, err := domainShapeDefaultTooling(ctx, home)
	if err != nil {
		return input, nil, "", "", "", err
	}
	var tooling *ProjectToolingManifest
	if snapshot.ToolingCommit != "" && (snapshot.ToolingCommit != commit || snapshot.ToolingContentHash != hash || !slices.Equal(snapshot.RequiredChecks, checks)) {
		return input, nil, "", "", "", domainShapeSetupError("default tooling no longer matches the projected facts")
	}
	if hash != "" {
		raw, err := runGit(ctx, home.RepoPath, "cat-file", "blob", commit+":"+projectToolingManifestPath)
		if err != nil {
			return input, nil, "", "", "", err
		}
		tooling, err = ParseProjectToolingManifest(raw)
		if err != nil {
			return input, nil, "", "", "", err
		}
		if tooling.DomainShape != nil {
			tooling.DomainShape.RequiredChecks = checks
		}
		if err := validateDomainShapeOperatorInvocations(tooling); err != nil {
			return input, nil, "", "", "", err
		}
	}
	input.RequiredChecks = checks
	registryRaw, err := runGit(ctx, home.RepoPath, "cat-file", "blob", snapshot.KnowledgeCommit+":"+knowledgeRegistryPath)
	if err != nil {
		return input, nil, "", "", "", err
	}
	var registry KnowledgeDomainRegistry
	if err := decodeStrictKnowledgeJSON(registryRaw, &registry); err != nil {
		return input, nil, "", "", "", err
	}
	if domainRegistryContentHash(registry) != snapshot.RegistryContentHash {
		return input, nil, "", "", "", domainShapeSetupError("registry does not match the selected snapshot")
	}
	for _, domain := range registry.Domains {
		if domain.Status != "current" {
			continue
		}
		for _, relation := range domain.ArchitectureRelations {
			if relation.Kind == "depends_on" {
				input.DependsOn = append(input.DependsOn, DomainShapeEdge{SourceDomainID: domain.DomainID, TargetDomainID: relation.TargetDomainID})
			}
		}
	}
	ratchet, err := runGit(ctx, home.RepoPath, "cat-file", "blob", snapshot.KnowledgeCommit+":"+domainShapeRatchetPath)
	if err != nil {
		return input, nil, "", "", "", err
	}
	if err := domainShapeJSON(ratchet, &input.Ratchet); err != nil {
		return input, nil, "", "", "", err
	}
	packages := knowledgePackageMapDocument{SchemaVersion: packageMapShapeVersion, Packages: []knowledgePackageEntry{}}
	for _, root := range input.Roots {
		packages.Packages = append(packages.Packages, knowledgePackageEntry{Path: root.Path, Role: root.Role})
	}
	if err := validateSetupRatchet(ratchet, packages); err != nil {
		return input, nil, "", "", "", err
	}
	collectDomainShapeOperatorAnchors(&input)
	// Bind the graph/facts, strict ratchet and default declaration together. Each
	// operation carries the same digest; the command cannot substitute a subject.
	pinned, err := json.Marshal(struct {
		Input                      DomainShapeCheckInput
		ToolingCommit, ToolingHash string
	}{input, commit, hash})
	if err != nil {
		return input, nil, "", "", "", err
	}
	input.Subject.ContentHash = domainShapeBytesHash(pinned)
	input.Operation = "boundary"
	return input, tooling, commit, hash, domainShapeBytesHash(ratchet), validateDomainShapeInput(input)
}

func collectDomainShapeOperatorAnchors(input *DomainShapeCheckInput) {
	for _, fact := range input.Facts {
		if fact.Invariant != nil {
			for _, anchor := range fact.Invariant.EnforcedBy {
				if !slices.Contains(input.EnforcementAnchors, anchor) {
					input.EnforcementAnchors = append(input.EnforcementAnchors, anchor)
				}
			}
		}
		if fact.ConfigCoupling != nil && !slices.Contains(input.RepositoryAnchors, fact.ConfigCoupling.ReadBy) {
			input.RepositoryAnchors = append(input.RepositoryAnchors, fact.ConfigCoupling.ReadBy)
		}
		if fact.StateAuthority != nil {
			for _, copy := range fact.StateAuthority.DerivedCopies {
				if !slices.Contains(input.RepositoryAnchors, copy.RebuildRoute) {
					input.RepositoryAnchors = append(input.RepositoryAnchors, copy.RebuildRoute)
				}
			}
		}
	}
}

func validateDomainShapeOperatorInvocations(tooling *ProjectToolingManifest) error {
	if tooling.DomainShape == nil {
		return nil
	}
	shape := tooling.DomainShape
	selected := []string{shape.BoundaryCheck, shape.DataObjectExtractor, shape.ConfigExtractor, shape.ReferenceCheck}
	for _, tool := range tooling.Declared {
		if !slices.Contains(selected, tool.ID) {
			continue
		}
		for _, arg := range strings.Fields(tool.Invocation) {
			if _, value, found := strings.Cut(arg, "="); found {
				arg = value
			}
			if filepath.IsAbs(arg) || slices.Contains(strings.Split(arg, "/"), "..") {
				return domainShapeSetupError("check invocation must not select a harness outside the pinned checkout")
			}
		}
	}
	return nil
}

func verifyDomainShapeCheckout(ctx context.Context, home KnowledgeHome, checkout, commit string) error {
	if !filepath.IsAbs(checkout) || filepath.Clean(checkout) != checkout {
		return domainShapeSetupError("check checkout must be an absolute normalized path")
	}
	top, err := runGit(ctx, checkout, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(top)) != checkout {
		return domainShapeSetupError("check directory is not the checkout root")
	}
	common, err := runGit(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	homeCommon, err := runGit(ctx, home.RepoPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(common)) != strings.TrimSpace(string(homeCommon)) {
		return domainShapeSetupError("check checkout belongs to a foreign repository")
	}
	head, err := runGit(ctx, checkout, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != commit {
		return domainShapeSetupError("check checkout does not match the exact pinned subject")
	}
	status, err := runGit(ctx, checkout, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return domainShapeSetupError("check checkout is dirty or contains untracked/ignored files")
	}
	return verifyDomainShapeCheckoutBytes(ctx, checkout, commit)
}

// Status alone is insufficient: assume-unchanged flags and clean/smudge filters
// can hide different harness bytes. Compare raw blobs without invoking filters.
func verifyDomainShapeCheckoutBytes(ctx context.Context, checkout, commit string) error {
	raw, err := runGit(ctx, checkout, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return err
	}
	entries, err := parseTreeEntries(raw)
	if err != nil {
		return err
	}
	files := []treeEntry{}
	for _, entry := range entries {
		if err := verifyDomainShapeCheckoutEntry(ctx, checkout, entry); err != nil {
			return err
		}
		if entry.mode == "100644" || entry.mode == "100755" {
			files = append(files, entry)
		}
	}
	for start := 0; start < len(files); start += 64 {
		if err := verifyDomainShapeBlobBatch(ctx, checkout, files[start:min(start+64, len(files))]); err != nil {
			return err
		}
	}
	return nil
}

func verifyDomainShapeCheckoutEntry(ctx context.Context, checkout string, entry treeEntry) error {
	full := filepath.Join(checkout, entry.path)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, checkout+string(filepath.Separator)) {
		return domainShapeSetupError("checkout source escapes the pinned tree")
	}
	switch entry.mode {
	case "100644", "100755":
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return domainShapeSetupError("checkout blob is not a regular pinned file")
		}
	case "120000":
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		pinned, err := runGit(ctx, checkout, "cat-file", "blob", entry.oid)
		if err != nil {
			return err
		}
		if target != string(pinned) {
			return domainShapeSetupError("checkout symlink differs from the pinned subject")
		}
	default:
		return domainShapeSetupError("checkout contains a gitlink or unsupported entry; checks require a fully materialized pinned tree")
	}
	return nil
}

func verifyDomainShapeBlobBatch(ctx context.Context, checkout string, batch []treeEntry) error {
	argv := []string{"hash-object", "--no-filters", "--"}
	for _, entry := range batch {
		argv = append(argv, entry.path)
	}
	hashes, err := runGit(ctx, checkout, argv...)
	if err != nil {
		return err
	}
	observed := strings.Fields(string(hashes))
	if len(observed) != len(batch) {
		return domainShapeSetupError("cannot verify raw checkout blobs")
	}
	for index, entry := range batch {
		if observed[index] != entry.oid {
			return domainShapeSetupError("raw checkout bytes differ from the pinned subject: " + entry.path)
		}
	}
	return nil
}
