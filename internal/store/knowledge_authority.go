package store

// CD-0159 D5 supplies standing to the closed default cohort. The Go store
// embeds the same asset the Python manifest readers use. Other legislative
// claims resolve through an exact approved contract in the caller's transaction.

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
)

//go:embed knowledge_authority_defaults.v1.json
var knowledgeAuthorityDefaultsFS embed.FS

// authorityAssetSchema is the closed schema constant of the committed cohort
// asset.
const authorityAssetSchema = "knowledge-authority-defaults.v1"

// authorityAssetSHAFormat bounds the application snapshot's git object name.
var authorityAssetSHAFormat = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

const authorityAssetMaxID = 256

// knowledgeAuthorityDefaults is the parsed committed cohort: one vehicle and
// its frozen (record_id, kind) members.
type knowledgeAuthorityDefaults struct {
	applicationSHA         string
	vehicleWorkID          string
	vehicleContractVersion int64
	members                map[knowledgeAuthorityMember]bool
}

type knowledgeAuthorityMember struct {
	RecordID string
	Kind     string
}

var (
	authorityDefaultsOnce sync.Once
	authorityDefaults     *knowledgeAuthorityDefaults
	authorityDefaultsErr  error
)

// knowledgeAuthorityDefaultDocument mirrors the asset's closed shape. The
// decoder refuses unknown keys, so a root, vehicle, or member field outside
// the closed set fails the parse.
type knowledgeAuthorityDefaultDocument struct {
	Schema         string `json:"schema"`
	ApplicationSHA string `json:"application_sha"`
	Vehicle        struct {
		WorkID          string `json:"work_id"`
		ContractVersion int64  `json:"contract_version"`
	} `json:"vehicle"`
	Members []struct {
		RecordID string `json:"record_id"`
		Kind     string `json:"kind"`
	} `json:"members"`
}

// knowledgeAuthorityDefaultsAsset parses the embedded asset bytes. Every
// defect raises; nothing skips. The rules mirror
// scripts/knowledge_authority_defaults.py exactly, so the two loaders refuse
// the same closed shape.
func knowledgeAuthorityDefaultsAsset(raw []byte) (*knowledgeAuthorityDefaults, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document knowledgeAuthorityDefaultDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("knowledge authority defaults: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("knowledge authority defaults: asset carries trailing JSON data")
	}
	if document.Schema != authorityAssetSchema {
		return nil, fmt.Errorf("knowledge authority defaults: asset schema is not %s", authorityAssetSchema)
	}
	if !authorityAssetSHAFormat.MatchString(document.ApplicationSHA) {
		return nil, fmt.Errorf("knowledge authority defaults: asset application_sha is not a git object name")
	}
	if !criterionWorkIDPattern.MatchString(document.Vehicle.WorkID) {
		return nil, fmt.Errorf("knowledge authority defaults: asset vehicle work_id is not a work- prefixed id")
	}
	if document.Vehicle.ContractVersion < 1 {
		return nil, fmt.Errorf("knowledge authority defaults: asset vehicle contract_version is not a positive integer")
	}
	if len(document.Members) == 0 {
		return nil, fmt.Errorf("knowledge authority defaults: asset members is not a non-empty array")
	}
	members := make(map[knowledgeAuthorityMember]bool, len(document.Members))
	previous := knowledgeAuthorityMember{}
	first := true
	for _, member := range document.Members {
		entry := knowledgeAuthorityMember{RecordID: member.RecordID, Kind: member.Kind}
		for _, value := range []string{entry.RecordID, entry.Kind} {
			if len(value) < 1 || len(value) > authorityAssetMaxID || value != strings.TrimSpace(value) {
				return nil, fmt.Errorf("knowledge authority defaults: asset member field is empty, oversized, or not clean")
			}
		}
		if !first {
			if entry == previous {
				return nil, fmt.Errorf("knowledge authority defaults: asset members carry a duplicate (record_id, kind)")
			}
			if !knowledgeAuthorityMemberLess(previous, entry) {
				return nil, fmt.Errorf("knowledge authority defaults: asset members are not sorted by (record_id, kind)")
			}
		}
		members[entry] = true
		previous, first = entry, false
	}
	return &knowledgeAuthorityDefaults{
		applicationSHA:         document.ApplicationSHA,
		vehicleWorkID:          document.Vehicle.WorkID,
		vehicleContractVersion: document.Vehicle.ContractVersion,
		members:                members,
	}, nil
}

