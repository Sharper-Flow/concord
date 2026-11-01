package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// A Domain is a second observation anchor beside the work item. Domain
// observations are non-authoritative in exactly the way work-anchored ones
// are: no gate, no evidence kind, and no workflow action reads one as
// authority. Promotion to work stays the CD-0018 capture path.

var domainObservationIDPattern = regexp.MustCompile(`^dob:[0-9a-f]{16}$`)

// Observation states follow the two-state shape of work_messages:
// dismissal flips state and never deletes, so the row survives for audit.
const (
	DomainObservationOpen      = "open"
	DomainObservationDismissed = "dismissed"
)

// DomainObservationOpenWindow is the cap on observations in state
// open for one Domain. A work item goes terminal and stops recording; a Domain
// is perpetual and never does, so unbounded growth replaces staleness as the
// failure mode. The cap is a declared constant, not a derived one.
const DomainObservationOpenWindow = 64

// DomainObservation is one durable Domain-anchored observation row.
type DomainObservation struct {
	ObservationID string   `json:"observation_id"`
	ProductID     string   `json:"product_id"`
	DomainID      string   `json:"domain_id"`
	Statement     string   `json:"statement"`
	Refs          []string `json:"refs,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	State         string   `json:"state"`
	RecordedAt    string   `json:"recorded_at"`
	DismissedAt   string   `json:"dismissed_at,omitempty"`
}

type domainObservationRecordedPayload struct {
	ObservationID string   `json:"observation_id"`
	ProductID     string   `json:"product_id"`
	DomainID      string   `json:"domain_id"`
	Statement     string   `json:"statement"`
	Refs          []string `json:"refs"`
	Tags          []string `json:"tags"`
}

type domainObservationDismissedPayload struct {
	ObservationID string `json:"observation_id"`
	ProductID     string `json:"product_id"`
	DomainID      string `json:"domain_id"`
}

func validateCurrentDomain(ctx context.Context, tx *sql.Tx, productID, domainID string) error {
	// The committed registry is the only Domain authority. The recording
	// route establishes the knowledge-source proof before the transaction
	// opens (EstablishDomainRegistryProof); the fold revalidates it and reads
	// its snapshot. A missing, rebound, or content-moved proof refuses with
	// its own typed reason rather than claiming the Domain is absent.
	snapshot, err := lawSnapshotForProduct(ctx, tx, productID, "fold_event")
	if err != nil {
		return err
	}
	registry, err := snapshot.Registry()
	if err != nil {
		return err
	}
	for _, domain := range registry.Domains {
		if domain.DomainID != domainID {
			continue
		}
		if domainRegistryStatus(domain.Status) != "current" {
			return newFailure(KindInvalidTransition, "fold_event", "deprecated Domain cannot receive observations", false, "record observations only on a current Domain")
		}
		return nil
	}
	return newFailure(KindProjectionNotFound, "fold_event", "Domain does not exist in the current Product registry", false, "verify the Product's committed Domain registry before recording local state")
}

func foldDomainObservationRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p domainObservationRecordedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !domainObservationIDPattern.MatchString(p.ObservationID) {
		return newFailure(KindInvalidPayload, "fold_event", "observation id must be a dob: identifier", false, "supply a generated Domain observation id")
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" || p.DomainID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "Domain observation payload is incomplete", false, "supply matching Product and Domain identity")
	}
	if err := validateObservationBounds(p.Statement, p.Refs, p.Tags); err != nil {
		return err
	}
	if err := validateCurrentDomain(ctx, tx, p.ProductID, p.DomainID); err != nil {
		return err
	}
	// A full window refuses and names the Domain. Eviction is
	// rejected — dropping the oldest observation to admit a new one is the
	// silent drop CD-0030 exists to prevent. Only open rows count.
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_observations WHERE product_id=? AND domain_id=? AND state=?`,
		p.ProductID, p.DomainID, DomainObservationOpen).Scan(&open); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot measure the Domain observation window", true, "retry once the database is readable", err)
	}
	if open >= DomainObservationOpenWindow {
		return newFailure(KindInvariantViolation, "fold_event",
			fmt.Sprintf("Domain %s already holds %d open observations", p.DomainID, DomainObservationOpenWindow),
			false, "contact_operator")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_observations(observation_id,product_id,domain_id,statement,refs,tags,state,recorded_at,dismissed_at) VALUES(?,?,?,?,?,?,?,?,NULL)`,
		p.ObservationID, p.ProductID, p.DomainID, p.Statement, marshalStrings(p.Refs), marshalStrings(p.Tags),
		DomainObservationOpen, event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		if sqlitedb.IsIdentityConflict(err) {
			return newFailure(KindProjectionConflict, "fold_event", "observation id already exists", false, "generate a new observation id")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the Domain observation", true, "retry once the database is writable", err)
	}
	return nil
}

func foldDomainObservationDismissed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p domainObservationDismissedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !domainObservationIDPattern.MatchString(p.ObservationID) {
		return newFailure(KindInvalidPayload, "fold_event", "observation id must be a dob: identifier", false, "supply an existing Domain observation id")
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" || p.DomainID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "Domain observation payload is incomplete", false, "supply matching Product and Domain identity")
	}
	// The flip is conditional on the row still being open, so a replayed or
	// racing dismissal refuses rather than rewriting the audit timestamp.
	result, err := tx.ExecContext(ctx, `UPDATE domain_observations SET state=?, dismissed_at=? WHERE observation_id=? AND product_id=? AND domain_id=? AND state=?`,
		DomainObservationDismissed, event.OccurredAt.UTC().Format(time.RFC3339Nano),
		p.ObservationID, p.ProductID, p.DomainID, DomainObservationOpen)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot dismiss the Domain observation", true, "retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot confirm the Domain observation dismissal", true, "retry once the database is writable", err)
	}
	if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "no open observation with that id on the Domain", false, "dismiss an open observation recorded on this Domain")
	}
	return nil
}

// observationsForDomain lists a Domain's open observations, newest first,
// bounded. concord_domain.detail carries them rather than adding
// a read operation. Dismissed rows persist for audit and are not returned.
func observationsForDomain(ctx context.Context, q queryer, productID, domainID string, limit int) ([]DomainObservation, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > DomainObservationOpenWindow {
		limit = DomainObservationOpenWindow
	}
	rows, err := q.QueryContext(ctx, `SELECT observation_id,product_id,domain_id,statement,refs,tags,state,recorded_at FROM domain_observations WHERE product_id=? AND domain_id=? AND state=? ORDER BY recorded_at DESC, observation_id LIMIT ?`,
		productID, domainID, DomainObservationOpen, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "domain_observations", "cannot read Domain observations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []DomainObservation{}
	for rows.Next() {
		var o DomainObservation
		var refs, tags string
		if err := rows.Scan(&o.ObservationID, &o.ProductID, &o.DomainID, &o.Statement, &refs, &tags, &o.State, &o.RecordedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "domain_observations", "cannot decode Domain observation", true, "retry once the database is readable", err)
		}
		_ = json.Unmarshal([]byte(refs), &o.Refs)
		_ = json.Unmarshal([]byte(tags), &o.Tags)
		out = append(out, o)
	}
	return out, rows.Err()
}
