package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// CompactionBoundary selects whether a compaction link revalidates law and
// architecture before folding. The zero value runs the guard, so a caller that
// says nothing is protected and an exemption must be written explicitly.
type CompactionBoundary int

const (
	// CompactionBoundaryRevalidated runs the CD-0041 D7 boundary check in the
	// transaction that folds the link.
	CompactionBoundaryRevalidated CompactionBoundary = iota
	// CompactionBoundaryRecoveryExempt skips that check. Reconciling an
	// orphaned note is the closed recovery choice for a pending compaction, so
	// guarding it would refuse the only way out of that condition.
	CompactionBoundaryRecoveryExempt
)

// CompactionLinkRequest is the proof-backed PM6 linkage input. The helper
// verifies the committed blob before constructing the domain event.
type CompactionLinkRequest struct {
	EventID         string
	WorkID          string
	ExpectedVersion int64
	Actor           string
	OccurredAt      time.Time
	Home            KnowledgeHome
	CommitOID       string
	NotePath        string
	ExpectedHash    string
	Reason          string
	Boundary        CompactionBoundary
}

type compactionLinkPayload struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	CompletedAt      string   `json:"completed_at"`
	OutcomeTag       string   `json:"outcome_tag"`
	LessonTags       []string `json:"lesson_tags"`
	TerminalState    string   `json:"terminal_state"`
	Priority         int64    `json:"priority"`
	Summary          string   `json:"summary"`
	SuccessorID      string   `json:"successor_work_id,omitempty"`
	ProductIDs       []string `json:"product_ids"`
	ProjectIDs       []string `json:"project_ids"`
	DomainIDs        []string `json:"domain_ids"`
	TagIDs           []string `json:"tag_ids"`
	HomeProjectID    string   `json:"home_project_id"`
	HomeLocatorID    string   `json:"home_locator_id"`
	NotePath         string   `json:"note_path"`
	CommitOID        string   `json:"commit_oid"`
	ContentHash      string   `json:"content_hash"`
	Reason           string   `json:"reason"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
}

// compactionLinkPayloadV1 is kept separate from the current payload so that
// replay can normalize legacy bytes without ever rewriting the event log.
type compactionLinkPayloadV1 struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	CompletedAt      string   `json:"completed_at"`
	OutcomeTag       string   `json:"outcome_tag"`
	LessonTags       []string `json:"lesson_tags"`
	TerminalState    string   `json:"terminal_state"`
	Priority         int64    `json:"priority"`
	Summary          string   `json:"summary"`
	SuccessorID      string   `json:"successor_work_id,omitempty"`
	ProductIDs       []string `json:"product_ids"`
	ProjectIDs       []string `json:"project_ids"`
	ComponentIDs     []string `json:"component_ids"`
	TagIDs           []string `json:"tag_ids"`
	HomeProjectID    string   `json:"home_project_id"`
	HomeLocatorID    string   `json:"home_locator_id"`
	NotePath         string   `json:"note_path"`
	CommitOID        string   `json:"commit_oid"`
	ContentHash      string   `json:"content_hash"`
	Reason           string   `json:"reason"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
}

