package store

import (
	"context"
	"database/sql"
	"regexp"
	"time"
)

// CD-0029: peer messages are durable records addressed to work, not sessions,
// so they survive the restarts CD-0016 prefers. A message carries no
// authority: nothing in the engine, the folds, or the gates reads messages,
// so a message cannot approve, complete, unblock, or rewrite a contract. It
// can only be read by whatever session next picks the work up.
//
// Obsolescence is handled at read time: messages are withdrawable by their
// sender, and every read returns the message state-stamped so the reader
// evaluates staleness against current work state — the incident's ten-minute
// obsolescence is normal, not exceptional.

const (
	MessageStateSent      = "sent"
	MessageStateWithdrawn = "withdrawn"
	messageBodyMax        = 4096
)

var messageIDPattern = regexp.MustCompile(`^msg:[0-9a-f]{32}$`)

// PeerMessage is one durable message row.
type PeerMessage struct {
	MessageID       string `json:"message_id"`
	SenderWorkID    string `json:"sender_work_id"`
	RecipientWorkID string `json:"recipient_work_id"`
	Body            string `json:"body"`
	State           string `json:"state"`
	SentAt          string `json:"sent_at"`
	WithdrawnAt     string `json:"withdrawn_at,omitempty"`
}

type messageSentPayload struct {
	WorkflowVersionFields
	MessageID string `json:"message_id"`
	// RecipientWorkID is empty on a broadcast event; recipients are fanned
	// out by the dispatch layer, one event per (sender, recipient) pair, so
	// the fold stays a single-row insert with cross-work FK validation.
	RecipientWorkID string `json:"recipient_work_id"`
	Body            string `json:"body"`
}

type messageWithdrawnPayload struct {
	WorkflowVersionFields
	MessageID string `json:"message_id"`
}

func foldMessageSent(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p messageSentPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !messageIDPattern.MatchString(p.MessageID) {
		return newFailure(KindInvalidPayload, "fold_event", "message id must be a msg: identifier", false, "supply a generated message id")
	}
	if p.RecipientWorkID == "" || p.RecipientWorkID == event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "message requires a recipient other than its sender", false, "address the message to other work")
	}
	if len(p.Body) < 1 || len(p.Body) > messageBodyMax {
		return newFailure(KindInvalidPayload, "fold_event", "message body must be a bounded non-empty string", false, "supply a body of at most 4096 characters")
	}
	if p.ExpectedVersion == nil || p.ResultingVersion == nil || *p.ResultingVersion != *p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "message send version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	// Cross-work FK validation precedent: foldWorkflowSuccessorLinked.
	var recipientExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, p.RecipientWorkID).Scan(&recipientExists); err == sql.ErrNoRows {
		return newFailure(KindInvalidRelation, "fold_event", "recipient work item is not recorded", false, "address the message to existing work")
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_messages(message_id,sender_work_id,recipient_work_id,body,state,sent_at,withdrawn_at) VALUES(?,?,?,?,?,?,NULL)`,
		p.MessageID, event.SubjectID, p.RecipientWorkID, p.Body, MessageStateSent, event.OccurredAt.Format(time.RFC3339Nano)); err != nil {
		return newFailure(KindProjectionConflict, "fold_event", "message id already exists", false, "generate a new message id")
	}
	return bumpVersion(ctx, tx, "work_items", event, *p.ExpectedVersion, *p.ResultingVersion, "work item")
}

func foldMessageWithdrawn(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p messageWithdrawnPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !messageIDPattern.MatchString(p.MessageID) {
		return newFailure(KindInvalidPayload, "fold_event", "message id must be a msg: identifier", false, "supply the message id")
	}
	if p.ExpectedVersion == nil || p.ResultingVersion == nil || *p.ResultingVersion != *p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "message withdraw version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	res, err := tx.ExecContext(ctx, `UPDATE work_messages SET state=?, withdrawn_at=? WHERE message_id=? AND sender_work_id=? AND state=?`,
		MessageStateWithdrawn, event.OccurredAt.Format(time.RFC3339Nano), p.MessageID, event.SubjectID, MessageStateSent)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "no sent message by this work item with that id", false, "withdraw a message this work item sent")
	}
	return bumpVersion(ctx, tx, "work_items", event, *p.ExpectedVersion, *p.ResultingVersion, "work item")
}

// MessagesForWork lists messages addressed to one work item, newest first,
// state-stamped. Read-time obsolescence: withdrawn messages are returned in
// their withdrawn state rather than hidden, so a reader can see that a prior
// finding was retracted.
func (s *Store) MessagesForWork(ctx context.Context, workID string, limit int) ([]PeerMessage, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "work_messages", "store is not open", false, "open the authority database")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id,sender_work_id,recipient_work_id,body,state,sent_at,coalesce(withdrawn_at,'') FROM work_messages WHERE recipient_work_id=? ORDER BY sent_at DESC, message_id LIMIT ?`, workID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_messages", "cannot read messages", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []PeerMessage{}
	for rows.Next() {
		var m PeerMessage
		if err := rows.Scan(&m.MessageID, &m.SenderWorkID, &m.RecipientWorkID, &m.Body, &m.State, &m.SentAt, &m.WithdrawnAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_messages", "cannot decode message", true, "retry once the database is readable", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UnreadMessageCount is the continuity pointer: how many sent (not
// withdrawn) messages await the work's next session. Bounded single query.
func (s *Store) UnreadMessageCount(ctx context.Context, workID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "work_messages", "store is not open", false, "open the authority database")
	}
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_messages WHERE recipient_work_id=? AND state='sent'`, workID).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, "work_messages", "cannot count messages", true, "retry once the database is readable", err)
	}
	return count, nil
}

// ActiveWorkInProduct lists in_progress work item ids in one Product — the
// broadcast fan-out set. Bounded, indexed through the scoped-membership CTE
// shape used by every Product read.
func (s *Store) ActiveWorkInProduct(ctx context.Context, productID string, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "work_messages", "store is not open", false, "open the authority database")
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id FROM work_items w WHERE w.lifecycle='in_progress' AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=?) ORDER BY w.id LIMIT ?`, productID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_messages", "cannot list active work", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_messages", "cannot decode work id", true, "retry once the database is readable", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RebuildAfterMessagesAndClaimsProvesDeleteOrder is asserted indirectly by
// work_observations_test.go's rebuild test, which exercises the same list.
