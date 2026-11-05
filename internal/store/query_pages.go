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
	if last.Terminal {
		ts = last.TerminalAt
	}
	encoded, err := encodeQ3Cursor(q3Cursor{Version: 1, QueryID: "PM1.Q3", Product: req.Product, Project: req.Project, States: states, Order: order, Timestamp: ts, ID: last.ID})
	if err != nil {
		return q, err
	}
	q.Items = q.Items[:limit]
	q.NextCursor = &encoded
	return q, nil
}

func q3Order(_ []string) (string, string) {
	return "relevant_time:desc,id:asc", "relevant_time DESC, id ASC"
}
