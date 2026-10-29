package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

// readNeighborCase is one independent store read entry point. absent, when
// set, is the typed failure the seeded absence must return; success, a busy
// refusal, an unavailable wrap, or any other kind fails the case.
type readNeighborCase struct {
	name string
	// absent, when set, is the typed failure the seeded absence must return:
	// TerminalWorkVersion on a live work, a removal audit with no receipt,
	// and so on. Success, a busy refusal, an unavailable wrap, or any other
	// kind fails the case.
	absent FailureKind
	// scratch marks reads whose remaining completion cost is an independent
	// scratch artifact: Backup writes a scratch snapshot file. The live read still precedes
	// a writer release; the wall-clock budget covers only the live read, so
	// the scratch tail is exempt from it.
	scratch bool
	run     func() error
}

// Every surviving independent read entry point runs beside a neighbor child
// process in both deterministic modes: a reader holding a WAL snapshot and a
// writer holding BEGIN IMMEDIATE for neighbor.WriterHold. A fresh neighbor
// per row and a 50ms busy timeout distinguish a deferred read from a read
// that queues behind the write lock: success must precede the writer's
// release, never follow a busy-timeout retry.
func TestReadEntryPointsWithNeighbor(t *testing.T) {
	ctx := context.Background()
	s := seedQueryFixture(t)
	seedResearchWork(t, s, "neighbor-research")
	actor, version := continuityTestWorkflow(t, s, "neighbor-work")
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	insertInvestigationGateObservation(t, s, "neighbor-work", "obs:"+strings.Repeat("3", 16), []string{"root", "blocker"})
	pack := createSimplePack(t, s, "neighbor-pack", "neighbor-research")
	if _, err := ClaimStepAuthorized(ctx, s, testClaim("neighbor-fence", "neighbor-fence-key"), nil); err != nil {
		t.Fatal(err)
	}
	neighborClient := TrustedClientRecord{ClientRef: "neighbor-client", Status: "active", PrincipalRef: "neighbor-principal", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}
	neighborKey := TrustedClientKeyRecord{ClientRef: "neighbor-client", KeyID: "neighbor-key", PublicKey: make([]byte, 32), Status: "active"}
	if err := s.RegisterTrustedClient(ctx, neighborClient, neighborKey, "2026-08-09T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	knowledgeRepo, knowledgeHome := seedNeighborKnowledgeHome(t, s)
	toolRepo := seedNeighborToolRepo(t)
	if err := s.AddProjectLocator(ctx, "proj", ProjectLocator{ID: "proj-canonical", Kind: LocatorCanonicalPath, Value: toolRepo}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginWrite(ctx, tx.tx)
		if err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('blocker','neighbor-issue','SYN-1','https://example.invalid/issues/SYN-1','2026-08-09','2026-08-09')`); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var watermark int64
	if err := s.db.QueryRow("SELECT max(effect_seq) FROM effect_receipts").Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	backupDestination := filepath.Join(t.TempDir(), "neighbor-backup.db")
	proposalPayload := []byte(`{"problem":"A bounded problem.","affected":["The store."],"stakes":"The committed proposal survives.","user_outcomes":["A durable proposal."]}`)
	preflightRequest := WorkflowActionPreflightRequest{WorkID: "neighbor-work", ExpectedVersion: version, ActionID: "record_proposal", Payload: proposalPayload, Actor: actor}
	if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
		t.Fatal(err)
	}
	rows := []readNeighborCase{
		// PM1 product-memory queries.
		{name: "PM1.Q1", run: func() error { _, err := s.QueryQ1(ctx, Q1Request{Product: "prod"}); return err }},
		{name: "PM1.Q2", run: func() error { _, err := s.QueryQ2(ctx, Q2Request{Product: "prod"}); return err }},
		{name: "PM1.Q3", run: func() error { _, err := s.QueryQ3(ctx, Q3Request{Product: "prod"}); return err }},
		{name: "PM1.Q4", run: func() error { _, err := s.QueryQ4(ctx, Q4Request{Product: "prod"}); return err }},
		{name: "PM1.Q5", run: func() error { _, err := s.QueryQ5(ctx, Q5Request{Product: "prod"}); return err }},
		{name: "PM1.Q6", run: func() error { _, err := s.QueryQ6(ctx, Q6Request{Work: "blocker"}); return err }},
		{name: "PM1.Q7", run: func() error { _, err := s.QueryQ7(ctx, Q7Request{Work: "blocker"}); return err }},
		{name: "PM1.Q8", run: func() error { _, err := s.QueryQ8(ctx, Q8Request{Work: "blocker"}); return err }},
		{name: "QueryQ9", run: func() error {
			result, err := s.QueryQ9(ctx, Q9Request{Text: "neighbor law govern", Kinds: []string{"decision"}, Home: knowledgeHome})
			if err != nil {
				return err
			}
			if len(result.Items) != 1 {
				return fmt.Errorf("items=%d, want 1", len(result.Items))
			}
			return nil
		}},
		{name: "QueryQ10", run: func() error {
			result, err := s.QueryQ10(ctx, Q10Request{Work: "blocked", AllowDegraded: true, Home: knowledgeHome})
			if err != nil {
				return err
			}
			if result.Status != "not_compacted" {
				return fmt.Errorf("status=%q, want not_compacted", result.Status)
			}
			return nil
		}},
		// Work-item reads.
		{name: "ReadWorkItemSummary", run: func() error { _, err := s.ReadWorkItemSummary(ctx, "blocker"); return err }},
		{name: "TrustedClientWithKey", run: func() error {
			client, key, err := s.TrustedClientWithKey(ctx, "neighbor-client")
			if err != nil {
				return err
			}
			if client.ClientRef != "neighbor-client" || key.KeyID != "neighbor-key" {
				return fmt.Errorf("client=%+v key=%+v", client, key)
			}
			return nil
		}},
		{name: "TrustedClient", run: func() error {
			client, err := s.TrustedClient(ctx, "neighbor-client")
			if err != nil {
				return err
			}
			if client.ClientRef != "neighbor-client" || client.PolicyRevision != 1 {
				return fmt.Errorf("client=%+v", client)
			}
			return nil
		}},
		{name: "EntityVersion", run: func() error {
			entityVersion, err := s.EntityVersion(ctx, SubjectProduct, "prod")
			if err != nil {
				return err
			}
			if entityVersion <= 0 {
				return fmt.Errorf("version=%d", entityVersion)
			}
			return nil
		}},
		{name: "ProductMembership", run: func() error {
			projects, err := s.ProductMembership(ctx, "prod")
			if err != nil {
				return err
			}
			if len(projects) != 1 || projects[0] != "proj" {
				return fmt.Errorf("projects=%v", projects)
			}
			return nil
		}},
		{name: "EventIDExists", run: func() error {
			exists, err := s.EventIDExists(ctx, "q-blocks")
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("seeded event q-blocks not found")
			}
			return nil
		}},
		{name: "FirstWorkEventByOtherActor", run: func() error {
			kind, actor, found, err := s.FirstWorkEventByOtherActor(ctx, "blocker", "nobody")
			if err != nil {
				return err
			}
			if !found || kind != "work.created" || actor == "nobody" {
				return fmt.Errorf("kind=%q actor=%q found=%v", kind, actor, found)
			}
			return nil
		}},
		{name: "WorkLifecycle", run: func() error {
			lifecycle, err := s.WorkLifecycle(ctx, "blocker")
			if err != nil {
				return err
			}
			if lifecycle == "" {
				return fmt.Errorf("empty lifecycle")
			}
			return nil
		}},
		{name: "TerminalWorkVersion", absent: KindProjectionNotFound, run: func() error { _, err := s.TerminalWorkVersion(ctx, "blocker"); return err }},
		{name: "PendingOperationForWork", run: func() error {
			operation, err := s.PendingOperationForWork(ctx, "work-1")
			if err != nil {
				return err
			}
			if operation.OperationID != "neighbor-fence" {
				return fmt.Errorf("op=%q, want neighbor-fence", operation.OperationID)
			}
			return nil
		}},
		{name: "AcceptedInputsDigest", run: func() error {
			digest, err := s.AcceptedInputsDigest(ctx, "neighbor-fence")
			if err != nil {
				return err
			}
			if digest == "" {
				return fmt.Errorf("empty accepted inputs digest")
			}
			return nil
		}},
		{name: "LookupMutationIdempotency", run: func() error {
			_, found, err := s.LookupMutationIdempotency(ctx, MutationIdempotencyKey{PrincipalRef: "neighbor-principal", Tool: "execute", OperationKind: "neighbor", IdempotencyKey: "neighbor-absent"})
			if err != nil {
				return err
			}
			if found {
				return fmt.Errorf("unexpected idempotency record")
			}
			return nil
		}},
		{name: "Revision", run: func() error {
			value, err := s.Revision(ctx)
			if err != nil {
				return err
			}
			if value <= 0 {
				return fmt.Errorf("watermark=%d", value)
			}
			return nil
		}},
		{name: "KnowledgeIndexWatermark", run: func() error {
			value, err := s.KnowledgeIndexWatermark(ctx, "home", "neighbor-locator", "HEAD")
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("empty knowledge index watermark")
			}
			return nil
		}},
		{name: "ReadLinearLink", run: func() error {
			link, err := s.ReadLinearLink(ctx, "blocker")
			if err != nil {
				return err
			}
			if link.URL != "https://example.invalid/issues/SYN-1" {
				return fmt.Errorf("url=%q", link.URL)
			}
			return nil
		}},
		{name: "ReadWorkObservations", run: func() error {
			page, err := s.ReadWorkObservations(ctx, WorkObservationsRequest{WorkID: "neighbor-work", Limit: 10})
			if err != nil {
				return err
			}
			if len(page.Observations) != 1 {
				return fmt.Errorf("observations=%d, want 1", len(page.Observations))
			}
			return nil
		}},
		{name: "ExternalObservationsForWork", run: func() error {
			observations, err := s.ExternalObservationsForWork(ctx, "neighbor-work", time.Now(), 10)
			if err != nil {
				return err
			}
			if len(observations) != 0 {
				return fmt.Errorf("observations=%+v, want none", observations)
			}
			return nil
		}},
		{name: "WorktreeEntries", run: func() error {
			entries, err := s.WorktreeEntries(ctx, "blocker")
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("entries=%+v, want none", entries)
			}
			return nil
		}},
		{name: "ResumeWorktreeLocation", absent: KindProjectionNotFound, run: func() error {
			_, err := s.ResumeWorktreeLocation(ctx, "prod", "proj", "blocker")
			return err
		}},
		{name: "LawAdditionReservations", run: func() error {
			reservations, err := s.LawAdditionReservations(ctx, "prod")
			if err != nil {
				return err
			}
			if len(reservations) != 0 {
				return fmt.Errorf("reservations=%+v, want none", reservations)
			}
			return nil
		}},
		{name: "WorktreeOwnerWorkID", run: func() error {
			workID, err := s.WorktreeOwnerWorkID(ctx, "proj", toolRepo)
			if err != nil {
				return err
			}
			if workID != "" {
				return fmt.Errorf("owner=%q, want empty", workID)
			}
			return nil
		}},
		{name: "WorktreeAudit", run: func() error {
			audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "prod", Limit: 20})
			if err != nil {
				return err
			}
			if len(audit.Drift) != 0 {
				return fmt.Errorf("drift=%+v, want none", audit.Drift)
			}
			return nil
		}},
		{name: "EncodeCursorDecodeCursor", run: func() error {
			expected := SignedCursor{Tool: "concord_work_browse", Operation: "list", Scope: "prod", Filter: "kind=task", Detail: "summary", Order: "priority:desc"}
			token, err := s.EncodeCursor(ctx, expected)
			if err != nil {
				return err
			}
			decoded, err := s.DecodeCursor(ctx, token, expected)
			if err != nil {
				return err
			}
			if decoded.Operation != "list" || decoded.Scope != "prod" {
				return fmt.Errorf("decoded=%+v", decoded)
			}
			return nil
		}},

		// Workflow reads.
		{name: "ReadWorkPin", run: func() error { _, err := ReadWorkPin(ctx, s, "neighbor-work"); return err }},
		{name: "ReadWorkClosure", run: func() error { _, err := ReadWorkClosure(ctx, s, "neighbor-work"); return err }},
		{name: "ReadWorkflowContinuity", run: func() error {
			_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: "neighbor-work", Limit: 20})
			return err
		}},
		{name: "ReadWorkPinPendingOperatorDecision", run: func() error { _, err := readOperatorQuestionFromWorkPinForTest(ctx, s, "neighbor-work"); return err }},
		{name: "ReadWorkflowProjection", run: func() error {
			_, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: "neighbor-work"})
			return err
		}},
		{name: "WorkflowProjectionHash", run: func() error {
			hash, err := WorkflowProjectionHash(ctx, s)
			if err != nil {
				return err
			}
			if hash == "" {
				return fmt.Errorf("empty projection hash")
			}
			return nil
		}},
		{name: "ReadWorkflowVerdict", run: func() error {
			verdict, err := ReadWorkflowVerdict(ctx, s, "neighbor-work")
			if err != nil {
				return err
			}
			if verdict != nil {
				return fmt.Errorf("unexpected verdict %+v", verdict)
			}
			return nil
		}},
		{name: "WorkflowExecutingIdentity", run: func() error {
			_, _, found, err := WorkflowExecutingIdentity(ctx, s, "neighbor-work")
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no executing identity")
			}
			return nil
		}},
		{name: "WorkflowActionDefinitionFor", run: func() error {
			_, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), "neighbor-work", "record_proposal")
			return err
		}},
		{name: "VerifyWorkflowInstanceDefinition", run: func() error {
			_, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), "neighbor-work")
			return err
		}},
		{name: "WorkflowActionAvailableWithRegistry", run: func() error {
			available, err := WorkflowActionAvailableWithRegistry(ctx, s, BuiltinWorkflowRegistry(), "neighbor-work")
			if err != nil {
				return err
			}
			if !available {
				return fmt.Errorf("action unavailable")
			}
			return nil
		}},
		{name: "WorkflowActionPreflightWithRegistry", run: func() error {
			return WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), preflightRequest)
		}},
		{name: "requireRecordedInvestigationArtifactSnapshot", run: func() error {
			return requireRecordedInvestigationArtifactSnapshot(ctx, s, "neighbor-work")
		}},
		{name: "ActiveWorkflowContract", absent: KindProjectionNotFound, run: func() error { _, err := s.ActiveWorkflowContract(ctx, "neighbor-work"); return err }},
		{name: "readSnapshot latestWorkflowContractVersion", run: func() error {
			var contractVersion int64
			err := s.readSnapshot(ctx, func(q queryer) error {
				var innerErr error
				contractVersion, innerErr = latestWorkflowContractVersion(ctx, q, "neighbor-work")
				return innerErr
			})
			if err != nil {
				return err
			}
			if contractVersion != 0 {
				return fmt.Errorf("version=%d, want 0", contractVersion)
			}
			return nil
		}},
		{name: "readSnapshot activeWorkflowContractVersions", run: func() error {
			var versions []int64
			err := s.readSnapshot(ctx, func(q queryer) error {
				var innerErr error
				versions, innerErr = activeWorkflowContractVersions(ctx, q, "neighbor-work")
				return innerErr
			})
			if err != nil {
				return err
			}
			if len(versions) != 0 {
				return fmt.Errorf("versions=%v, want none", versions)
			}
			return nil
		}},

		// Fence and scope reads.
		{name: "Step", run: func() error {
			result, err := Step(ctx, s, "neighbor-fence")
			if err != nil {
				return err
			}
			if result.AttemptEpoch != 1 {
				return fmt.Errorf("epoch=%d, want 1", result.AttemptEpoch)
			}
			return nil
		}},

		// Research reads use the canonical pack reader and owner descriptor page.
		{name: "ReadResearchPack", run: func() error {
			_, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 100})
			return err
		}},
		{name: "ResearchPacksByOwner", run: func() error {
			page, err := s.ResearchPacksByOwner(ctx, "neighbor-research", 100, "")
			if err != nil {
				return err
			}
			if len(page.Packs) != 1 {
				return fmt.Errorf("packs=%d, want 1", len(page.Packs))
			}
			return nil
		}},

		// Knowledge-home scope reads.
		{name: "ResolveCompactionHome", run: func() error {
			home, err := s.ResolveCompactionHome(ctx, "blocker")
			if err != nil {
				return err
			}
			if home.RepoPath != toolRepo {
				return fmt.Errorf("home=%+v", home)
			}
			return nil
		}},
		{name: "ResolveKnowledgeQueryHome", run: func() error {
			home, err := s.ResolveKnowledgeQueryHome(ctx, "neighbor-knowledge-product", "", KnowledgeHome{}, "PM1.Q9")
			if err != nil {
				return err
			}
			if home.RepoPath != knowledgeRepo {
				return fmt.Errorf("home=%+v", home)
			}
			return nil
		}},
		{name: "KnowledgeHomeForLocator", run: func() error {
			home, err := s.KnowledgeHomeForLocator(ctx, "home", "neighbor-locator", "")
			if err != nil {
				return err
			}
			if home.RepoPath != knowledgeRepo {
				return fmt.Errorf("home=%+v", home)
			}
			return nil
		}},

		// Project locator and tooling reads.
		{name: "ProjectsForProduct", run: func() error {
			memberships, err := s.ProjectsForProduct(ctx, "prod")
			if err != nil {
				return err
			}
			if len(memberships) != 1 || memberships[0].ID != "proj" {
				return fmt.Errorf("memberships=%+v", memberships)
			}
			return nil
		}},
		{name: "ProjectsForWork", run: func() error {
			memberships, err := s.ProjectsForWork(ctx, "blocker")
			if err != nil {
				return err
			}
			if len(memberships) != 1 {
				return fmt.Errorf("memberships=%+v", memberships)
			}
			return nil
		}},
		{name: "ProjectCanonicalPath", run: func() error {
			path, err := s.ProjectCanonicalPath(ctx, "proj")
			if err != nil {
				return err
			}
			if path != toolRepo {
				return fmt.Errorf("path=%q", path)
			}
			return nil
		}},
		{name: "ProjectLocators", run: func() error {
			locators, err := s.ProjectLocators(ctx, "proj")
			if err != nil {
				return err
			}
			if len(locators) != 1 {
				return fmt.Errorf("locators=%+v", locators)
			}
			return nil
		}},
		{name: "ResolveProject", run: func() error {
			resolution, err := s.ResolveProject(ctx, toolRepo, toolRepo)
			if err != nil {
				return err
			}
			if resolution.ProjectID != "proj" {
				return fmt.Errorf("resolution=%+v", resolution)
			}
			return nil
		}},
		{name: "ResolveProjectWithRunner", run: func() error {
			resolution, err := s.ResolveProjectWithRunner(ctx, toolRepo, toolRepo, ExecGitRunner{})
			if err != nil {
				return err
			}
			if resolution.ProjectID != "proj" {
				return fmt.Errorf("resolution=%+v", resolution)
			}
			return nil
		}},
		{name: "MatchResolvedProjectHost", run: func() error {
			host, err := ResolveProjectHost(ctx, toolRepo, toolRepo, ExecGitRunner{})
			if err != nil {
				return err
			}
			resolution, err := s.MatchResolvedProjectHost(ctx, host)
			if err != nil {
				return err
			}
			if resolution.ProjectID != "proj" {
				return fmt.Errorf("resolution=%+v", resolution)
			}
			return nil
		}},
		{name: "ResolveWorkProjectTooling", run: func() error {
			tooling, err := ResolveWorkProjectTooling(ctx, s, "blocker")
			if err != nil {
				return err
			}
			if tooling == nil || len(tooling.Declared) != 1 {
				return fmt.Errorf("tooling=%+v", tooling)
			}
			return nil
		}},
		{name: "RefineProofManifestRequired", run: func() error {
			required, err := RefineProofManifestRequired(ctx, s, "neighbor-work", "record_delivery")
			if err != nil {
				return err
			}
			if required {
				return fmt.Errorf("refine proof gate active at the proposal step")
			}
			return nil
		}},
		{name: "ScopeVersion", run: func() error {
			digest, _, err := s.ScopeVersion(ctx, "proj")
			if err != nil {
				return err
			}
			if digest == "" {
				return fmt.Errorf("empty scope version")
			}
			return nil
		}},
		{name: "ProductsForWorkIDs", run: func() error {
			products, err := s.ProductsForWorkIDs(ctx, []string{"blocker"})
			if err != nil {
				return err
			}
			if len(products["blocker"]) != 1 || products["blocker"][0] != "prod" {
				return fmt.Errorf("products=%+v", products)
			}
			return nil
		}},
		{name: "ProductsForProjectIDs", run: func() error {
			products, err := s.ProductsForProjectIDs(ctx, []string{"proj"})
			if err != nil {
				return err
			}
			if len(products["proj"]) != 1 || products["proj"][0] != "prod" {
				return fmt.Errorf("products=%+v", products)
			}
			return nil
		}},
		{name: "ProductsForKnowledgeID", run: func() error {
			products, err := s.ProductsForKnowledgeID(ctx, "neighbor-law")
			if err != nil {
				return err
			}
			if len(products) != 0 {
				return fmt.Errorf("products=%v, want none", products)
			}
			return nil
		}},

		// Work-context reads.
		{name: "MessagesForWork", run: func() error {
			messages, err := s.MessagesForWork(ctx, "blocker", 10)
			if err != nil {
				return err
			}
			if len(messages) != 0 {
				return fmt.Errorf("messages=%+v, want none", messages)
			}
			return nil
		}},
		{name: "ResolveContinuityWorkByDirectory", run: func() error {
			resolution, err := s.ResolveContinuityWorkByDirectory(ctx, filepath.Join(toolRepo, "no-such-dir"))
			if err != nil {
				return err
			}
			if resolution.WorkID != "" || resolution.ProjectID != "" {
				return fmt.Errorf("resolution=%+v, want empty", resolution)
			}
			return nil
		}},
		{name: "GoverningRequirementsForProjectIDs", run: func() error {
			refs, err := s.GoverningRequirementsForProjectIDs(ctx, []string{"proj"})
			if err != nil {
				return err
			}
			if len(refs) != 0 {
				return fmt.Errorf("refs=%v, want none", refs)
			}
			return nil
		}},
		{name: "ValidateCaptureGoverningRequirements", run: func() error {
			return s.ValidateCaptureGoverningRequirements(ctx, []string{"prod"}, nil, nil)
		}},

		// Snapshot and launcher reads.
		{name: "readAppliedFromDB", run: func() error { _, _, err := readAppliedFromDB(ctx, s.db, s.Path(), nil); return err }},
		{name: "Backup", scratch: true, run: func() error {
			// The destination persists across modes; each run needs a clean
			// scratch path, which the entry point refuses to overwrite.
			_ = os.Remove(backupDestination)
			_ = os.Remove(manifestPath(backupDestination))
			_, err := Backup(ctx, s, backupDestination)
			return err
		}},
		{name: "BackupWithOptions", scratch: true, run: func() error {
			optioned := backupDestination + "-options"
			_ = os.Remove(optioned)
			_ = os.Remove(manifestPath(optioned))
			_, err := BackupWithOptions(ctx, s, optioned, BackupOptions{SnapshotID: "neighbor-snapshot"})
			return err
		}},
		{name: "BlockedSessions", run: func() error { _, err := s.BlockedSessions(ctx, time.Now(), []string{"prod"}, 100); return err }},
		{name: "ReadSessionClaimedWorktree", run: func() error {
			_, err := ReadSessionClaimedWorktree(ctx, s, "neighbor-work", "proj")
			return err
		}},
		{name: "ReadPendingProjectHandoffForProject", run: func() error {
			_, err := ReadPendingProjectHandoffForProject(ctx, s, "neighbor-work", "proj", "neighbor-session")
			return err
		}},
		{name: "EvaluateProjectSessionRetirement", run: func() error {
			_, err := EvaluateProjectSessionRetirement(ctx, s, "neighbor-work", "proj", "neighbor-session")
			return err
		}},
	}
	for _, mode := range []neighbor.Mode{neighbor.Reader, neighbor.Writer} {
		for _, row := range rows {
			t.Run(string(mode)+"/"+row.name, func(t *testing.T) {
				neighbor.Start(t, s.Path(), mode)
				started := time.Now()
				err := row.run()
				if row.absent != "" {
					err = requireReadNeighborAbsence(row.name, err, row.absent)
				}
				if err != nil {
					t.Fatalf("%s under %s neighbor: %v", row.name, mode, err)
				}
				if mode == neighbor.Writer && !row.scratch {
					if elapsed := time.Since(started); elapsed >= neighbor.WriterHold {
						t.Fatalf("%s waited for writer release: %s", row.name, elapsed)
					}
				}
			})
		}
	}
	// Recovery discovery folds admission even when duplicate contracts prevent
	// selecting one active contract. Its own fixture keeps that ambiguity out
	// of the other read rows.
	for _, mode := range []neighbor.Mode{neighbor.Reader, neighbor.Writer} {
		t.Run(string(mode)+"/WorkflowActionDefinitionForSupersedeContract", func(t *testing.T) {
			f := newAcceptanceRecoveryFixture(ctx, t)
			duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
			if _, err := f.store.db.Exec("PRAGMA busy_timeout=50"); err != nil {
				t.Fatal(err)
			}
			neighbor.Start(t, f.store.Path(), mode)
			started := time.Now()
			_, action, err := WorkflowActionDefinitionFor(ctx, f.store, BuiltinWorkflowRegistry(), f.workID, "supersede_contract")
			if err != nil {
				t.Fatalf("supersede_contract discovery under %s neighbor: %v", mode, err)
			}
			if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
				t.Fatalf("recovery action=%+v, want supersede_contract with required approval", action)
			}
			if mode == neighbor.Writer && time.Since(started) >= neighbor.WriterHold {
				t.Fatal("supersede_contract discovery waited for writer release")
			}
		})
	}
	// The bootstrap-origin validation owns a worktree fixture and its fake
	// git, so it prepares its own store per mode beside the same neighbors.
	for _, mode := range []neighbor.Mode{neighbor.Reader, neighbor.Writer} {
		t.Run(string(mode)+"/ValidateBootstrapOrigin", func(t *testing.T) {
			s, git, _ := worktreeFixture(t)
			claim, err := s.ClaimWorktree(ctx, baseClaim(git))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
				t.Fatal(err)
			}
			neighbor.Start(t, s.Path(), mode)
			if _, err := s.ValidateBootstrapOrigin(ctx, "project-w", claim.Entry.Path, git); err != nil {
				t.Fatal(err)
			}
			// The claimed worktree backs the worktree-tier read surface: the
			// inspection is a read over the claim's entry plus the fake git's
			// porcelain answer, never a lease.
			status, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeStatus, Runner: git})
			if err != nil {
				t.Fatal(err)
			}
			if status.Path != claim.Entry.Path {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

// requireRecordedInvestigationArtifactSnapshot runs the investigation-artifact
// precondition in one read-only transaction, so the Product work-item count and
// the investigation-ref reads see one snapshot (CD-0173 D2).
func requireRecordedInvestigationArtifactSnapshot(ctx context.Context, s *Store, workID string) error {
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot open a consistent investigation snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return requireRecordedInvestigationArtifact(ctx, tx, workID)
}

// requireReadNeighborAbsence accepts only the seeded absence's typed failure.
// A nil error means the fixture drifted; a busy, timeout, or unavailable
// refusal means the read queued behind the write lock.
func requireReadNeighborAbsence(name string, err error, want FailureKind) error {
	if err == nil {
		return fmt.Errorf("%s: expected %s absence, got success", name, want)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != want {
		return fmt.Errorf("%s: expected %s absence, got %w", name, want, err)
	}
	return nil
}

// seedNeighborKnowledgeHome commits a one-record knowledge manifest in a
// scratch git repository, authorizes it as the neighbor Product knowledge
// home, and rebuilds the index so watermark and query reads carry a fresh
// indexed head.
func seedNeighborKnowledgeHome(t *testing.T, s *Store) (string, KnowledgeHome) {
	t.Helper()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo, manifestFixture{
		ID: "neighbor-law", Kind: "decision", Path: ".concord/docs/decisions/CD-9001-neighbor-law.md",
		Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Neighbor law",
		Summary: "Govern the neighbor read fixture", Scopes: homeScope(),
	})
	commitKnowledgeRepo(t, repo, "neighbor read fixture law")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "neighbor-locator", RepoPath: repo, HeadRef: "HEAD"}
	// A replay must restore the home from the log, so seed it the way the
	// production event paths leave it instead of direct SQL rows.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		locatorProductEvent("neighbor-knowledge-product"),
		locatorProjectEvent("home"),
		locatorMembershipEvent("neighbor-knowledge-product", "home"),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProduct, "neighbor-knowledge-product"): 0,
		VersionRef(SubjectProject, "home"):                       0,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "home", ProjectLocator{ID: "neighbor-locator", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: "neighbor-knowledge-product", ProjectID: "home", LocatorID: "neighbor-locator", Reason: "neighbor read fixture", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	return repo, home
}

// seedNeighborToolRepo commits a Project tooling manifest in a scratch git
// repository and pins origin/HEAD, so default-ref tooling resolution and
// project resolution resolve against a reachable repository identity.
func seedNeighborToolRepo(t *testing.T) string {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/tooling.v1.json", `{"schema_version":"1.0","project":"concord","tools":[{"id":"working-tree-check","invocation":"bin/working-tree-check","tier":"fast"}]}`)
	commitKnowledgeRepo(t, repo, "neighbor tooling manifest")
	runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}