// knowledgeAuthorityMemberLess orders members byte-wise by (record_id, kind),
// the sort the committed asset carries.
func knowledgeAuthorityMemberLess(a, b knowledgeAuthorityMember) bool {
	if a.RecordID != b.RecordID {
		return a.RecordID < b.RecordID
	}
	return a.Kind < b.Kind
}

// committedKnowledgeAuthorityDefaults parses the embedded asset once per
// process. A defect stays an error on every later call: the asset is never
// partially trusted.
func committedKnowledgeAuthorityDefaults() (*knowledgeAuthorityDefaults, error) {
	authorityDefaultsOnce.Do(func() {
		raw, err := knowledgeAuthorityDefaultsFS.ReadFile("knowledge_authority_defaults.v1.json")
		if err != nil {
			authorityDefaultsErr = fmt.Errorf("knowledge authority defaults: %w", err)
			return
		}
		authorityDefaults, authorityDefaultsErr = knowledgeAuthorityDefaultsAsset(raw)
	})
	return authorityDefaults, authorityDefaultsErr
}

// validateKnowledgeAuthorityDefault enforces the frozen default-cohort rule
// for a legislated record: a declared vehicle reference must name a frozen
// (record_id, kind) member at exactly the asset's contract version. Any other
// declared reference carries no cohort obligation here; its approval is the
// resolver's alone. A corrupt asset refuses strictly — never skips.
func validateKnowledgeAuthorityDefault(record KnowledgeRecord) error {
	defaults, err := committedKnowledgeAuthorityDefaults()
	if err != nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "knowledge authority defaults are unavailable: "+err.Error(), false, "repair the committed cohort asset")
	}
	if record.Authority.LegislatedBy != defaults.vehicleWorkID {
		return nil
	}
	member := knowledgeAuthorityMember{RecordID: record.ID, Kind: record.Kind}
	if record.Authority.ContractVersion == defaults.vehicleContractVersion && defaults.members[member] {
		return nil
	}
	if defaults.members[member] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest",
			fmt.Sprintf("vehicle claim for %s must carry contract_version %d", record.ID, defaults.vehicleContractVersion),
			false, "declare the frozen vehicle contract version or leave the default cohort's law to D5")
	}
	return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest",
		fmt.Sprintf("vehicle claim for %s (%s) is outside the frozen legislated-by-default cohort", record.ID, record.Kind),
		false, "declare a work-backed legislative reference or restore the record to the default cohort")
}

// isKnowledgeAuthorityDefault reports whether the record's declared reference
// is the frozen vehicle claim D5 stands behind. It is the resolver's D5 basis
// check: cohort members resolve without any contract lookup.
func (d *knowledgeAuthorityDefaults) isKnowledgeAuthorityDefault(record KnowledgeRecord) bool {
	if record.Authority.LegislatedBy != d.vehicleWorkID || record.Authority.ContractVersion != d.vehicleContractVersion {
		return false
	}
	return d.members[knowledgeAuthorityMember{RecordID: record.ID, Kind: record.Kind}]
}

