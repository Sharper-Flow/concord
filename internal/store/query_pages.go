package store

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