// PublishCompactionLink verifies the committed note before appending the link
// event and archived projection in the same SQLite transaction.
func PublishCompactionLink(ctx context.Context, s *Store, req CompactionLinkRequest) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "publish_compaction_link", "store is not open", false, "open a store before publishing a compaction link")
	}
	if req.WorkID == "" || req.EventID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedVersion < 1 || req.Reason == "" {
		return newFailure(KindInvalidOperation, "publish_compaction_link", "compaction link is missing bounded operation fields", false, "supply work, event, actor, time, version, and reason")
	}
	commit := req.CommitOID
	if commit == "" {
		var err error
		commit, err = resolveKnowledgeHead(ctx, req.Home)
		if err != nil {
			return err
		}
	}
	note, err := VerifyCommittedNote(ctx, req.Home.RepoPath, commit, req.NotePath, req.ExpectedHash)
	if err != nil {
		return err
	}
	if note.Kind != "work_note" || note.ID != req.WorkID {
		return newFailure(KindInvalidNoteProof, "publish_compaction_link", "verified note identity does not match the work", false, "publish the canonical work note for the requested work ID")
	}
	var lifecycle string
	if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id = ?`, req.WorkID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnknownScope, "publish_compaction_link", "work item does not exist", false, "publish a note for a known live work item")
		}
		return wrapFailure(KindUnavailable, "publish_compaction_link", "cannot inspect work lifecycle", true, "retry once the database is readable", err)
	}
	if lifecycle != "completed" && lifecycle != "cancelled" && lifecycle != "superseded" {
		return newFailure(KindInvalidOperation, "publish_compaction_link", "work item is not terminal", false, "transition the work to a terminal state before linking its note")
	}
	if note.TerminalState != lifecycle {
		return newFailure(KindInvalidNoteProof, "publish_compaction_link", "note terminal_state does not match live work", false, "publish a note whose terminal state matches the work projection")
	}
	var blockedConsumer string
	err = s.db.QueryRowContext(ctx, `SELECT c.consumer_work_id FROM active_research_consumers c JOIN active_research_packs p ON p.pack_id=c.pack_id JOIN work_items w ON w.id=c.consumer_work_id WHERE p.owner_work_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, req.WorkID).Scan(&blockedConsumer)
	if err == nil {
		return newFailure(KindResearchConsumerBlocked, "publish_compaction_link", "required active consumer remains bound: "+blockedConsumer, false, "unbind, rebind, or terminalize every required active consumer")
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "publish_compaction_link", "cannot inspect required research consumers", true, "retry once the database is readable", err)
	}
	var existing struct{ homeProject, homeLocator, notePath, commitOID, contentHash string }
	err = s.db.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash FROM archived_work WHERE id = ?`, req.WorkID).Scan(&existing.homeProject, &existing.homeLocator, &existing.notePath, &existing.commitOID, &existing.contentHash)
	if err == nil {
		if existing.homeProject == req.Home.HomeProjectID && existing.homeLocator == req.Home.HomeLocatorID && existing.notePath == note.NotePath && existing.commitOID == note.CommitOID && existing.contentHash == note.ContentHash {
			return cleanupTerminalResearch(ctx, s, req.WorkID)
		}
		return newFailure(KindCompactionConflict, "publish_compaction_link", "work already has a different canonical locator", false, "resolve the competing canonical note before retrying")
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "publish_compaction_link", "cannot inspect existing compaction linkage", true, "retry once the database is readable", err)
	}
	scopeDomainIDs := append([]string{}, note.DomainIDs...)
	if len(scopeDomainIDs) == 0 && note.HasComponentIDs {
		scopeDomainIDs = append([]string{}, note.ComponentIDs...)
	}
	payload := compactionLinkPayload{
		ID: note.ID, Type: note.Kind, Title: note.Title, CompletedAt: note.CompletedAt,
		OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState,
		Priority: note.Priority, Summary: note.Summary, SuccessorID: note.SuccessorID,
		ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, DomainIDs: scopeDomainIDs,
		TagIDs: note.TagIDs, HomeProjectID: req.Home.HomeProjectID, HomeLocatorID: req.Home.HomeLocatorID,
		NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash,
		Reason: req.Reason, ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "publish_compaction_link", "cannot encode the bounded compaction payload", false, "repair the note metadata", err)
	}
	operation := Operation{
		Events:           []Event{{EventID: req.EventID, Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 2, Payload: payloadBytes}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion},
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return publishCompactionLinkTx(ctx, tx, req.Boundary, req.WorkID, operation)
	}); err != nil {
		return err
	}
	return cleanupTerminalResearch(ctx, s, req.WorkID)
}

// publishCompactionLinkTx runs the CD-0041 D7 boundary check and the link fold
// in the caller's transaction, so a law cutover cannot land between them.
//
// The subject of a compaction link is terminal, so the Domain-overlap half of
// the check has an empty footprint by construction and the law-revision half is
// what can refuse here. That refusal keeps output authorized under a superseded
// law revision out of Product truth when the cutover commits while the external
// git write is still in flight.
func publishCompactionLinkTx(ctx context.Context, tx *Transaction, boundary CompactionBoundary, workID string, operation Operation) error {
	if boundary == CompactionBoundaryRevalidated {
		if err := CheckWorkflowConsequentialBoundaryTx(ctx, tx, workID); err != nil {
			return err
		}
	}
	_, err := ApplyOperationTx(ctx, tx, operation)
	return err
}

func foldCompactionLinkPublished(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload compactionLinkPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.ID == "" || payload.ID != event.SubjectID || payload.Type != "work_note" || payload.Title == "" || payload.CompletedAt == "" || payload.OutcomeTag == "" || payload.Summary == "" || payload.HomeProjectID == "" || payload.HomeLocatorID == "" || payload.NotePath == "" || payload.CommitOID == "" || payload.ContentHash == "" || payload.Reason == "" || payload.ExpectedVersion < 1 || payload.ResultingVersion != payload.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "compaction_link.published payload is incomplete", false, "supply the complete verified PM6 linkage payload")
	}
	if err := validateCompactionScopeArrays(payload); err != nil {
		return err
	}
	if err := validateCommitOID(payload.CommitOID); err != nil {
		return err
	}
	if err := validateNotePath(payload.NotePath); err != nil {
		return err
	}
	if err := validateContentHash(payload.ContentHash); err != nil {
		return err
	}
	if payload.TerminalState != "completed" && payload.TerminalState != "cancelled" && payload.TerminalState != "superseded" {
		return newFailure(KindInvalidPayload, "fold_event", "compaction link has an invalid terminal state", false, "use a PM4 terminal state")
	}
	var blockedConsumer string
	err := tx.QueryRowContext(ctx, `SELECT c.consumer_work_id FROM active_research_consumers c JOIN active_research_packs p ON p.pack_id=c.pack_id JOIN work_items w ON w.id=c.consumer_work_id WHERE p.owner_work_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, payload.ID).Scan(&blockedConsumer)
	if err == nil {
		return newFailure(KindResearchConsumerBlocked, "fold_event", "required active consumer remains bound: "+blockedConsumer, false, "unbind, rebind, or terminalize every required active consumer")
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect required research consumers", true, "retry once the database is readable", err)
	}
	var existing struct {
		HomeProjectID string
		HomeLocatorID string
		NotePath      string
		CommitOID     string
		ContentHash   string
	}
	err = tx.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash FROM archived_work WHERE id = ?`, payload.ID).Scan(
		&existing.HomeProjectID, &existing.HomeLocatorID, &existing.NotePath, &existing.CommitOID, &existing.ContentHash)
	if err == nil {
		if existing.HomeProjectID != payload.HomeProjectID || existing.HomeLocatorID != payload.HomeLocatorID || existing.NotePath != payload.NotePath || existing.CommitOID != payload.CommitOID || existing.ContentHash != payload.ContentHash {
			return newFailure(KindCompactionConflict, "fold_event", "work already has a different canonical locator", false, "resolve the competing canonical note before retrying")
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect the archived work projection", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,successor_work_id,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, payload.ID, payload.Type, payload.Title, payload.CompletedAt, payload.OutcomeTag, marshalStrings(payload.LessonTags), payload.TerminalState, payload.Priority, payload.Summary, nullString(payload.SuccessorID), payload.HomeProjectID, payload.HomeLocatorID, payload.NotePath, payload.CommitOID, payload.ContentHash); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write archived work projection", true, "retry once the database is writable", err)
	}
	for table, values := range map[string][]string{"archived_work_products": payload.ProductIDs, "archived_work_projects": payload.ProjectIDs, "archived_work_domains": payload.DomainIDs, "archived_work_tags": payload.TagIDs} {
		column := map[string]string{"archived_work_products": "product_id", "archived_work_projects": "project_id", "archived_work_domains": "domain_id", "archived_work_tags": "tag_id"}[table]
		for _, value := range values {
			if value == "" {
				return newFailure(KindInvalidPayload, "fold_event", "compaction scope contains an empty ID", false, "supply non-empty frozen scope IDs")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (work_id, "+column+") VALUES (?, ?)", payload.ID, value); err != nil {
				return wrapFailure(KindUnavailable, "fold_event", "cannot write archived scope projection", true, "retry once the database is writable", err)
			}
		}
	}
	return nil
}

func validateCompactionScopeArrays(payload compactionLinkPayload) error {
	arrays := []struct {
		name   string
		values []string
	}{
		{"lesson_tags", payload.LessonTags},
		{"product_ids", payload.ProductIDs},
		{"project_ids", payload.ProjectIDs},
		{"domain_ids", payload.DomainIDs},
		{"tag_ids", payload.TagIDs},
	}
	for _, array := range arrays {
		if array.values == nil || len(array.values) > maxManifestArray {
			return newFailure(KindInvalidPayload, "fold_event", array.name+" must be an explicit bounded array", false, "supply at most 64 unique non-empty values")
		}
		seen := make(map[string]struct{}, len(array.values))
		for _, value := range array.values {
			if value == "" || utf8.RuneCountInString(value) > maxManifestID || strings.TrimSpace(value) != value {
				return newFailure(KindInvalidPayload, "fold_event", array.name+" contains an invalid value", false, "supply bounded clean values")
			}
			if _, duplicate := seen[value]; duplicate {
				return newFailure(KindInvalidPayload, "fold_event", array.name+" contains a duplicate value", false, "supply unique values")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

// upcastCompactionLinkPublishedV1 converts only the in-memory event passed to
// the fold. The stored v1 payload remains byte-for-byte historical evidence.
func upcastCompactionLinkPublishedV1(event Event) (Event, error) {
	if event.PayloadVersion != 1 {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "compaction_link.published upcaster requires payload version 1", false, "supply a legacy v1 compaction payload")
	}
	if err := rejectDuplicateJSONKeys(event.Payload); err != nil {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "compaction payload contains duplicate keys", false, "repair the stored event payload")
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	if err := decoder.Decode(&fields); err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event", "compaction payload is not an object", false, "repair the stored event payload", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "legacy compaction payload contains trailing JSON", false, "store exactly one compaction payload object")
	}
	if _, ok := fields["domain_ids"]; ok {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "legacy compaction payload already contains domain_ids", false, "remove mixed legacy and Domain vocabulary")
	}
	var legacy compactionLinkPayloadV1
	strict := json.NewDecoder(strings.NewReader(string(event.Payload)))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&legacy); err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event", "legacy compaction payload contains unknown or malformed fields", false, "repair the stored event payload", err)
	}
	current := compactionLinkPayload{
		ID: legacy.ID, Type: legacy.Type, Title: legacy.Title, CompletedAt: legacy.CompletedAt,
		OutcomeTag: legacy.OutcomeTag, LessonTags: legacy.LessonTags, TerminalState: legacy.TerminalState,
		Priority: legacy.Priority, Summary: legacy.Summary, SuccessorID: legacy.SuccessorID,
		ProductIDs: legacy.ProductIDs, ProjectIDs: legacy.ProjectIDs, DomainIDs: append([]string{}, legacy.ComponentIDs...),
		TagIDs: legacy.TagIDs, HomeProjectID: legacy.HomeProjectID, HomeLocatorID: legacy.HomeLocatorID,
		NotePath: legacy.NotePath, CommitOID: legacy.CommitOID, ContentHash: legacy.ContentHash,
		Reason: legacy.Reason, ExpectedVersion: legacy.ExpectedVersion, ResultingVersion: legacy.ResultingVersion,
	}
	payload, err := json.Marshal(current)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event", "cannot encode current compaction payload", false, "repair the stored event payload", err)
	}
	event.Payload, event.PayloadVersion = payload, 2
	return event, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type domainProjection struct {
	ProductID        string
	ProductKey       string
	RegistryHash     string
	RootDomainID     string
	Domains          []domainProjectionDomain
	Relations        []domainProjectionRelation
	LawHomes         map[string]string
	LawApplicability map[string][]string
}

type domainProjectionDomain struct {
	ID             string
	Name           string
	Purpose        string
	Status         string
	ParentDomainID string
}

type domainProjectionRelation struct {
	SourceDomainID  string
	Kind            string
	TargetDomainID  string
	State           string
	GoverningLawIDs []string
}

func prepareDomainProjection(ctx context.Context, s *Store, home KnowledgeHome, manifest KnowledgeManifest) (domainProjection, error) {
	registry := manifest.DomainRegistry
	result := domainProjection{ProductKey: registry.ProductKey, RegistryHash: domainRegistryContentHash(registry), RootDomainID: registry.RootDomainID}
	if len(registry.Domains) == 0 {
		return result, newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "Domain registry contains no Domains", false, "declare the Product root Domain and its retained Domains")
	}
	seen := map[string]bool{}
	for _, domain := range registry.Domains {
		if domain.DomainID == "" {
			return result, newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "Domain registry contains an empty Domain ID", false, "declare stable non-empty Domain IDs")
		}
		if seen[domain.DomainID] {
			return result, newFailure(KindKnowledgeAmbiguous, "rebuild_knowledge_index", "Domain registry contains duplicate Domain IDs", false, "declare each Domain ID once")
		}
		seen[domain.DomainID] = true
		result.Domains = append(result.Domains, domainProjectionDomain{ID: domain.DomainID, Name: domain.Name, Purpose: domain.Purpose, Status: domain.Status, ParentDomainID: domain.ParentDomainID})
		for _, relation := range domain.ArchitectureRelations {
			result.Relations = append(result.Relations, domainProjectionRelation{SourceDomainID: domain.DomainID, Kind: relation.Kind, TargetDomainID: relation.TargetDomainID, State: relation.State, GoverningLawIDs: uniqueSorted(relation.GoverningLawIDs)})
		}
	}
	if !seen[result.RootDomainID] {
		return result, newFailure(KindUnknownScope, "rebuild_knowledge_index", "Domain registry root Domain is unknown", false, "declare the Product root Domain in the registry")
	}
	if result.ProductKey == "" {
		return result, newFailure(KindUnknownScope, "rebuild_knowledge_index", "Domain registry does not identify its Product", false, "declare the owning Product in the registry")
	}
	var products []string
	rows, err := s.db.QueryContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id=? AND locator_id=? ORDER BY product_id`, home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot resolve the Product knowledge home", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var product string
		if err := rows.Scan(&product); err != nil {
			rows.Close()
			return result, err
		}
		products = append(products, product)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if len(products) != 1 {
		return result, newFailure(KindKnowledgeAmbiguous, "rebuild_knowledge_index", "Domain registry home must resolve exactly one Product", false, "assign exactly one Product knowledge home before rebuilding")
	}
	result.ProductID = products[0]
	result.LawHomes, result.LawApplicability = map[string]string{}, map[string][]string{}
	for _, record := range manifest.Records {
		if record.Kind != "decision" && record.Kind != "spec" {
			continue
		}
		homeDomain := record.HomeDomainID
		applies := append([]string(nil), record.AppliesToDomainIDs...)
		if homeDomain == "" {
			continue
		}
		if !seen[homeDomain] {
			return result, newFailure(KindUnknownScope, "rebuild_knowledge_index", "law home Domain is unknown: "+homeDomain, false, "assign each current law to a Domain in the same Product registry")
		}
		result.LawHomes[record.ID] = homeDomain
		result.LawApplicability[record.ID] = uniqueSorted(applies)
	}
	return result, nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func insertDomainProjection(ctx context.Context, tx *sql.Tx, home KnowledgeHome, commit string, projection domainProjection) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_registries(home_project_id,home_locator_id,product_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, projection.ProductID, projection.ProductKey, projection.RootDomainID, "1.0", projection.RegistryHash, commit); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write Domain registry projection", true, "retry once the database is writable", err)
	}
	for _, domain := range projection.Domains {
		status := domain.Status
		if status == "" {
			status = "current"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, projection.ProductID, domain.ID, domain.Name, domain.Purpose, nullString(domain.ParentDomainID), status, projection.RegistryHash, commit); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write Domain projection", true, "retry once the database is writable", err)
		}
	}
	for _, relation := range projection.Relations {
		state := relation.State
		if state == "" {
			state = "active"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,state,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, projection.ProductID, relation.SourceDomainID, relation.Kind, relation.TargetDomainID, state, projection.RegistryHash, commit); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write Domain architecture relation", true, "retry once the database is writable", err)
		}
		for _, lawID := range relation.GoverningLawIDs {
			result, err := tx.ExecContext(ctx, `INSERT INTO domain_relation_governing_laws(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,law_id,law_content_hash) SELECT ?,?,?,?,?,?,?,content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, home.HomeProjectID, home.HomeLocatorID, projection.ProductID, relation.SourceDomainID, relation.Kind, relation.TargetDomainID, lawID, home.HomeProjectID, home.HomeLocatorID, lawID)
			if err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write Domain relation governing law", true, "retry once the database is writable", err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 1 {
				return newFailure(KindInvariantViolation, "rebuild_knowledge_index", "Domain relation governing law did not resolve exactly one accepted projection", false, "rebuild from a manifest with one accepted governing law")
			}
		}
	}
	return nil
}

// RebuildKnowledgeIndex replaces only the git-derived tables for one home.
// RebuildFromLog intentionally does not call this method.
func (s *Store) RebuildKnowledgeIndex(ctx context.Context, home KnowledgeHome) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "rebuild_knowledge_index", "store is not open", false, "open a store before rebuilding the knowledge index")
	}
	commit, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		return err
	}
	paths, err := scanKnowledgeTree(ctx, home, commit)
	if err != nil {
		return err
	}
	linked, err := linkedCompactionWorkIDs(ctx, s.db)
	if err != nil {
		return err
	}
	notes := make([]VerifiedNote, 0, len(paths))
	laws := make([]KnowledgeRecord, 0)
	seen := map[string]bool{}
	seenPaths := map[string]bool{}
	for _, notePath := range paths {
		note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, notePath, "")
		if err != nil {
			return err
		}
		if seen[note.ID] {
			return newFailure(KindKnowledgeAmbiguous, "rebuild_knowledge_index", "two valid canonical notes claim the same stable ID", false, "resolve duplicate canonical note identities")
		}
		seen[note.ID], seenPaths[note.NotePath] = true, true
		if linked[note.ID] {
			notes = append(notes, note)
		}
	}
	manifest, manifestMissing, err := readKnowledgeManifest(ctx, home.RepoPath, commit)
	if err != nil {
		return err
	}
	if !manifestMissing {
		for _, record := range manifest.Records {
			if seen[record.ID] {
				return newFailure(KindKnowledgeAmbiguous, "rebuild_knowledge_index", "manifest and work-note populations claim the same stable ID", false, "assign distinct stable IDs across knowledge populations")
			}
			if seenPaths[record.Path] {
				return newFailure(KindKnowledgeAmbiguous, "rebuild_knowledge_index", "manifest and work-note populations claim the same canonical path", false, "assign distinct canonical paths across knowledge populations")
			}
			if err := verifyManifestBlob(ctx, home.RepoPath, commit, record); err != nil {
				return err
			}
			seen[record.ID], seenPaths[record.Path] = true, true
			notes = append(notes, manifestRecordNote(record, commit, manifest.SchemaVersion))
			if record.Kind == "decision" || record.Kind == "spec" {
				laws = append(laws, record)
			}
		}
	}
	var domainProjectionData domainProjection
	if !manifestMissing {
		domainProjectionData, err = prepareDomainProjection(ctx, s, home, manifest)
		if err != nil {
			return err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot begin knowledge index rebuild", true, "retry once the database is writable", err)
	}
	if err := rebuildKnowledgeIndexTx(ctx, tx, home, commit, notes, laws, manifestMissing, manifest, domainProjectionData); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot commit the knowledge index rebuild", true, "retry once the database is writable", err)
	}
	return nil
}

func rebuildKnowledgeIndexTx(ctx context.Context, tx *sql.Tx, home KnowledgeHome, commit string, notes []VerifiedNote, laws []KnowledgeRecord, manifestMissing bool, manifest KnowledgeManifest, domainProjectionData domainProjection) error {
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	for _, table := range []string{"archived_work_products", "archived_work_projects", "archived_work_components", "archived_work_domains", "archived_work_tags", "archived_work"} {
		deleteSQL := "DELETE FROM " + table + " WHERE home_project_id = ? AND home_locator_id = ?"
		if table != "archived_work" {
			deleteSQL = "DELETE FROM " + table + " WHERE work_id IN (SELECT id FROM archived_work WHERE home_project_id = ? AND home_locator_id = ?)"
		}
		if _, err := tx.ExecContext(ctx, deleteSQL, home.HomeProjectID, home.HomeLocatorID); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot clear git-derived "+table, true, "retry once the database is writable", err)
		}
	}
	for _, table := range []string{"law_relations", "law_subjects"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE home_project_id=? AND home_locator_id=?", home.HomeProjectID, home.HomeLocatorID); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot clear git-derived "+table, true, "retry once the database is writable", err)
		}
	}
	for _, table := range []string{"domain_relation_governing_laws", "law_domain_applicability", "law_domain_homes", "domain_architecture_relations", "domains", "domain_registries"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE home_project_id=? AND home_locator_id=?", home.HomeProjectID, home.HomeLocatorID); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot clear Domain projection "+table, true, "retry once the database is writable", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot clear the git knowledge watermark", true, "retry once the database is writable", err)
	}
	for _, note := range notes {
		if err := insertKnowledgeNote(ctx, tx, home, note); err != nil {
			return err
		}
	}
	for _, law := range laws {
		if err := insertLawSubject(ctx, tx, home, law, commit); err != nil {
			return err
		}
	}
	if !manifestMissing {
		if err := insertDomainProjection(ctx, tx, home, commit, domainProjectionData); err != nil {
			return err
		}
		for _, law := range laws {
			homeDomain, hasHome := domainProjectionData.LawHomes[law.ID]
			if !hasHome {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES(?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, law.ID, domainProjectionData.ProductID, homeDomain, law.SHA256, commit, law.ProductWideRationale); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write law Domain home", true, "retry once the database is writable", err)
			}
			for _, domainID := range domainProjectionData.LawApplicability[law.ID] {
				if _, err := tx.ExecContext(ctx, `INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, law.ID, domainProjectionData.ProductID, domainID, commit); err != nil {
					return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write law Domain applicability", true, "retry once the database is writable", err)
				}
			}
		}
	}
	for _, law := range laws {
		for _, relation := range law.LawRelations {
			source, target := law.ID, relation.TargetID
			if relation.Kind == "conflicts_with" && source > target {
				source, target = target, source
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, source, relation.Kind, target, commit); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot insert a derived law relation", true, "retry once the database is writable", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_kind_coverage WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot clear knowledge kind coverage", true, "retry once the database is writable", err)
	}
	coverage := map[string]string{
		"work_note": "indexed",
		"lesson":    "supported_not_indexed",
		"decision":  "supported_not_indexed",
		"spec":      "supported_not_indexed",
		"research":  "supported_not_indexed",
	}
	if !manifestMissing {
		for _, kind := range manifest.IndexedKinds {
			coverage[kind] = "indexed"
		}
	}
	for _, kind := range []string{"work_note", "lesson", "decision", "spec", "research"} {
		reason := "manifest absent at scanned commit"
		if kind == "work_note" {
			reason = "canonical docs/work directory scanned"
		} else if !manifestMissing && coverage[kind] == "indexed" {
			reason = "manifest indexed kind at scanned commit"
		} else if kind == "research" {
			reason = "research has no accepted canonical manifest form"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage (home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES (?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef, kind, coverage[kind], reason, commit); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write knowledge kind coverage", true, "retry once the database is writable", err)
		}
	}
	// The commit identity is the deterministic observation value. It avoids
	// rebuild-only wall-clock churn while the query result still exposes the
	// current observation time in its transient envelope.
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_index_watermark (home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES (?,?,?,?,?,1)`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef, commit, commit); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write the knowledge watermark", true, "retry once the database is writable", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return nil
}

func insertLawSubject(ctx context.Context, tx *sql.Tx, home KnowledgeHome, record KnowledgeRecord, commit string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, record.ID, record.Kind, record.Status, record.Path, record.Title, record.SHA256, commit); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot insert a derived law subject", true, "retry once the database is writable", err)
	}
	return nil
}

func linkedCompactionWorkIDs(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT subject_id FROM domain_events WHERE kind = 'compaction_link.published'`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot read compaction links", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot read a compaction link", true, "retry once the database is readable", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

func insertKnowledgeNote(ctx context.Context, tx *sql.Tx, home KnowledgeHome, note VerifiedNote) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,successor_work_id,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, note.ID, note.Kind, note.Title, note.CompletedAt, note.OutcomeTag, marshalStrings(note.LessonTags), note.TerminalState, note.Priority, note.Summary, nullString(note.SuccessorID), home.HomeProjectID, home.HomeLocatorID, note.NotePath, note.CommitOID, note.ContentHash, note.ScopeMode, note.ManifestSchemaVersion); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot insert an indexed note", true, "retry once the database is writable", err)
	}
	scopeTable, scopeColumn, scopeIDs := "archived_work_components", "component_id", note.ComponentIDs
	if note.HasDomainIDs || len(note.DomainIDs) > 0 {
		scopeTable, scopeColumn, scopeIDs = "archived_work_domains", "domain_id", note.DomainIDs
	}
	for table, values := range map[string][]string{"archived_work_products": note.ProductIDs, "archived_work_projects": note.ProjectIDs, scopeTable: scopeIDs, "archived_work_tags": note.TagIDs} {
		column := map[string]string{"archived_work_products": "product_id", "archived_work_projects": "project_id", "archived_work_tags": "tag_id", scopeTable: scopeColumn}[table]
		for _, value := range values {
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (work_id, "+column+") VALUES (?, ?)", note.ID, value); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot insert indexed note scope", true, "retry once the database is writable", err)
			}
		}
	}
	return nil
}

