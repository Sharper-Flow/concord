package store

import (
	"context"
	"encoding/base64"
	"sort"
	"strings"
)

// Bounded Product → Domain reads navigate current Domains, their current law,
// and typed architecture relations. Every read resolves the Product's
// designated git home and reads the independent Domain registry and the law
// folder at one explicit commit into an immutable snapshot before answering;
// a Product without a readable committed registry refuses rather than
// returning an empty page.

const (
	domainListDefaultLimit = 20
	domainListMaxLimit     = 100
)

// DomainRegistryView is the authority watermark every Domain read carries: it
// names the committed registry the answer came from, so callers can distinguish
// authoritative-empty (registry present, no matching rows) from absent
// (no registry at all, a typed refusal).
type DomainRegistryView struct {
	ProductID     string `json:"product_id"`
	ProductKey    string `json:"product_key"`
	RootDomainID  string `json:"root_domain_id"`
	ContentHash   string `json:"content_hash"`
	ScannedCommit string `json:"scanned_commit_oid"`
}

type DomainSummary struct {
	DomainID   string `json:"domain_id"`
	Name       string `json:"name"`
	Purpose    string `json:"purpose"`
	ParentID   string `json:"parent_domain_id,omitempty"`
	Status     string `json:"status"`
	HomeDomain bool   `json:"home_domain"`
}

type DomainListRequest struct {
	Product string
	Limit   int
	Cursor  string
}

type DomainListResult struct {
	ResultMeta
	Registry *DomainRegistryView `json:"registry"`
	Domains  []DomainSummary     `json:"domains"`
}

