package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// migration is one ordered, immutable step of the schema manifest. A recorded
// migration is never edited: its checksum is compared on every open, so an
// edited step is reported instead of silently diverging from the live schema.
type migration struct {
	Version int
	Name    string
	SQL     string
}

// migrations is the ordered manifest. Append new steps; never rewrite applied
// ones.
var migrations = []migration{
	{
		Version: 1,
		Name:    "domain_events",
		SQL: `
CREATE TABLE domain_events (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT    NOT NULL UNIQUE,
    kind            TEXT    NOT NULL,
    subject_type    TEXT    NOT NULL,
    subject_id      TEXT    NOT NULL,
    actor           TEXT    NOT NULL,
    occurred_at     TEXT    NOT NULL,
    payload_version INTEGER NOT NULL,
    payload         TEXT    NOT NULL,

    CHECK (length(event_id) > 0),
    CHECK (length(kind) > 0),
    CHECK (length(subject_type) > 0),
    CHECK (length(subject_id) > 0),
    CHECK (length(actor) > 0),
    CHECK (length(occurred_at) > 0),
    CHECK (payload_version > 0),
    CHECK (json_valid(payload)),
    CHECK (json_type(payload) = 'object')
);

CREATE INDEX domain_events_subject ON domain_events (subject_type, subject_id, seq);
CREATE INDEX domain_events_kind ON domain_events (kind, seq);

-- The log is the sole authority for live state, so rewriting it is refused by
-- the database rather than by convention.
CREATE TRIGGER domain_events_no_update
BEFORE UPDATE ON domain_events
BEGIN
    SELECT RAISE(ABORT, 'domain_events is append-only');
END;

CREATE TRIGGER domain_events_no_delete
BEFORE DELETE ON domain_events
BEGIN
    SELECT RAISE(ABORT, 'domain_events is append-only');
END;
`,
	},
	{
		Version: 2,
		Name:    "typed_projections",
		SQL: `
CREATE TABLE fold_guard (
    active INTEGER PRIMARY KEY,
    CHECK (active = 1)
);

CREATE TABLE products (
    id                          TEXT    PRIMARY KEY,
    display_name                TEXT    NOT NULL,
    stage_maturity              TEXT    NOT NULL CHECK (stage_maturity IN ('prototype', 'alpha', 'beta', 'production', 'deprecated')),
    stage_audience_commitment   TEXT    NOT NULL CHECK (stage_audience_commitment IN ('operator_only', 'limited', 'public')),
    version                     INTEGER NOT NULL,
    created_at                  TEXT    NOT NULL,
    updated_at                  TEXT    NOT NULL
);

CREATE TABLE projects (
    id           TEXT    PRIMARY KEY,
    display_name TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

CREATE TRIGGER products_guard_insert
BEFORE INSERT ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER products_guard_update
BEFORE UPDATE ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER products_guard_delete
BEFORE DELETE ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER projects_guard_insert
BEFORE INSERT ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER projects_guard_update
BEFORE UPDATE ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER projects_guard_delete
BEFORE DELETE ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
`,
	},
	{
		Version: 3,
		Name:    "work_items_and_relations",
		SQL: `
CREATE TABLE work_items (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    title         TEXT NOT NULL,
    lifecycle     TEXT NOT NULL CHECK(lifecycle IN ('needed', 'in_progress', 'completed', 'cancelled', 'superseded')),
    priority      INTEGER NOT NULL,
    version       INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    terminal_time TEXT
);

CREATE TABLE relations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    work_id_from TEXT NOT NULL REFERENCES work_items(id),
    work_id_to   TEXT NOT NULL REFERENCES work_items(id),
    kind         TEXT NOT NULL CHECK(kind IN ('parent', 'blocks', 'supersedes', 'implements')),
    created_at   TEXT NOT NULL,
    CHECK(work_id_from <> work_id_to),
    UNIQUE(work_id_from, work_id_to, kind)
);

CREATE INDEX idx_relations_from_kind ON relations(work_id_from, kind, work_id_to);
CREATE UNIQUE INDEX relations_supersedes_target ON relations(work_id_to) WHERE kind = 'supersedes';

CREATE TRIGGER work_items_guard_insert
BEFORE INSERT ON work_items FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_items is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER work_items_guard_update
BEFORE UPDATE ON work_items FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_items is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER work_items_guard_delete
BEFORE DELETE ON work_items FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_items is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER relations_guard_insert
BEFORE INSERT ON relations FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'relations is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER relations_guard_update
BEFORE UPDATE ON relations FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'relations is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER relations_guard_delete
BEFORE DELETE ON relations FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'relations is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
`,
	},
	{
		Version: 4,
		Name:    "product_project_and_work_project_memberships",
		SQL: `
CREATE TABLE product_projects (
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    role       TEXT NOT NULL CHECK(role IN ('primary','secondary')),
    PRIMARY KEY(product_id, project_id)
);
CREATE UNIQUE INDEX product_projects_one_primary
    ON product_projects(product_id) WHERE role='primary';
CREATE INDEX product_projects_by_project
    ON product_projects(project_id, product_id);

CREATE TABLE work_projects (
    work_id    TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    role       TEXT NOT NULL CHECK(role IN ('primary','secondary')),
    PRIMARY KEY(work_id, project_id)
);
CREATE UNIQUE INDEX work_projects_one_primary
    ON work_projects(work_id) WHERE role='primary';
CREATE INDEX work_projects_by_project
    ON work_projects(project_id, work_id);

CREATE TRIGGER product_projects_guard_insert
BEFORE INSERT ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER product_projects_guard_update
BEFORE UPDATE ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER product_projects_guard_delete
BEFORE DELETE ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER work_projects_guard_insert
BEFORE INSERT ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER work_projects_guard_update
BEFORE UPDATE ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER work_projects_guard_delete
BEFORE DELETE ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
`,
	},
	{
		Version: 5,
		Name:    "incoming_relation_lookup",
		SQL: `
CREATE INDEX idx_relations_to_kind
ON relations(work_id_to, kind, work_id_from);
`,
	},
	{
		Version: 6,
		Name:    "git_knowledge_index",
		SQL: `
CREATE TABLE archived_work (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,
    title           TEXT NOT NULL,
    completed_at    TEXT NOT NULL,
    outcome_tag     TEXT NOT NULL,
    lesson_tags     TEXT NOT NULL,
    terminal_state  TEXT NOT NULL CHECK(terminal_state IN ('completed','cancelled','superseded')),
    priority        INTEGER NOT NULL,
    summary         TEXT NOT NULL,
    successor_work_id TEXT,
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    note_path       TEXT NOT NULL,
    commit_oid      TEXT NOT NULL,
    content_hash    TEXT NOT NULL
);

CREATE TABLE archived_work_products (
    work_id TEXT NOT NULL REFERENCES archived_work(id) ON DELETE RESTRICT,
    product_id TEXT NOT NULL,
    PRIMARY KEY(work_id, product_id)
);
CREATE TABLE archived_work_projects (
    work_id TEXT NOT NULL REFERENCES archived_work(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL,
    PRIMARY KEY(work_id, project_id)
);
CREATE TABLE archived_work_components (
    work_id TEXT NOT NULL REFERENCES archived_work(id) ON DELETE RESTRICT,
    component_id TEXT NOT NULL,
    PRIMARY KEY(work_id, component_id)
);
CREATE TABLE archived_work_tags (
    work_id TEXT NOT NULL REFERENCES archived_work(id) ON DELETE RESTRICT,
    tag_id TEXT NOT NULL,
    PRIMARY KEY(work_id, tag_id)
);

CREATE TABLE knowledge_index_watermark (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    head_ref TEXT NOT NULL,
    scanned_commit_oid TEXT NOT NULL,
    scanned_at TEXT NOT NULL,
    complete INTEGER NOT NULL CHECK(complete IN (0,1)),
    PRIMARY KEY(home_project_id, home_locator_id, head_ref)
);

CREATE INDEX archived_work_completed_order ON archived_work(completed_at DESC, id);
CREATE INDEX archived_work_products_lookup ON archived_work_products(product_id, work_id);
CREATE INDEX archived_work_projects_lookup ON archived_work_projects(project_id, work_id);
CREATE INDEX archived_work_components_lookup ON archived_work_components(component_id, work_id);
CREATE INDEX archived_work_tags_lookup ON archived_work_tags(tag_id, work_id);

CREATE TRIGGER archived_work_guard_insert
BEFORE INSERT ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_guard_update
BEFORE UPDATE ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_guard_delete
BEFORE DELETE ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER archived_work_products_guard_insert
BEFORE INSERT ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_products_guard_update
BEFORE UPDATE ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_products_guard_delete
BEFORE DELETE ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER archived_work_projects_guard_insert
BEFORE INSERT ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_projects_guard_update
BEFORE UPDATE ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_projects_guard_delete
BEFORE DELETE ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER archived_work_components_guard_insert
BEFORE INSERT ON archived_work_components FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_components is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_components_guard_update
BEFORE UPDATE ON archived_work_components FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_components is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_components_guard_delete
BEFORE DELETE ON archived_work_components FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_components is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER archived_work_tags_guard_insert
BEFORE INSERT ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_tags_guard_update
BEFORE UPDATE ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_tags_guard_delete
BEFORE DELETE ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

CREATE TRIGGER knowledge_index_watermark_guard_insert
BEFORE INSERT ON knowledge_index_watermark FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_index_watermark is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER knowledge_index_watermark_guard_update
BEFORE UPDATE ON knowledge_index_watermark FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_index_watermark is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER knowledge_index_watermark_guard_delete
BEFORE DELETE ON knowledge_index_watermark FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_index_watermark is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
		`,
	},
	{
		Version: 7,
		Name:    "durable_operations_and_idempotency",
		SQL: `
CREATE TABLE durable_operations (
    op_id TEXT NOT NULL,
    attempt_epoch INTEGER NOT NULL,
    work_id TEXT NOT NULL,
    workflow_type_ref TEXT NOT NULL,
    workflow_type_version INTEGER NOT NULL,
    step_id TEXT NOT NULL,
    step_kind TEXT NOT NULL CHECK(step_kind IN ('internal_sqlite','cross_authority','external_effect')),
    accepted_inputs_digest TEXT NOT NULL,
    accepted_scope_snapshot TEXT NOT NULL,
    result_kind TEXT CHECK(result_kind IS NULL OR result_kind IN ('completed','pending','partial','failed','failed_stale')),
    result_payload TEXT,
    evidence_refs TEXT NOT NULL DEFAULT '[]',
    changed_refs TEXT NOT NULL DEFAULT '[]',
    resume_cursor TEXT,
    principal_ref TEXT NOT NULL,
    request_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    completed_at TEXT,
    PRIMARY KEY(op_id, attempt_epoch)
);
CREATE INDEX durable_operations_pending ON durable_operations(work_id,result_kind)
    WHERE result_kind IS NULL OR result_kind IN ('pending','partial');

CREATE TABLE idempotency_records (
    principal_ref TEXT NOT NULL,
    tool TEXT NOT NULL,
    operation_kind TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    canonical_digest TEXT NOT NULL,
    op_id TEXT NOT NULL,
    result_event_ids TEXT NOT NULL DEFAULT '[]',
    replayed_count INTEGER NOT NULL DEFAULT 0,
    first_observed_at TEXT NOT NULL,
    last_observed_at TEXT NOT NULL,
    PRIMARY KEY(principal_ref,tool,operation_kind,idempotency_key)
);
`,
	},
	{
		Version: 8,
		Name:    "active_research_and_epic_entries",
		SQL: `
CREATE TABLE active_research_packs (
    pack_id         TEXT PRIMARY KEY,
    owner_work_id   TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    current_revision INTEGER NOT NULL CHECK(current_revision > 0),
    freshness       TEXT NOT NULL CHECK(freshness IN ('current','stale','unknown')),
    expected_version INTEGER NOT NULL CHECK(expected_version > 0),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    CHECK(length(pack_id) > 0),
    CHECK(length(owner_work_id) > 0)
);

CREATE TABLE active_research_revisions (
    pack_id         TEXT NOT NULL,
    revision        INTEGER NOT NULL CHECK(revision > 0),
    question        TEXT NOT NULL,
    scope_in_json   TEXT NOT NULL CHECK(json_valid(scope_in_json)),
    scope_out_json  TEXT NOT NULL CHECK(json_valid(scope_out_json)),
    done_when_json  TEXT NOT NULL CHECK(json_valid(done_when_json)),
    method          TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision),
    FOREIGN KEY(pack_id) REFERENCES active_research_packs(pack_id) ON DELETE CASCADE,
    CHECK(length(question) > 0),
    CHECK(length(method) > 0)
);

CREATE TABLE active_research_findings (
    pack_id         TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    finding_id      TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK(kind IN ('observation','inference','hypothesis','conclusion','recommendation')),
    statement       TEXT NOT NULL,
    confidence      TEXT NOT NULL CHECK(confidence IN ('low','medium','high')),
    freshness       TEXT NOT NULL CHECK(freshness IN ('current','stale','unknown')),
    status          TEXT NOT NULL CHECK(status IN ('active','contradicted','superseded')),
    PRIMARY KEY(pack_id, revision, finding_id),
    FOREIGN KEY(pack_id, revision) REFERENCES active_research_revisions(pack_id, revision) ON DELETE CASCADE,
    CHECK(length(finding_id) > 0),
    CHECK(length(statement) > 0)
);

CREATE TABLE active_research_sources (
    pack_id         TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    source_id       TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK(kind IN ('official_doc','source_code','issue','paper','web','local_evidence')),
    locator         TEXT NOT NULL,
    title           TEXT NOT NULL,
    publisher_or_author TEXT NOT NULL,
    published_at    TEXT,
    accessed_at     TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision, source_id),
    FOREIGN KEY(pack_id, revision) REFERENCES active_research_revisions(pack_id, revision) ON DELETE CASCADE,
    CHECK(length(source_id) > 0),
    CHECK(length(locator) > 0),
    CHECK(length(title) > 0),
    CHECK(length(publisher_or_author) > 0),
    CHECK(length(accessed_at) > 0)
);

CREATE TABLE active_research_finding_sources (
    pack_id         TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    finding_id      TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision, finding_id, source_id),
    FOREIGN KEY(pack_id, revision, finding_id) REFERENCES active_research_findings(pack_id, revision, finding_id) ON DELETE CASCADE,
    FOREIGN KEY(pack_id, revision, source_id) REFERENCES active_research_sources(pack_id, revision, source_id) ON DELETE CASCADE
);

CREATE TABLE active_research_consumers (
    pack_id         TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    consumer_work_id TEXT NOT NULL,
    use_role        TEXT NOT NULL CHECK(use_role IN ('context','design_input','verification_basis','decision_basis')),
    required        INTEGER NOT NULL CHECK(required IN (0,1)),
    accepted_at     TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision, consumer_work_id),
    FOREIGN KEY(pack_id, revision) REFERENCES active_research_revisions(pack_id, revision) ON DELETE CASCADE,
    FOREIGN KEY(consumer_work_id) REFERENCES work_items(id) ON DELETE RESTRICT,
    CHECK(length(accepted_at) > 0)
);

CREATE INDEX active_research_consumers_by_work
    ON active_research_consumers(consumer_work_id, pack_id, revision);

CREATE TABLE epic_entries (
    epic_work_id   TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    child_work_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    position       INTEGER NOT NULL CHECK(position >= 0),
    required       INTEGER NOT NULL CHECK(required IN (0,1)),
    PRIMARY KEY(epic_work_id, child_work_id),
    UNIQUE(epic_work_id, position),
    CHECK(epic_work_id <> child_work_id)
);
CREATE INDEX epic_entries_by_child ON epic_entries(child_work_id, epic_work_id);
`,
	},
}

