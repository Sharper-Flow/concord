package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type initiativeStatementQueryer struct {
	*countingQueryer
	statements []string
}

func (q *initiativeStatementQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.statements = append(q.statements, query)
	return q.countingQueryer.QueryRowContext(ctx, query, args...)
}

// The validator's exclusive writer window must not walk canonical projection
// rows or build a temporary aggregate. Two statements alone do not bound work.
func TestInitiativeInvariants_NoProjectionWalkInsideValidator(t *testing.T) {
	ctx := context.Background()
	s := newBootstrapStore(t)
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 150, 100)
	err := s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginFold(ctx, tx.tx)
		if err != nil {
			return err
		}
		defer func() { _ = scope.close(ctx) }()
		q := &initiativeStatementQueryer{countingQueryer: &countingQueryer{Tx: tx.tx}}
		if err := validateInitiativeInvariantsTx(ctx, q); err != nil {
			return err
		}
		if q.queries.Load() != 0 || q.queryRows.Load() != 2 {
			t.Errorf("validator reads=(%d, %d), want (0, 2)", q.queries.Load(), q.queryRows.Load())
		}
		roots := map[int]string{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT rootpage,name FROM sqlite_schema WHERE tbl_name IN ('work_items','work_projects','product_projects','initiative_entries','relations') AND rootpage>0`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var root int
			var name string
			if err := rows.Scan(&root, &name); err != nil {
				return errors.Join(err, rows.Close())
			}
			roots[root] = name
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for i, statement := range q.statements {
			rows, err := tx.tx.QueryContext(ctx, "EXPLAIN "+statement)
			if err != nil {
				return err
			}
			resultRows, limitCounters := 0, 0
			for rows.Next() {
				var addr, p1, p2, p3, p5 int
				var opcode string
				var p4, comment sql.NullString
				if err := rows.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
					return errors.Join(err, rows.Close())
				}
				if opcode == "OpenRead" && p3 == 0 && roots[p2] != "" {
					t.Errorf("validator statement %d opens canonical projection %s; work must not depend on total fixture size", i+1, roots[p2])
				}
				if opcode == "OpenEphemeral" || opcode == "SorterOpen" {
					t.Errorf("validator statement %d builds a temporary projection with %s", i+1, opcode)
				}
				if opcode == "ResultRow" {
					resultRows++
				}
				if opcode == "DecrJumpZero" {
					limitCounters++
				}
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			// A bounded validator reads at most one row per statement: the
			// program emits exactly one ResultRow opcode inside its loop, and
			// the LIMIT counter (DecrJumpZero) stops the scan after the first.
			if resultRows != 1 || limitCounters != 1 {
				t.Errorf("validator statement %d has %d ResultRow and %d DecrJumpZero opcodes, want 1 and 1: the read must stop at the first row", i+1, resultRows, limitCounters)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
