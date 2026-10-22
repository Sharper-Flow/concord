package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// backupCurrentStateDigest includes every persistent table, including direct
// authority and SQLite sequence state, plus schema definitions. Physical page
// locations are excluded: an online copy can change those without changing state.
func backupCurrentStateDigest(ctx context.Context, db *sql.DB) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", backupStateReadFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	h := sha256.New()
	if err := hashBackupRows(ctx, tx, h, `SELECT type,name,tbl_name,sql FROM main.sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY`); err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM main.sqlite_schema WHERE type='table' ORDER BY name COLLATE BINARY`)
	if err != nil {
		return "", backupStateReadFailure(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return "", backupStateReadFailure(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", backupStateReadFailure(err)
	}
	for _, table := range tables {
		name := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		query := `SELECT * FROM main.` + name
		probe, err := tx.QueryContext(ctx, query+` LIMIT 0`)
		if err != nil {
			return "", backupStateReadFailure(err)
		}
		columns, err := probe.Columns()
		if closeErr := probe.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return "", backupStateReadFailure(err)
		}
		var order []string
		for _, column := range columns {
			quoted := `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
			// Separate INTEGER and REAL ties and ignore declared text collations.
			order = append(order, `typeof(`+quoted+`)`, quoted+` COLLATE BINARY`)
		}
		writeBackupHashValue(h, table)
		if err := hashBackupRows(ctx, tx, h, query+` ORDER BY `+strings.Join(order, ",")); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", backupStateReadFailure(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBackupRows(ctx context.Context, tx *sql.Tx, h hash.Hash, query string) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return backupStateReadFailure(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return backupStateReadFailure(err)
	}
	writeBackupHashValue(h, len(columns))
	for _, column := range columns {
		writeBackupHashValue(h, column)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	var count int64
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return backupStateReadFailure(err)
		}
		// Structural bytes cannot be mistaken for length-prefixed values.
		_, _ = h.Write([]byte{0})
		for _, value := range values {
			writeBackupHashValue(h, value)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return backupStateReadFailure(err)
	}
	_, _ = h.Write([]byte{1})
	writeBackupHashValue(h, count)
	return nil
}

// Length framing keeps embedded NULs and delimiters unambiguous. The driver
// storage type distinguishes NULL, text, blobs, integers, and real values.
func writeBackupHashValue(h hash.Hash, value any) {
	encoded := fmt.Sprintf("%T:%v", value, value)
	_, _ = fmt.Fprintf(h, "%d:%s", len(encoded), encoded)
}

func backupStateReadFailure(err error) error {
	return wrapFailure(KindUnavailable, "verify_backup", "cannot read snapshot current state", false, "supply a readable Concord SQLite snapshot", err)
}