// schemaManifestDDL creates the manifest itself. It is applied before any
// migration and is not part of the versioned sequence.
const schemaManifestDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);`

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

// SchemaVersion reports the highest applied migration version, or zero when the
// manifest is empty.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "schema_version", "cannot read the schema manifest", true,
			"confirm the database is initialized", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// SchemaCompatibility is the read-only projection-schema manifest status. The
// migration manifest remains the authority; this API exposes its relationship
// to the binary without adding per-table version columns.
type SchemaCompatibility struct {
	CurrentVersion int  `json:"current_version"`
	AppliedVersion int  `json:"applied_version"`
	Compatible     bool `json:"compatible"`
	NeedsMigration bool `json:"needs_migration"`
}

// CurrentSchemaVersion returns the highest checksummed migration known to this
// binary.
func CurrentSchemaVersion() int {
	return migrations[len(migrations)-1].Version
}

// CheckSchemaCompatibility verifies the complete recorded manifest and reports
// whether this binary can operate on it. Newer or drifted manifests return the
// existing typed fail-closed migration errors.
func CheckSchemaCompatibility(ctx context.Context, db *sql.DB, supportedMax ...int) (SchemaCompatibility, error) {
	current := CurrentSchemaVersion()
	if len(supportedMax) > 0 {
		current = supportedMax[0]
	}
	compatibility := SchemaCompatibility{CurrentVersion: current}
	if db == nil {
		return compatibility, newFailure(KindUnavailable, "schema_compatibility", "database is not open", false, "open a database before checking schema compatibility")
	}
	applied, err := readAppliedMigrations(ctx, db)
	if err != nil {
		return compatibility, err
	}
	if err := checkManifest(applied); err != nil {
		return compatibility, err
	}
	if compatibility.CurrentVersion < CurrentSchemaVersion() {
		for version := range applied {
			if version > compatibility.CurrentVersion {
				return compatibility, newFailure(KindSchemaUnsupported, "schema_compatibility",
					fmt.Sprintf("the database records migration %d, which exceeds the caller-supported schema %d", version, compatibility.CurrentVersion), true,
					"upgrade the binary before opening this database")
			}
		}
	}
	for version := range applied {
		if version > compatibility.AppliedVersion {
			compatibility.AppliedVersion = version
		}
	}
	compatibility.NeedsMigration = compatibility.AppliedVersion < compatibility.CurrentVersion
	compatibility.Compatible = compatibility.AppliedVersion <= compatibility.CurrentVersion
	return compatibility, nil
}

// Migrate brings the database up to this binary's schema version. It is
// idempotent, applies the manifest and all pending steps in one transaction,
// and fails closed on drift or on a database written by a newer binary.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "migrate", "cannot begin schema migration", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}

	if _, err := tx.ExecContext(ctx, schemaManifestDDL); err != nil {
		return rollback(wrapFailure(KindUnavailable, "migrate", "cannot create the schema manifest", true,
			"check database permissions", err))
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if err := checkManifest(applied); err != nil {
		return rollback(err)
	}

	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if m.Version == 4 {
			if err := preflightMembershipMigration(ctx, tx); err != nil {
				return rollback(err)
			}
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return rollback(wrapFailure(KindUnavailable, "migrate",
				fmt.Sprintf("migration %d (%s) failed", m.Version, m.Name), false,
				"correct the migration definition", err))
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version, m.Name, m.checksum(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(wrapFailure(KindUnavailable, "migrate",
				fmt.Sprintf("cannot record migration %d (%s)", m.Version, m.Name), true,
				"retry once the database is writable", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return rollback(wrapFailure(KindUnavailable, "migrate", "cannot commit schema migration", true,
			"retry once the database is writable", err))
	}
	return nil
}

func preflightMembershipMigration(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"products", "projects", "work_items"} {
		var populated bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+")").Scan(&populated); err != nil {
			return wrapFailure(KindUnavailable, "migrate",
				"cannot inspect the "+table+" projection before migration 4", true,
				"confirm the database is readable", err)
		}
		if populated {
			return newFailure(KindMembershipMigrationRequired, "migrate",
				"migration 4 requires explicit memberships for the populated "+table+" projection",
				false,
				"run an explicit operator-mapped PM5 membership migration using stable IDs, or continue with the v3 binary; Concord must never infer memberships")
		}
	}
	return nil
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, wrapFailure(KindUnavailable, "migrate", "cannot read a manifest row", true,
				"confirm the database is readable", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	return applied, nil
}

func readAppliedMigrations(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "schema_compatibility", "cannot read the schema manifest", true,
			"confirm the database is initialized", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, wrapFailure(KindUnavailable, "schema_compatibility", "cannot read a manifest row", true,
				"confirm the database is readable", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "schema_compatibility", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	return applied, nil
}

// checkManifest compares the recorded manifest against this binary's
// definition. Both directions matter: an edited historical step means the live
// schema no longer matches the code, and an unknown newer step means the
// database was written by a binary that knows more than this one.
func checkManifest(applied map[int]string) error {
	known := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		known[m.Version] = m
	}

	for version, checksum := range applied {
		m, ok := known[version]
		if !ok {
			return newFailure(KindSchemaUnsupported, "migrate",
				fmt.Sprintf("the database records migration %d, which this binary does not define", version),
				true, "upgrade to a binary that defines this schema version and retry")
		}
		if m.checksum() != checksum {
			return newFailure(KindSchemaDrift, "migrate",
				fmt.Sprintf("migration %d (%s) no longer matches its recorded checksum", version, m.Name),
				false, "restore the original migration definition; applied migrations are immutable")
		}
	}
	return nil
}