// resolveKnowledgeLegislatedAuthorityTx resolves one manifest record's
// declared legislative reference inside the caller's transaction. D5 resolves
// first: a frozen default-cohort member at the vehicle version carries its
// standing from D5 itself and performs no contract lookup. Every other
// declared reference must name a live work whose exact historical contract
// row is approved and whose pinned definition is Product-changing and admits
// the record through one closed field.
//
// Absence of the named work is unverified, not negative: the caller keeps the
// record's declared tier. A held negative — a missing or unapproved exact
// version, a non-Product-changing contract, or no admitted reference — is a
// KindInvalidNoteProof refusal naming the law, the work, and the version.
// Database failures and corrupt pins stay their own typed errors.
func resolveKnowledgeLegislatedAuthorityTx(ctx context.Context, tx *sql.Tx, record KnowledgeRecord) (bool, error) {
	if record.Authority.Tier != "legislated" {
		return false, nil
	}
	defaults, err := committedKnowledgeAuthorityDefaults()
	if err != nil {
		return false, newFailure(KindInvalidNoteProof, "knowledge_authority", "knowledge authority defaults are unavailable: "+err.Error(), false, "repair the committed cohort asset")
	}
	if defaults.isKnowledgeAuthorityDefault(record) {
		return false, nil
	}
	workID, version := record.Authority.LegislatedBy, record.Authority.ContractVersion
	heldNegative := func(detail string) error {
		return newFailure(KindInvalidNoteProof, "knowledge_authority",
			fmt.Sprintf("law %s declares work %s contract v%d: %s", record.ID, workID, version, detail),
			false, "approve the exact declared contract for this law, or correct the record's declared reference")
	}
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = ?)`, workID).Scan(&live); err != nil {
		return false, wrapFailure(KindUnavailable, "knowledge_authority", "cannot probe the legislating work "+workID, true, "retry once the database is readable", err)
	}
	if !live {
		return true, nil
	}
	pins, err := workflowContractDefinitionPins(ctx, tx, workID, version)
	if err != nil {
		return false, err
	}
	pin, ok := pins[version]
	if !ok || pin.definitionRef == "" || !validDigest(pin.definitionDigest) {
		return false, heldNegative("declares no approved contract at the exact version")
	}
	var approvedAt, approvedBy, lawModifies string
	err = tx.QueryRowContext(ctx, `SELECT approved_at, approved_by, law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, version).Scan(&approvedAt, &approvedBy, &lawModifies)
	if err == sql.ErrNoRows {
		return false, heldNegative("declares no approved contract at the exact version")
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "knowledge_authority", "cannot read the declared workflow contract", true, "retry once the database is readable", err)
	}
	if approvedAt == "" || approvedBy == "" {
		return false, heldNegative("the exact contract row carries no recorded approval")
	}
	registered, err := verifyReadWorkflowDefinition(WorkflowReadDefinition{Ref: pin.definitionRef, Digest: pin.definitionDigest})
	if err != nil {
		return false, err
	}
	if registered.Definition.ChangesProductTruth == nil || !*registered.Definition.ChangesProductTruth {
		return false, heldNegative("the approving contract is not Product-changing")
	}
	predicates, err := readWorkflowContractPredicates(ctx, tx, workID, version)
	if err != nil {
		return false, err
	}
	admitted := false
	for _, predicate := range predicates {
		decoded, decodeErr := DecodeWorkflowPredicate([]byte(predicate.OutcomePayload))
		if decodeErr != nil {
			return false, decodeErr
		}
		if (decoded.Kind == PredicateExists || decoded.Kind == PredicateAbsent) && slices.Contains(decoded.Subjects, record.ID) {
			admitted = true
		}
	}
	if !admitted && (record.Kind == "decision" || record.Kind == "constitution") {
		var modifies []string
		if json.Unmarshal([]byte(lawModifies), &modifies) != nil {
			return false, newFailure(KindInvariantViolation, "knowledge_authority", "the declared contract's law_modifies projection is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if slices.Contains(modifies, record.ID) {
			admitted = true
		}
		if !admitted {
			additions, additionsErr := workflowContractLawAdditionIDs(ctx, tx, workID, version)
			if additionsErr != nil {
				return false, additionsErr
			}
			if slices.Contains(additions, record.ID) {
				admitted = true
			}
		}
	}
	if !admitted {
		return false, heldNegative("names the law in no admitted legislative field")
	}
	return false, nil
}

// workflowContractLawAdditionIDs reads the contract's persisted architecture
// law additions for the record kinds whose field set admits them.
func workflowContractLawAdditionIDs(ctx context.Context, tx *sql.Tx, workID string, version int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT law_id FROM workflow_contract_law_additions WHERE work_id=? AND contract_version=? ORDER BY law_id`, workID, version)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_authority", "cannot read the contract's law additions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "knowledge_authority", "cannot decode the contract's law additions", true, "retry once the database is readable", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