type DomainLawRecord struct {
	LawID         string   `json:"law_id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Path          string   `json:"path"`
	ContentHash   string   `json:"content_hash"`
	AppliesTo     []string `json:"applies_to,omitempty"`
	ScannedCommit string   `json:"scanned_commit_oid"`
}

type DomainRelationView struct {
	Kind          string   `json:"kind"`
	SourceDomain  string   `json:"source_domain_id"`
	TargetDomain  string   `json:"target_domain_id"`
	State         string   `json:"state"`
	GoverningLaws []string `json:"governing_law_ids"`
}

type DomainDetailRequest struct {
	Product string
	Domain  string
}

type DomainDetailResult struct {
	ResultMeta
	Registry     *DomainRegistryView  `json:"registry"`
	Domain       DomainSummary        `json:"domain"`
	CurrentLaw   []DomainLawRecord    `json:"current_law"`
	Relations    []DomainRelationView `json:"relations"`
	Observations []DomainObservation  `json:"observations"`
}

// DomainListPayload excludes ResultMeta's envelope fields from the closed payload.
type DomainListPayload struct {
	Registry *DomainRegistryView `json:"registry"`
	Domains  []DomainSummary     `json:"domains"`
}

func NewDomainListPayload(result DomainListResult) DomainListPayload {
	return DomainListPayload{Registry: result.Registry, Domains: sliceOrEmpty(result.Domains)}
}

type DomainDetailPayload struct {
	Registry     *DomainRegistryView  `json:"registry"`
	Domain       DomainSummary        `json:"domain"`
	CurrentLaw   []DomainLawRecord    `json:"current_law"`
	Relations    []DomainRelationView `json:"relations"`
	Observations []DomainObservation  `json:"observations"`
}

func NewDomainDetailPayload(result DomainDetailResult) DomainDetailPayload {
	return DomainDetailPayload{Registry: result.Registry, Domain: result.Domain, CurrentLaw: sliceOrEmpty(result.CurrentLaw), Relations: sliceOrEmpty(result.Relations), Observations: sliceOrEmpty(result.Observations)}
}

// sliceOrEmpty renders an absent collection as an empty JSON array. Every
// Domain read schema requires its collections, so a nil slice would marshal to
// null and be refused.
func sliceOrEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// loadDomainSnapshot resolves the Product's designated git home and reads the
// committed law folder and Domain registry at its explicit head commit. It
// must run before any transaction opens: both steps are git subprocesses the
// pool lock must never hold (CD-0195 D2).
func loadDomainSnapshot(ctx context.Context, q queryer, productID, op string) (*lawFolderSnapshot, error) {
	home, err := resolveKnowledgeQueryHome(ctx, q, productID, "", KnowledgeHome{}, op)
	if err != nil {
		return nil, err
	}
	return loadLawFolderSnapshotAtHome(ctx, home)
}

// domainRegistryView projects the snapshot's committed registry into the
// authority watermark the Domain reads carry.
func domainRegistryView(snapshot *lawFolderSnapshot, productID string) *DomainRegistryView {
	registry := snapshot.Registry()
	return &DomainRegistryView{
		ProductID:     productID,
		ProductKey:    registry.ProductKey,
		RootDomainID:  registry.RootDomainID,
		ContentHash:   domainRegistryContentHash(registry),
		ScannedCommit: snapshot.CommitOID(),
	}
}

// domainRegistryStatus applies the projection's status default: a registry
// entry without an authored status is current.
func domainRegistryStatus(status string) string {
	if status == "" {
		return "current"
	}
	return status
}

// domainRegistryRelationState applies the projection's state default: a
// relation without an authored state is active.
func domainRegistryRelationState(state string) string {
	if state == "" {
		return "active"
	}
	return state
}

// QueryDomainList returns every current Domain for a Product in the bounded
// page order (name, domain_id). Deprecated Domains stay out of the
// default view; they remain reachable through registry history.
func (s *Store) QueryDomainList(ctx context.Context, req DomainListRequest) (DomainListResult, error) {
	var out DomainListResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainList", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainList(ctx, s.db, req)
}

func queryDomainList(ctx context.Context, q queryer, req DomainListRequest) (DomainListResult, error) {
	var out DomainListResult
	snapshot, err := loadDomainSnapshot(ctx, q, req.Product, "C22.DomainList")
	if err != nil {
		return out, err
	}
	registry := domainRegistryView(snapshot, req.Product)
	limit := req.Limit
	if limit == 0 {
		limit = domainListDefaultLimit
	}
	if limit < 1 || limit > domainListMaxLimit {
		return out, newFailure(KindInvalidFilter, "C22.DomainList", "Domain list limit is out of bounds", false, "use a limit between 1 and 100")
	}
	cursorName, cursorID, err := decodeDomainCursor(req.Cursor)
	if err != nil {
		return out, err
	}
	current := currentSnapshotDomains(snapshot)
	sort.Slice(current, func(i, j int) bool {
		if current[i].Name != current[j].Name {
			return current[i].Name < current[j].Name
		}
		return current[i].DomainID < current[j].DomainID
	})
	out.Domains = []DomainSummary{}
	for _, domain := range current {
		if cursorName != "" && !(domain.Name > cursorName || domain.Name == cursorName && domain.DomainID > cursorID) {
			continue
		}
		domain.HomeDomain = domain.DomainID == registry.RootDomainID
		out.Domains = append(out.Domains, domain)
		if len(out.Domains) > limit {
			break
		}
	}
	if len(out.Domains) > limit {
		out.Domains = out.Domains[:limit]
		last := out.Domains[len(out.Domains)-1]
		next := encodeDomainCursor(last.Name, last.DomainID)
		out.NextCursor = &next
	}
	out.Registry = registry
	out.ResultMeta = ResultMeta{QueryID: "C22.DomainList", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"status", "name", "domain_id"}}
	return out, nil
}

// currentSnapshotDomains lists the snapshot registry's current Domains in
// registry order with the projected status default applied.
func currentSnapshotDomains(snapshot *lawFolderSnapshot) []DomainSummary {
	registry := snapshot.Registry()
	out := make([]DomainSummary, 0, len(registry.Domains))
	for _, domain := range registry.Domains {
		status := domainRegistryStatus(domain.Status)
		if status != "current" {
			continue
		}
		out = append(out, DomainSummary{DomainID: domain.DomainID, Name: domain.Name, Purpose: domain.Purpose, ParentID: domain.ParentDomainID, Status: status})
	}
	return out
}

func decodeDomainCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", newFailure(KindInvalidFilter, "C22", "Domain cursor is malformed", false, "continue from the previous page cursor")
	}
	parts := strings.SplitN(string(raw), "\x1f", 2)
	if len(parts) != 2 {
		return "", "", newFailure(KindInvalidFilter, "C22", "Domain cursor is malformed", false, "continue from the previous page cursor")
	}
	return parts[0], parts[1], nil
}

func encodeDomainCursor(name, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(name + "\x1f" + id))
}

// QueryDomainDetail returns one Domain with its current law page and typed
// architecture relations. Superseded law is absent from the default view; the
// applicability fan-out names the other Domains each current law reaches.
func (s *Store) QueryDomainDetail(ctx context.Context, req DomainDetailRequest) (DomainDetailResult, error) {
	var out DomainDetailResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainDetail", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainDetail(ctx, s.db, req)
}

func queryDomainDetail(ctx context.Context, q queryer, req DomainDetailRequest) (DomainDetailResult, error) {
	var out DomainDetailResult
	snapshot, err := loadDomainSnapshot(ctx, q, req.Product, "C22.DomainDetail")
	if err != nil {
		return out, err
	}
	registry := domainRegistryView(snapshot, req.Product)
	summary, err := domainExistsCurrent(snapshot, req.Domain)
	if err != nil {
		return out, err
	}
	summary.HomeDomain = summary.DomainID == registry.RootDomainID
	out.Domain = summary
	out.Registry = registry
	out.CurrentLaw = []DomainLawRecord{}
	out.Relations = []DomainRelationView{}

	for _, record := range snapshotOrderedList(snapshot) {
		if record.HomeDomainID != req.Domain {
			continue
		}
		out.CurrentLaw = append(out.CurrentLaw, DomainLawRecord{
			LawID:         record.ID,
			Kind:          record.Kind,
			Title:         record.Title,
			Path:          record.Path,
			ContentHash:   record.SHA256,
			AppliesTo:     orderedStrings(record.AppliesToDomainIDs),
			ScannedCommit: snapshot.CommitOID(),
		})
	}

	seenRelation := map[string]bool{}
	for _, domain := range snapshot.Registry().Domains {
		for _, relation := range domain.ArchitectureRelations {
			if domain.DomainID != req.Domain && relation.TargetDomainID != req.Domain {
				continue
			}
			key := domain.DomainID + "\x00" + relation.Kind + "\x00" + relation.TargetDomainID
			if seenRelation[key] {
				continue
			}
			seenRelation[key] = true
			out.Relations = append(out.Relations, DomainRelationView{
				Kind:          relation.Kind,
				SourceDomain:  domain.DomainID,
				TargetDomain:  relation.TargetDomainID,
				State:         domainRegistryRelationState(relation.State),
				GoverningLaws: orderedStrings(relation.GoverningLawIDs),
			})
		}
	}
	sort.Slice(out.Relations, func(i, j int) bool {
		left, right := out.Relations[i], out.Relations[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.SourceDomain != right.SourceDomain {
			return left.SourceDomain < right.SourceDomain
		}
		return left.TargetDomain < right.TargetDomain
	})

	// CD-0224: the open observations join the existing Domain surface, so
	// the operator meets them where the next decision about the Domain is
	// made. They are not authority — the result stays authoritative on law and
	// relations, and an observation satisfies nothing.
	observations, err := observationsForDomain(ctx, q, req.Product, req.Domain, DomainObservationOpenWindow)
	if err != nil {
		return out, err
	}
	out.Observations = observations

	out.ResultMeta = ResultMeta{QueryID: "C22.DomainDetail", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product, DomainID: req.Domain}, Authority: "authoritative", OrderingKeys: []string{"kind", "source_domain_id", "target_domain_id"}}
	return out, nil
}

// snapshotOrderedList lists the snapshot's laws in the stable (kind, law_id)
// order the detail page projects.
func snapshotOrderedList(snapshot *lawFolderSnapshot) []KnowledgeRecord {
	records := make([]KnowledgeRecord, 0, len(snapshot.LawIDs()))
	for _, id := range snapshot.LawIDs() {
		record, _ := snapshot.Law(id)
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Kind != records[j].Kind {
			return records[i].Kind < records[j].Kind
		}
		return records[i].ID < records[j].ID
	})
	return records
}

// domainExistsCurrent resolves one current Domain from the committed
// registry. A deprecated or unknown Domain refuses typed, exactly as the
// projected read did.
func domainExistsCurrent(snapshot *lawFolderSnapshot, domain string) (DomainSummary, error) {
	var summary DomainSummary
	for _, entry := range snapshot.Registry().Domains {
		if entry.DomainID != domain {
			continue
		}
		summary = DomainSummary{DomainID: entry.DomainID, Name: entry.Name, Purpose: entry.Purpose, ParentID: entry.ParentDomainID, Status: domainRegistryStatus(entry.Status)}
		break
	}
	if summary.DomainID == "" {
		return summary, newFailure(KindUnknownDomain, "domain_read", "Domain does not exist in the Product registry", false, "read the Domain list for the current registry before requesting detail")
	}
	if summary.Status != "current" {
		return summary, newFailure(KindUnknownDomain, "domain_read", "Domain is deprecated in the current registry", false, "read current Domains only; deprecated Domains remain historical")
	}
	return summary, nil
}
