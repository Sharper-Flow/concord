package store

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
)

// LimitPage keeps a complete prefix of a captured result and derives the cursor
// from its last emitted key. It does not re-query or change the source watermark.
func (q Q3Result) LimitPage(req Q3Request, limit int) (Q3Result, error) {
	limit, err := queryLimit(limit)
	if err != nil || len(q.Items) <= limit {
		return q, err
	}
	states, err := validateLifecycleFilters(req.LifecycleStates)
	if err != nil {
		return q, err
	}
	order, _ := q3Order(states)
	last := q.Items[limit-1]
	ts := last.CreatedAt
	if terminalOnlyQ3(states) {
		ts = last.TerminalAt
	}
	encoded, err := encodeQ3Cursor(q3Cursor{Version: 1, QueryID: "PM1.Q3", Product: req.Product, Project: req.Project, States: states, Order: order, Urgency: last.Urgency, Priority: last.Priority, Timestamp: ts, ID: last.ID})
	if err != nil {
		return q, err
	}
	q.Items = q.Items[:limit]
	q.NextCursor = &encoded
	return q, nil
}

func q3Order(states []string) (string, string) {
	if terminalOnlyQ3(states) {
		return "urgency:asc,terminal_time:desc,priority:asc,id:asc", "urgency ASC, relevant_time DESC, priority ASC, id ASC"
	}
	return "urgency:asc,priority:asc,relevant_time:desc,id:asc", "urgency ASC, priority ASC, relevant_time DESC, id ASC"
}

func (q Q4Result) LimitPage(req Q4Request, limit int) (Q4Result, error) {
	items, next, err := offsetWorkPage(q.Items, q.NextCursor, req.Cursor, req.Offset, limit)
	q.Items, q.NextCursor = items, next
	return q, err
}

func (q Q5Result) LimitPage(req Q5Request, limit int) (Q5Result, error) {
	items, next, err := offsetWorkPage(q.Items, q.NextCursor, req.Cursor, req.Offset, limit)
	q.Items, q.NextCursor = items, next
	return q, err
}

func offsetWorkPage(items []WorkItem, next *string, cursor string, offset, limit int) ([]WorkItem, *string, error) {
	limit, err := queryLimit(limit)
	if err != nil || len(items) <= limit {
		return items, next, err
	}
	if cursor != "" {
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return nil, nil, newFailure(KindInvalidCursor, "query", "work page cursor is invalid", false, "restart_query")
		}
	}
	token := strconv.Itoa(offset + limit)
	return items[:limit], &token, nil
}

func (q Q6Result) LimitPage(req Q6Request, limit int) (Q6Result, error) {
	limit, err := queryLimit(limit)
	if err != nil || len(q.Items) <= limit {
		return q, err
	}
	offset := req.Offset
	if req.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		var cursor q6Cursor
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Offset < 0 {
			return q, newFailure(KindInvalidCursor, "PM1.Q6", "scope cursor is invalid", false, "restart_query")
		}
		offset = cursor.Offset
	}
	raw, err := json.Marshal(q6Cursor{Version: 1, Product: req.Product, Project: req.Project, Order: "urgency:asc,priority:asc,updated_at:desc,id:asc", Offset: offset + limit})
	if err != nil {
		return q, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	q.Items, q.NextCursor = q.Items[:limit], &token
	return q, nil
}