func manifestRecordNote(record KnowledgeRecord, commit, schemaVersion string) VerifiedNote {
	terminal := "completed"
	if record.Status == "superseded" {
		terminal = "superseded"
	}
	return VerifiedNote{
		ID: record.ID, Kind: record.Kind, Title: record.Title, CompletedAt: record.Date,
		OutcomeTag: record.Status, LessonTags: append([]string(nil), record.Tags...),
		TerminalState: terminal, Summary: record.Summary, SuccessorID: record.Successor,
		ProductIDs: append([]string(nil), record.Scopes.ProductIDs...), ProjectIDs: append([]string(nil), record.Scopes.ProjectIDs...),
		DomainIDs: append([]string(nil), record.Scopes.DomainIDs...), HasDomainIDs: true, TagIDs: append([]string(nil), record.Scopes.TagIDs...),
		ScopeMode: record.Scopes.Mode, ManifestSchemaVersion: schemaVersion, NotePath: record.Path, CommitOID: commit, ContentHash: record.SHA256,
	}
}

func verifyManifestBlob(ctx context.Context, repo, commit string, record KnowledgeRecord) error {
	entry, err := gitTreeEntry(ctx, repo, commit, record.Path)
	if err != nil || entry.kind != "blob" || entry.mode != "100644" {
		return newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "manifest record blob is missing or not regular: "+record.Path, false, "restore the referenced regular markdown blob")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+record.Path)
	if err != nil {
		return wrapFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "cannot read manifest record blob: "+record.Path, true, "restore the git object and retry", err)
	}
	sum := sha256.Sum256(content)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != record.SHA256 {
		return newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "manifest record hash does not match blob: "+record.Path, false, "recompute the authored sha256 proof")
	}
	return nil
}

