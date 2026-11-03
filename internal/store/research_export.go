package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

type ResearchExportResult struct {
	Destination string `json:"destination"`
	PackCount   int    `json:"pack_count"`
}

type researchExportPack struct {
	PackID string                      `json:"pack_id"`
	Tables map[string][]map[string]any `json:"tables"`
}

var researchExportTables = []struct{ name, order string }{
	{"active_research_packs", "pack_id"},
	{"active_research_revisions", "pack_id,revision"},
	{"active_research_findings", "pack_id,revision,finding_id"},
	{"active_research_sources", "pack_id,revision,source_id"},
	{"active_research_finding_sources", "pack_id,revision,finding_id,source_id"},
	{"active_research_finding_scopes", "pack_id,revision,finding_id,scope_kind,scope_id"},
	{"active_research_consumers", "pack_id,revision,consumer_work_id"},
}

// ExportResearchDatabase reads all research rows, including retained packs, from
// one snapshot without creating or migrating the source. Each JSON file contains
// raw table rows; JSON-valued text columns remain text. Filenames are SHA-256 pack
// IDs. The destination must be absent or empty. On failure, any partial output is
// not a complete export and must not be used to authorize dropping source data.
func ExportResearchDatabase(ctx context.Context, path, destination string) (result ResearchExportResult, err error) {
	if path == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return ResearchExportResult{}, fmt.Errorf("research export requires a source and an absolute clean destination directory")
	}
	if err := AdmitStorePath(path); err != nil {
		return ResearchExportResult{}, err
	}
	packs, err := readResearchExport(ctx, path)
	if err != nil {
		return ResearchExportResult{}, fmt.Errorf("read research export: %w", err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil && !os.IsExist(err) {
		return ResearchExportResult{}, fmt.Errorf("create research export directory: %w", err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return ResearchExportResult{}, err
	}
	if !info.IsDir() {
		return ResearchExportResult{}, fmt.Errorf("research export destination is not a directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return ResearchExportResult{}, err
	}
	if len(entries) != 0 {
		return ResearchExportResult{}, fmt.Errorf("research export destination is not empty")
	}
	defer func() {
		if err != nil {
			err = wrapFailure(KindUnavailable, "research_export", "research export did not complete", false,
				"keep the source database; retry export to a new empty directory before dropping any research tables", err)
		}
	}()
	ids := make([]string, 0, len(packs))
	for id := range packs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return ResearchExportResult{}, err
		}
		data, err := json.MarshalIndent(packs[id], "", "  ")
		if err != nil {
			return ResearchExportResult{}, err
		}
		name := fmt.Sprintf("%x.json", sha256.Sum256([]byte(id)))
		file, err := os.OpenFile(filepath.Join(destination, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // The caller supplies the checked-empty directory; the filename is a fixed-length hash and O_EXCL refuses replacement.
		if err != nil {
			return ResearchExportResult{}, err
		}
		_, err = file.Write(append(data, '\n'))
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return ResearchExportResult{}, err
		}
	}
	return ResearchExportResult{Destination: destination, PackCount: len(packs)}, nil
}

func readResearchExport(ctx context.Context, path string) (map[string]*researchExportPack, error) {
	db, err := sqlitedb.Open(readOnlyDataSource(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	tx, err := sqlitedb.BeginRead(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	packs := make(map[string]*researchExportPack)
	for _, table := range researchExportTables {
		if err := readResearchExportTable(ctx, tx, table.name, table.order, packs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return packs, db.Close()
}

func readResearchExportTable(ctx context.Context, tx *sql.Tx, table, order string, packs map[string]*researchExportPack) error {
	// Table names and ordering come only from researchExportTables, never input.
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY `+order) //nolint:gosec // Identifiers come only from the closed researchExportTables list, never caller input.
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			value := values[i]
			// JSON text is also stored as []byte by research mutations. Export
			// its content as text, not the JSON encoder's base64 blob form.
			if data, ok := value.([]byte); ok {
				value = string(data)
			}
			if text, ok := value.(string); ok && !utf8.ValidString(text) {
				return fmt.Errorf("research export column %s.%s contains non-UTF-8 data", table, column)
			}
			row[column] = value
		}
		id, ok := row["pack_id"].(string)
		if !ok || id == "" {
			return fmt.Errorf("research export row in %s has no pack ID", table)
		}
		pack := packs[id]
		if pack == nil {
			pack = &researchExportPack{PackID: id, Tables: make(map[string][]map[string]any)}
			for _, table := range researchExportTables {
				pack.Tables[table.name] = []map[string]any{}
			}
			packs[id] = pack
		}
		pack.Tables[table] = append(pack.Tables[table], row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}