func readWatermark(ctx context.Context, db *sql.DB, home KnowledgeHome, current string) (string, bool, error) {
	var scanned string
	var complete bool
	err := db.QueryRowContext(ctx, `SELECT scanned_commit_oid, complete FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&scanned, &complete)
	if err == sql.ErrNoRows {
		return scanned, false, nil
	}
	if err != nil {
		return "", false, wrapFailure(KindUnavailable, "knowledge_index", "cannot read the knowledge watermark", true, "retry once the database is readable", err)
	}
	return scanned, complete && scanned == current, nil
}

func validateKnowledgeHomeForQuery(ctx context.Context, s *Store, home KnowledgeHome, allowDegraded bool, op string) (string, string, error) {
	current, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		if allowDegraded {
			return "unreachable", "degraded", nil
		}
		return "", "", newFailure(KindUnreachable, op, "git knowledge authority is unreachable", true, "restore the git home and retry")
	}
	watermark, complete, err := readWatermark(ctx, s.db, home, current)
	if err != nil {
		return "", "", err
	}
	if !complete {
		if allowDegraded {
			return watermark, "degraded", nil
		}
		return "", "", newFailure(KindIndexDegraded, op, "knowledge index watermark is stale or incomplete", true, "rebuild the git-derived knowledge index")
	}
	return watermark, "authoritative", nil
}

func validateKnowledgeCoverage(ctx context.Context, s *Store, home KnowledgeHome, commit string, kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,coverage,scanned_commit_oid FROM knowledge_kind_coverage WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef)
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q9", "cannot read knowledge kind coverage", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var kind, coverage, scanned string
		if err := rows.Scan(&kind, &coverage, &scanned); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q9", "cannot decode knowledge kind coverage", true, "retry once the database is readable", err)
		}
		if coverage == "indexed" && scanned == commit {
			available[kind] = true
		}
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q9", "cannot finish reading knowledge kind coverage", true, "retry once the database is readable", err)
	}
	missing := make([]string, 0)
	for _, kind := range kinds {
		if !available[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		failure := newFailure(KindKnowledgeUnavailable, "PM1.Q9", "explicitly requested knowledge kinds are unavailable: "+strings.Join(missing, ","), false, "publish and rebuild the canonical kind, or remove it from the filter")
		failure.UnavailableKinds = missing
		failure.CandidateIDs = append([]string(nil), missing...)
		return failure
	}
	return nil
}

// knowledgeCoverageOmissions takes a queryer so the launcher search can pass
// its own open read transaction rather than reaching back through the pool.
func knowledgeCoverageOmissions(ctx context.Context, db queryer, home KnowledgeHome, commit string) []string {
	rows, err := db.QueryContext(ctx, `SELECT kind FROM knowledge_kind_coverage WHERE home_project_id=? AND home_locator_id=? AND head_ref=? AND scanned_commit_oid=? AND coverage='supported_not_indexed' ORDER BY kind`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef, commit)
	if err != nil {
		return []string{"knowledge_kind_coverage_unavailable"}
	}
	defer rows.Close()
	omissions := make([]string, 0)
	for rows.Next() {
		var kind string
		if rows.Scan(&kind) == nil {
			omissions = append(omissions, "knowledge_kind_not_indexed:"+kind)
		}
	}
	return omissions
}

func orderedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func knowledgeWatermarkMeta(queryID string, home KnowledgeHome, watermark, authority string, observedAt time.Time) ResultMeta {
	return ResultMeta{QueryID: queryID, ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: ""}, SourceVersionWatermark: 0, Authority: authority, Freshness: Freshness{ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), Age: 0, Stale: authority != "authoritative"}, OrderingKeys: []string{"structured_match", "completed_at_desc", "id"}, NextCursor: nil, Omissions: []string{}, Warnings: []string{fmt.Sprintf("index_watermark:%s/%s/%s=%s", home.HomeProjectID, home.HomeLocatorID, home.HeadRef, watermark)}}
}
