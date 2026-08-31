package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
    kind         TEXT NOT NULL CHECK(kind IN ('parent', 'includes', 'blocks', 'supersedes', 'implements')),
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
    contract_digest TEXT NOT NULL,
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
		Name:    "active_research_and_initiative_entries",
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

CREATE TABLE initiative_entries (
    initiative_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    child_work_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    position       INTEGER NOT NULL CHECK(position >= 0),
    required       INTEGER NOT NULL CHECK(required IN (0,1)),
    PRIMARY KEY(initiative_work_id, child_work_id),
    UNIQUE(initiative_work_id, position),
    CHECK(initiative_work_id <> child_work_id)
);
CREATE INDEX initiative_entries_by_child ON initiative_entries(child_work_id, initiative_work_id);
		`,
	},
	{
		Version: 9,
		Name:    "agent_authority_and_approvals",
		SQL: `
CREATE TABLE agent_clients (
    client_ref TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK(status IN ('active','revoked')),
    principal_ref TEXT NOT NULL,
    capabilities_json TEXT NOT NULL CHECK(json_valid(capabilities_json) AND json_type(capabilities_json)='array' AND json_array_length(capabilities_json) <= 32),
    product_scope_json TEXT NOT NULL CHECK(json_valid(product_scope_json) AND json_type(product_scope_json)='array' AND json_array_length(product_scope_json) <= 100),
    project_scope_json TEXT NOT NULL CHECK(json_valid(project_scope_json) AND json_type(project_scope_json)='array' AND json_array_length(project_scope_json) <= 100),
    created_at TEXT NOT NULL,
    rotated_at TEXT,
    revoked_at TEXT,
    CHECK(length(client_ref) > 0 AND length(client_ref) <= 128),
    CHECK(length(principal_ref) > 0 AND length(principal_ref) <= 128),
    CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL))
);

CREATE TABLE agent_client_keys (
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    key_id TEXT NOT NULL,
    public_key BLOB NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','revoked')),
    created_at TEXT NOT NULL,
    revoked_at TEXT,
    PRIMARY KEY(client_ref, key_id),
    UNIQUE(public_key),
    CHECK(length(key_id) > 0 AND length(key_id) <= 128),
    CHECK(length(public_key) = 32),
    CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL))
);
CREATE UNIQUE INDEX agent_client_one_active_key ON agent_client_keys(client_ref) WHERE status='active';

CREATE TABLE agent_nonce_replay (
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    nonce TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY(client_ref, nonce),
    CHECK(length(nonce) >= 16 AND length(nonce) <= 256),
    CHECK(length(observed_at) > 0 AND length(expires_at) > 0)
);
CREATE INDEX agent_nonce_replay_expiry ON agent_nonce_replay(expires_at);

CREATE TABLE agent_grants (
    grant_ref TEXT PRIMARY KEY,
    grant_hash BLOB NOT NULL UNIQUE,
    principal_ref TEXT NOT NULL,
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    session_ref TEXT NOT NULL,
    agent_ref TEXT NOT NULL,
    directory TEXT NOT NULL,
    worktree TEXT NOT NULL,
    client_key_id TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    capabilities_json TEXT NOT NULL CHECK(json_valid(capabilities_json) AND json_type(capabilities_json)='array'),
    product_scope_json TEXT NOT NULL CHECK(json_valid(product_scope_json) AND json_type(product_scope_json)='array'),
    project_scope_json TEXT NOT NULL CHECK(json_valid(project_scope_json) AND json_type(project_scope_json)='array'),
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    max_uses INTEGER NOT NULL DEFAULT 0 CHECK(max_uses >= 0 AND max_uses <= 1000000),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses OR max_uses=0),
    CHECK(length(grant_ref) = 64 AND length(grant_hash) = 32),
    CHECK(length(principal_ref) > 0 AND length(principal_ref) <= 128),
    CHECK(length(session_ref) > 0 AND length(session_ref) <= 128),
    CHECK(length(agent_ref) > 0 AND length(agent_ref) <= 128),
    CHECK(length(directory) > 0 AND length(directory) <= 4096),
    CHECK(length(worktree) > 0 AND length(worktree) <= 4096),
    CHECK(length(client_key_id) > 0 AND length(client_key_id) <= 128),
    CHECK(length(manifest_digest) = 71),
    CHECK(expires_at > issued_at)
);
CREATE INDEX agent_grants_lookup ON agent_grants(client_ref, session_ref, agent_ref);

CREATE TABLE agent_approvals (
    approval_ref TEXT PRIMARY KEY,
    operation_digest TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK(json_valid(scope_json) AND json_type(scope_json)='object'),
    version_json TEXT NOT NULL CHECK(json_valid(version_json) AND json_type(version_json)='object'),
    consequence TEXT NOT NULL CHECK(consequence IN ('read','intent','lifecycle','workflow_action','scope','relation','supersession','publication','recovery')),
    human_principal_ref TEXT NOT NULL,
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    session_ref TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    max_uses INTEGER NOT NULL CHECK(max_uses > 0 AND max_uses <= 100),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
    revoked_at TEXT,
    protected_evidence_ref TEXT NOT NULL,
    protected_evidence_digest TEXT NOT NULL,
    CHECK(length(approval_ref) = 64),
    CHECK(length(operation_digest) > 0 AND length(operation_digest) <= 128),
    CHECK(length(human_principal_ref) > 0 AND length(human_principal_ref) <= 128),
    CHECK(length(session_ref) > 0 AND length(session_ref) <= 128),
    CHECK(length(protected_evidence_ref) > 0 AND length(protected_evidence_ref) <= 256),
    CHECK(length(protected_evidence_digest) > 0 AND length(protected_evidence_digest) <= 128),
    CHECK(expires_at > issued_at)
);
CREATE INDEX agent_approvals_lookup ON agent_approvals(client_ref, session_ref, operation_digest);

CREATE TABLE agent_approval_challenges (
    challenge_ref TEXT PRIMARY KEY,
    grant_ref TEXT NOT NULL REFERENCES agent_grants(grant_ref) ON DELETE RESTRICT,
    operation_digest TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK(json_valid(scope_json) AND json_type(scope_json)='object'),
    version_json TEXT NOT NULL CHECK(json_valid(version_json) AND json_type(version_json)='object'),
    consequence TEXT NOT NULL CHECK(consequence IN ('read','intent','lifecycle','workflow_action','scope','relation','supersession','publication','recovery')),
    host_assertion_digest TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','consumed','revoked')),
    consumed_at TEXT,
    CHECK(length(challenge_ref) = 64),
    CHECK(length(operation_digest) > 0 AND length(operation_digest) <= 128),
    CHECK(length(host_assertion_digest) > 0 AND length(host_assertion_digest) <= 128),
    CHECK(expires_at > issued_at),
    CHECK((status='active' AND consumed_at IS NULL) OR (status IN ('consumed','revoked')))
);
CREATE INDEX agent_approval_challenges_grant ON agent_approval_challenges(grant_ref, status);

-- Stable Project identity is projected from locator events. A filesystem path
-- or remote is never the Project identity itself; each is replaceable evidence
-- for one stable Project ID.
CREATE TABLE project_locators (
    locator_id       TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    kind             TEXT NOT NULL CHECK(kind IN ('git_remote','canonical_path')),
    locator_value    TEXT NOT NULL,
    normalized_value TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    CHECK(length(locator_id) > 0 AND length(locator_id) <= 128),
    CHECK(length(locator_value) > 0 AND length(locator_value) <= 4096),
    CHECK(length(normalized_value) > 0 AND length(normalized_value) <= 4096),
    UNIQUE(kind, normalized_value)
);
CREATE INDEX project_locators_by_project ON project_locators(project_id, kind, normalized_value);
CREATE TRIGGER project_locators_guard_insert
BEFORE INSERT ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER project_locators_guard_update
BEFORE UPDATE ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER project_locators_guard_delete
BEFORE DELETE ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

-- Governing requirements bind to a Project scope (CD-0035 D2). They are not a
-- per-rule obligation field, which CD-0015 R0 forbids: a requirement is an
-- explicit scope-level declaration that work captured into the Project must
-- carry. Withdrawal appends its own event so a mistaken declaration is corrected
-- forward rather than hand-repaired.
CREATE TABLE project_governing_requirements (
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    requirement_ref TEXT NOT NULL,
    reason          TEXT NOT NULL,
    declared_at     TEXT NOT NULL,
    PRIMARY KEY (project_id, requirement_ref),
    CHECK(length(requirement_ref) > 0 AND length(requirement_ref) <= 128),
    CHECK(length(reason) > 0 AND length(reason) <= 1000)
);
CREATE INDEX project_governing_requirements_by_project ON project_governing_requirements(project_id, requirement_ref);
CREATE TRIGGER project_governing_requirements_guard_insert
BEFORE INSERT ON project_governing_requirements FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_governing_requirements is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER project_governing_requirements_guard_update
BEFORE UPDATE ON project_governing_requirements FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_governing_requirements is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER project_governing_requirements_guard_delete
BEFORE DELETE ON project_governing_requirements FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_governing_requirements is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;

-- One installation-scoped random key authenticates opaque pagination cursors.
-- It is authority state, not adapter state or a source-code secret.
CREATE TABLE agent_installation_keys (
    key_name  TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL CHECK(length(key_bytes) = 32),
    created_at TEXT NOT NULL
);

-- Grant context is retained as a structural snapshot for stale-context checks.
ALTER TABLE agent_grants ADD COLUMN scope_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_grants ADD COLUMN scope_snapshot_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_grants ADD COLUMN candidate_products_json TEXT NOT NULL DEFAULT '[]';
		`,
	},
	{
		Version: 10,
		Name:    "work_intent_and_membership_revisions",
		SQL: `
ALTER TABLE work_items ADD COLUMN intent_json TEXT NOT NULL DEFAULT '{}';
		`,
	},
	{
		Version: 11,
		Name:    "mutation_result_replay_payloads",
		SQL: `
ALTER TABLE idempotency_records ADD COLUMN result_payload TEXT NOT NULL DEFAULT '{}';
ALTER TABLE idempotency_records ADD COLUMN changed_refs TEXT NOT NULL DEFAULT '[]';
		`,
	},
	{
		Version: 12,
		Name:    "approval_challenge_use_bounds",
		SQL: `
ALTER TABLE agent_approval_challenges ADD COLUMN max_uses INTEGER NOT NULL DEFAULT 1 CHECK(max_uses > 0 AND max_uses <= 100);
ALTER TABLE agent_approval_challenges ADD COLUMN used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses);
		`,
	},
	{
		Version: 13,
		Name:    "idempotency_authorized_scope_snapshot",
		SQL: `
ALTER TABLE idempotency_records ADD COLUMN authorized_scope_snapshot TEXT NOT NULL DEFAULT '{}';
		`,
	},
	{
		Version: 14,
		Name:    "product_knowledge_homes",
		SQL: `
CREATE TABLE product_knowledge_homes (
    product_id TEXT PRIMARY KEY REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    locator_id TEXT NOT NULL REFERENCES project_locators(locator_id) ON DELETE RESTRICT,
    UNIQUE(project_id, locator_id)
);
`,
	},
	{
		Version: 15,
		Name:    "workflow_engine_projections",
		SQL: `
-- Forward-linked successors are still ordinary relation edges. Rebuild the
-- small relation table once so the accepted closed kind is represented in the
-- same projection and remains governed by the existing fold guard.
DROP TRIGGER relations_guard_insert;
DROP TRIGGER relations_guard_update;
DROP TRIGGER relations_guard_delete;
DROP INDEX idx_relations_from_kind;
DROP INDEX idx_relations_to_kind;
DROP INDEX relations_supersedes_target;
ALTER TABLE relations RENAME TO relations_v14;
CREATE TABLE relations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    work_id_from TEXT NOT NULL REFERENCES work_items(id),
    work_id_to   TEXT NOT NULL REFERENCES work_items(id),
    kind         TEXT NOT NULL CHECK(kind IN ('parent', 'includes', 'blocks', 'supersedes', 'implements', 'forward_link')),
    created_at   TEXT NOT NULL,
    CHECK(work_id_from <> work_id_to),
    UNIQUE(work_id_from, work_id_to, kind)
);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at)
    SELECT id,work_id_from,work_id_to,kind,created_at FROM relations_v14;
DROP TABLE relations_v14;
CREATE INDEX idx_relations_from_kind ON relations(work_id_from, kind, work_id_to);
CREATE INDEX idx_relations_to_kind ON relations(work_id_to, kind, work_id_from);
CREATE UNIQUE INDEX relations_supersedes_target ON relations(work_id_to) WHERE kind = 'supersedes';
CREATE TRIGGER relations_guard_insert BEFORE INSERT ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_update BEFORE UPDATE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_delete BEFORE DELETE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;

CREATE TABLE workflow_actors (
    actor_ref    TEXT PRIMARY KEY,
    principal_ref TEXT NOT NULL,
    client_ref   TEXT NOT NULL,
    agent_ref    TEXT NOT NULL,
    session_ref  TEXT NOT NULL,
    actor_class  TEXT NOT NULL CHECK(actor_class IN ('agent','operator')),
    first_seen_at TEXT NOT NULL,
    UNIQUE(principal_ref, client_ref, agent_ref, session_ref),
    CHECK(length(actor_ref) = 70 AND substr(actor_ref,1,6) = 'actor:'),
    CHECK(length(principal_ref) BETWEEN 2 AND 128),
    CHECK(length(client_ref) BETWEEN 2 AND 128),
    CHECK(length(agent_ref) BETWEEN 2 AND 128),
    CHECK(length(session_ref) BETWEEN 2 AND 128),
    CHECK(length(first_seen_at) > 0)
);

CREATE TABLE workflow_instances (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    definition_ref TEXT NOT NULL,
    definition_version INTEGER NOT NULL,
    definition_digest TEXT NOT NULL,
    current_step TEXT NOT NULL,
    instance_state TEXT NOT NULL CHECK(instance_state IN ('planned','ready','running','blocked','awaiting_condition','verifying','completed','cancelled','superseded')),
    execution_actor_ref TEXT REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    started_at TEXT,
    completed_at TEXT,
    last_checkpoint_at TEXT,
    CHECK(definition_version > 0 AND definition_version <= 2147483647),
    CHECK(length(definition_ref) BETWEEN 2 AND 128),
    CHECK(length(definition_digest) = 71 AND substr(definition_digest,1,7) = 'sha256:'),
    CHECK(length(current_step) BETWEEN 2 AND 128)
);

CREATE TABLE workflow_contracts (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    contract_version INTEGER NOT NULL,
    premise TEXT NOT NULL,
    outcome_kind TEXT NOT NULL CHECK(outcome_kind IN ('exists','absent','outcome','check')),
    outcome_payload TEXT NOT NULL CHECK(json_valid(outcome_payload) AND json_type(outcome_payload)='object'),
    consequence_class TEXT NOT NULL CHECK(consequence_class IN ('internal_sqlite','cross_authority','external_effect')),
    required_evidence TEXT NOT NULL CHECK(json_valid(required_evidence) AND json_type(required_evidence)='array'),
    route_conventions TEXT NOT NULL CHECK(json_valid(route_conventions) AND json_type(route_conventions)='array'),
    approved_at TEXT NOT NULL,
    approved_by TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    superseded_by INTEGER,
    spec_mandate TEXT NOT NULL CHECK(json_valid(spec_mandate) AND json_type(spec_mandate)='array'),
    PRIMARY KEY(work_id, contract_version),
    FOREIGN KEY(work_id, superseded_by) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT,
    CHECK(contract_version > 0 AND contract_version <= 2147483647),
    CHECK(length(premise) BETWEEN 1 AND 4096),
    CHECK(json_array_length(required_evidence) BETWEEN 0 AND 7),
    CHECK(json_array_length(route_conventions) BETWEEN 0 AND 16),
    CHECK(json_array_length(spec_mandate) BETWEEN 0 AND 32)
);

CREATE TABLE workflow_candidate_sets (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    candidate_kind TEXT NOT NULL CHECK(candidate_kind IN ('work_item','product','project')),
    candidate_ref TEXT NOT NULL,
    candidate_role TEXT NOT NULL CHECK(candidate_role IN ('include')),
    candidate_scope TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    recorded_by TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    PRIMARY KEY(work_id, contract_version, candidate_kind, candidate_ref),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT,
    CHECK(length(candidate_ref) BETWEEN 2 AND 128),
    CHECK(length(candidate_scope) BETWEEN 1 AND 4096)
);

CREATE TABLE workflow_checkpoints (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    checkpoint_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    step_kind TEXT NOT NULL CHECK(step_kind IN ('internal_sqlite','cross_authority','external_effect','human_checkpoint')),
    attempt_epoch INTEGER NOT NULL,
    accepted_inputs_digest TEXT NOT NULL,
    result_evidence_refs TEXT NOT NULL CHECK(json_valid(result_evidence_refs) AND json_type(result_evidence_refs)='array'),
    resume_cursor TEXT NOT NULL,
    idempotency_identity TEXT NOT NULL,
    actor_ref TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    request_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, checkpoint_id),
    UNIQUE(work_id, step_id, attempt_epoch),
    UNIQUE(work_id, idempotency_identity),
    CHECK(length(checkpoint_id) BETWEEN 2 AND 128),
    CHECK(length(step_id) BETWEEN 2 AND 128),
    CHECK(attempt_epoch > 0 AND attempt_epoch <= 2147483647),
    CHECK(length(accepted_inputs_digest) > 0),
    CHECK(json_array_length(result_evidence_refs) BETWEEN 0 AND 32),
    CHECK(length(resume_cursor) <= 2048),
    CHECK(length(idempotency_identity) BETWEEN 2 AND 128),
    CHECK(length(request_id) > 0)
);

CREATE TABLE workflow_external_conditions (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    condition_id TEXT NOT NULL,
    await_type TEXT NOT NULL CHECK(await_type IN ('pr_merge','ci_result','timer','human_approval','remote_work_state')),
    await_ref TEXT NOT NULL,
    resolution_authority TEXT NOT NULL,
    condition_state TEXT NOT NULL CHECK(condition_state IN ('open','resolved','cancelled')),
    resolution_evidence TEXT,
    resolved_by_event TEXT,
    cancellation_authority TEXT,
    cancellation_evidence TEXT,
    cancelled_by_event TEXT,
    recorded_at TEXT NOT NULL,
    resolved_at TEXT,
    cancelled_at TEXT,
    PRIMARY KEY(work_id, condition_id),
    CHECK(length(condition_id) BETWEEN 2 AND 128),
    CHECK(length(await_ref) BETWEEN 2 AND 128),
    CHECK(length(resolution_authority) BETWEEN 2 AND 128),
    CHECK((condition_state='open' AND resolution_evidence IS NULL AND resolved_by_event IS NULL AND cancellation_authority IS NULL AND cancellation_evidence IS NULL AND cancelled_by_event IS NULL AND resolved_at IS NULL AND cancelled_at IS NULL)
       OR (condition_state='resolved' AND resolution_evidence IS NOT NULL AND resolved_by_event IS NOT NULL AND cancellation_authority IS NULL AND cancellation_evidence IS NULL AND cancelled_by_event IS NULL AND resolved_at IS NOT NULL AND cancelled_at IS NULL)
       OR (condition_state='cancelled' AND resolution_evidence IS NULL AND resolved_by_event IS NULL AND cancellation_authority='operator' AND cancellation_evidence IS NOT NULL AND cancelled_by_event IS NOT NULL AND resolved_at IS NULL AND cancelled_at IS NOT NULL))
);

CREATE TABLE workflow_impact_edges (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    edge_id TEXT NOT NULL,
    edge_kind TEXT NOT NULL CHECK(edge_kind IN ('modifies','depends_on','forward_link')),
    edge_class TEXT NOT NULL CHECK(edge_class IN ('hard','soft','none')),
    target_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    target_kind TEXT NOT NULL CHECK(target_kind IN ('work_item')),
    severity TEXT NOT NULL CHECK(severity IN ('breaking','non-breaking','informational')),
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, edge_id),
    CHECK(length(edge_id) BETWEEN 2 AND 128),
    CHECK(work_id <> target_work_id OR edge_kind = 'modifies')
);

CREATE TABLE workflow_impact_notices (
    notice_id TEXT PRIMARY KEY,
    source_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    source_contract_version INTEGER NOT NULL,
    entity_kind TEXT NOT NULL,
    entity_ref TEXT NOT NULL,
    target_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    edge_id TEXT NOT NULL,
    old_hash TEXT,
    new_hash TEXT,
    severity TEXT NOT NULL CHECK(severity IN ('breaking','non-breaking','informational')),
    recorded_at TEXT NOT NULL,
    UNIQUE(source_work_id, source_contract_version, entity_kind, entity_ref, target_work_id, severity),
    FOREIGN KEY(source_work_id, edge_id) REFERENCES workflow_impact_edges(work_id, edge_id) ON DELETE RESTRICT,
    CHECK(length(notice_id) = 71 AND substr(notice_id,1,7) = 'notice:'),
    CHECK(source_contract_version > 0),
    CHECK(length(entity_kind) BETWEEN 2 AND 128),
    CHECK(length(entity_ref) BETWEEN 2 AND 128),
    CHECK(length(edge_id) BETWEEN 2 AND 128)
);

CREATE TABLE workflow_decision_records (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    question TEXT NOT NULL,
    options_considered TEXT NOT NULL CHECK(json_valid(options_considered) AND json_type(options_considered)='array'),
    decision TEXT NOT NULL CHECK(decision IN ('accepted_decision','insufficient_evidence')),
    rationale TEXT NOT NULL,
    consequences TEXT NOT NULL CHECK(json_valid(consequences) AND json_type(consequences)='array'),
    inputs TEXT NOT NULL CHECK(json_valid(inputs) AND json_type(inputs)='array'),
    poc_findings TEXT NOT NULL,
    supersedes TEXT,
    superseded_by TEXT,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, question),
    CHECK(length(question) BETWEEN 1 AND 4096),
    CHECK(json_array_length(options_considered) BETWEEN 1 AND 16),
    CHECK(length(rationale) BETWEEN 1 AND 4096),
    CHECK(json_array_length(consequences) BETWEEN 1 AND 16),
    CHECK(json_array_length(inputs) BETWEEN 1 AND 32),
    CHECK(length(poc_findings) BETWEEN 1 AND 4096)
);

CREATE TABLE workflow_premise_confirmations (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    confirmed_by TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    confirmed_at TEXT NOT NULL,
    PRIMARY KEY(work_id, contract_version),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT,
    CHECK(contract_version > 0)
);

CREATE INDEX workflow_instances_state ON workflow_instances(instance_state, work_id);
CREATE INDEX workflow_conditions_state ON workflow_external_conditions(work_id, condition_state);
CREATE INDEX workflow_impact_edges_target ON workflow_impact_edges(target_work_id, edge_kind, edge_class);
CREATE INDEX workflow_notices_target ON workflow_impact_notices(target_work_id, severity);

CREATE TRIGGER workflow_actors_guard_insert BEFORE INSERT ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_actors_guard_update BEFORE UPDATE ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is immutable') ; END;
CREATE TRIGGER workflow_actors_guard_delete BEFORE DELETE ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is immutable') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_insert BEFORE INSERT ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_update BEFORE UPDATE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_delete BEFORE DELETE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_insert BEFORE INSERT ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_update BEFORE UPDATE ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_delete BEFORE DELETE ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_candidate_sets_guard_insert BEFORE INSERT ON workflow_candidate_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_candidate_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_candidate_sets_guard_update BEFORE UPDATE ON workflow_candidate_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_candidate_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_candidate_sets_guard_delete BEFORE DELETE ON workflow_candidate_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_candidate_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_checkpoints_guard_insert BEFORE INSERT ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_checkpoints_guard_update BEFORE UPDATE ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_checkpoints_guard_delete BEFORE DELETE ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_external_conditions_guard_insert BEFORE INSERT ON workflow_external_conditions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_external_conditions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_external_conditions_guard_update BEFORE UPDATE ON workflow_external_conditions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_external_conditions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_external_conditions_guard_delete BEFORE DELETE ON workflow_external_conditions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_external_conditions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_insert BEFORE INSERT ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_update BEFORE UPDATE ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_delete BEFORE DELETE ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_notices_guard_insert BEFORE INSERT ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_notices_guard_update BEFORE UPDATE ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_notices_guard_delete BEFORE DELETE ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_insert BEFORE INSERT ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_update BEFORE UPDATE ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_delete BEFORE DELETE ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_insert BEFORE INSERT ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_update BEFORE UPDATE ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_delete BEFORE DELETE ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 16,
		Name:    "durable_operation_contract_digest",
		SQL: `
SELECT 1;
`,
	},
	{
		Version: 17,
		Name:    "workflow_staleness_warnings",
		SQL: `
CREATE TABLE workflow_staleness_warnings (
    work_id     TEXT NOT NULL,
    rule_id     TEXT NOT NULL,
    severity    TEXT NOT NULL CHECK (severity IN ('warning','block')),
    observed_at TEXT NOT NULL,
    PRIMARY KEY (work_id, rule_id)
);
CREATE INDEX workflow_staleness_warnings_work ON workflow_staleness_warnings(work_id, observed_at);
		`,
	},
	{
		Version: 18,
		Name:    "workflow_contract_rigor_class",
		SQL: `
ALTER TABLE workflow_contracts ADD COLUMN rigor_class TEXT NOT NULL DEFAULT 'prototype_internal';
		`,
	},
	{
		Version: 19,
		Name:    "durable_knowledge_kind_coverage",
		SQL: `
ALTER TABLE archived_work ADD COLUMN scope_mode TEXT NOT NULL DEFAULT 'explicit' CHECK(scope_mode IN ('home','explicit'));

CREATE TABLE knowledge_kind_coverage (
    home_project_id     TEXT NOT NULL,
    home_locator_id     TEXT NOT NULL,
    head_ref            TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK(kind IN ('work_note','lesson','decision','spec','research')),
    coverage            TEXT NOT NULL CHECK(coverage IN ('indexed','supported_not_indexed')),
    reason              TEXT NOT NULL,
    scanned_commit_oid  TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, head_ref, kind)
);
CREATE INDEX knowledge_kind_coverage_lookup ON knowledge_kind_coverage(home_project_id, home_locator_id, head_ref, kind);
CREATE TRIGGER knowledge_kind_coverage_guard_insert
BEFORE INSERT ON knowledge_kind_coverage FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_kind_coverage is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1);
END;
CREATE TRIGGER knowledge_kind_coverage_guard_update
BEFORE UPDATE ON knowledge_kind_coverage FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_kind_coverage is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1);
END;
CREATE TRIGGER knowledge_kind_coverage_guard_delete
BEFORE DELETE ON knowledge_kind_coverage FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'knowledge_kind_coverage is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1);
END;
		`,
	},
	{
		Version: 20,
		Name:    "project_stage_overrides",
		SQL: `
ALTER TABLE projects ADD COLUMN stage_maturity_override TEXT
    CHECK(stage_maturity_override IS NULL OR stage_maturity_override IN ('prototype','alpha','beta','production','deprecated'));
ALTER TABLE projects ADD COLUMN stage_audience_commitment_override TEXT
    CHECK(stage_audience_commitment_override IS NULL OR stage_audience_commitment_override IN ('operator_only','limited','public'));

CREATE TRIGGER projects_stage_override_pair_insert
BEFORE INSERT ON projects FOR EACH ROW
WHEN (NEW.stage_maturity_override IS NULL) <> (NEW.stage_audience_commitment_override IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'Project stage override must contain both maturity and audience commitment');
END;
CREATE TRIGGER projects_stage_override_pair_update
BEFORE UPDATE OF stage_maturity_override, stage_audience_commitment_override ON projects FOR EACH ROW
WHEN (NEW.stage_maturity_override IS NULL) <> (NEW.stage_audience_commitment_override IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'Project stage override must contain both maturity and audience commitment');
END;

CREATE INDEX products_display_name_order
    ON products(display_name, id);
		`,
	},
	{
		Version: 21,
		Name:    "typed_law_relations_and_workflow_amendments",
		SQL: `
ALTER TABLE workflow_contracts ADD COLUMN law_modifies TEXT NOT NULL DEFAULT '[]'
    CHECK(json_valid(law_modifies) AND json_type(law_modifies)='array' AND json_array_length(law_modifies) BETWEEN 0 AND 32);
ALTER TABLE workflow_contracts ADD COLUMN law_boundary_version INTEGER NOT NULL DEFAULT 0
    CHECK(law_boundary_version IN (0,1));

CREATE TABLE law_subjects (
    home_project_id    TEXT NOT NULL,
    home_locator_id    TEXT NOT NULL,
    law_id             TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK(kind IN ('decision','spec')),
    status             TEXT NOT NULL CHECK(status IN ('accepted','superseded')),
    path               TEXT NOT NULL,
    title              TEXT NOT NULL,
    content_hash       TEXT NOT NULL CHECK(length(content_hash)=71 AND substr(content_hash,1,7)='sha256:'),
    scanned_commit_oid TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, law_id)
);

CREATE TABLE law_relations (
    home_project_id    TEXT NOT NULL,
    home_locator_id    TEXT NOT NULL,
    source_law_id      TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK(kind IN ('supersedes','refines','subordinate_to','conflicts_with')),
    target_law_id      TEXT NOT NULL,
    scanned_commit_oid TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, source_law_id, kind, target_law_id),
    CHECK(source_law_id <> target_law_id),
    FOREIGN KEY(home_project_id, home_locator_id, source_law_id) REFERENCES law_subjects(home_project_id, home_locator_id, law_id) ON DELETE RESTRICT,
    FOREIGN KEY(home_project_id, home_locator_id, target_law_id) REFERENCES law_subjects(home_project_id, home_locator_id, law_id) ON DELETE RESTRICT
);
CREATE INDEX law_subjects_lookup ON law_subjects(home_project_id, home_locator_id, status, law_id);
CREATE INDEX law_relations_target ON law_relations(home_project_id, home_locator_id, kind, target_law_id);
CREATE UNIQUE INDEX law_relations_conflict_pair ON law_relations(
    home_project_id, home_locator_id, kind,
    CASE WHEN kind='conflicts_with' THEN min(source_law_id,target_law_id) ELSE source_law_id END,
    CASE WHEN kind='conflicts_with' THEN max(source_law_id,target_law_id) ELSE target_law_id END
);

CREATE TRIGGER law_subjects_guard_insert BEFORE INSERT ON law_subjects FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_subjects is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_subjects_guard_update BEFORE UPDATE ON law_subjects FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_subjects is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_subjects_guard_delete BEFORE DELETE ON law_subjects FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_subjects is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_relations_guard_insert BEFORE INSERT ON law_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_relations_guard_update BEFORE UPDATE ON law_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_relations_guard_delete BEFORE DELETE ON law_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 22,
		Name:    "workflow_context_continuity",
		SQL: `
CREATE TABLE workflow_context_checkpoints (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL,
    checkpoint_sequence INTEGER NOT NULL,
    checkpoint_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    attempt_epoch INTEGER NOT NULL,
    active_unit TEXT NOT NULL,
    hypothesis TEXT NOT NULL,
    diagnosis TEXT NOT NULL,
    strategy TEXT NOT NULL,
    touched_refs TEXT NOT NULL CHECK(json_valid(touched_refs) AND json_type(touched_refs)='array'),
    evidence_refs TEXT NOT NULL CHECK(json_valid(evidence_refs) AND json_type(evidence_refs)='array'),
    pending_questions TEXT NOT NULL CHECK(json_valid(pending_questions) AND json_type(pending_questions)='array'),
    pending_decisions TEXT NOT NULL CHECK(json_valid(pending_decisions) AND json_type(pending_decisions)='array'),
    workflow_ref TEXT NOT NULL,
    workflow_definition_version INTEGER NOT NULL,
    workflow_definition_digest TEXT NOT NULL,
    actor_ref TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    request_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, work_version, checkpoint_sequence),
    UNIQUE(work_id, checkpoint_id),
    CHECK(work_version > 0),
    CHECK(checkpoint_sequence > 0),
    CHECK(attempt_epoch > 0),
    CHECK(length(checkpoint_id) BETWEEN 2 AND 128),
    CHECK(length(step_id) BETWEEN 2 AND 128),
    CHECK(length(active_unit) BETWEEN 2 AND 256),
    CHECK(length(hypothesis) BETWEEN 2 AND 4096),
    CHECK(length(diagnosis) BETWEEN 2 AND 4096),
    CHECK(length(strategy) BETWEEN 2 AND 4096),
    CHECK(json_array_length(touched_refs) BETWEEN 1 AND 64),
    CHECK(json_array_length(evidence_refs) BETWEEN 1 AND 64),
    CHECK(json_array_length(pending_questions) BETWEEN 0 AND 16),
    CHECK(json_array_length(pending_decisions) BETWEEN 0 AND 16),
    CHECK(length(workflow_ref) BETWEEN 2 AND 128),
    CHECK(workflow_definition_version > 0),
    CHECK(length(workflow_definition_digest) = 71 AND substr(workflow_definition_digest,1,7)='sha256:'),
    CHECK(length(request_id) > 0)
);
CREATE INDEX workflow_context_checkpoints_latest ON workflow_context_checkpoints(work_id, checkpoint_sequence DESC);

CREATE TABLE workflow_context_boundaries (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL,
    boundary_sequence INTEGER NOT NULL,
    boundary_count INTEGER NOT NULL,
    boundary_id TEXT NOT NULL,
    boundary_kind TEXT NOT NULL CHECK(boundary_kind IN ('summary','restart')),
    checkpoint_id TEXT NOT NULL,
    checkpoint_sequence INTEGER NOT NULL,
    attempt_epoch INTEGER NOT NULL,
    summary TEXT NOT NULL,
    workflow_ref TEXT NOT NULL,
    workflow_definition_version INTEGER NOT NULL,
    workflow_definition_digest TEXT NOT NULL,
    typed_agent_type TEXT,
    typed_agent_version TEXT,
    typed_agent_ruleset_digest TEXT,
    actor_ref TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    request_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, boundary_sequence),
    UNIQUE(work_id, boundary_id),
    CHECK(work_version > 0),
    CHECK(boundary_sequence > 0 AND boundary_count = boundary_sequence),
    CHECK(length(boundary_id) BETWEEN 2 AND 128),
    CHECK(boundary_kind='summary' AND length(summary) BETWEEN 1 AND 16384),
    CHECK(boundary_kind='restart' OR (typed_agent_type IS NULL AND typed_agent_version IS NULL AND typed_agent_ruleset_digest IS NULL)),
    CHECK(attempt_epoch > 0),
    CHECK(length(checkpoint_id) BETWEEN 2 AND 128),
    CHECK(checkpoint_sequence > 0),
    CHECK(length(workflow_ref) BETWEEN 2 AND 128),
    CHECK(workflow_definition_version > 0),
    CHECK(length(workflow_definition_digest) = 71 AND substr(workflow_definition_digest,1,7)='sha256:'),
    CHECK(length(request_id) > 0)
);
CREATE INDEX workflow_context_boundaries_history ON workflow_context_boundaries(work_id, boundary_sequence DESC);
CREATE TRIGGER workflow_context_checkpoints_guard_insert BEFORE INSERT ON workflow_context_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_checkpoints is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_context_checkpoints_guard_update BEFORE UPDATE ON workflow_context_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_checkpoints is immutable'); END;
CREATE TRIGGER workflow_context_checkpoints_guard_delete BEFORE DELETE ON workflow_context_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_checkpoints is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_context_boundaries_guard_insert BEFORE INSERT ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_context_boundaries_guard_update BEFORE UPDATE ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is immutable'); END;
CREATE TRIGGER workflow_context_boundaries_guard_delete BEFORE DELETE ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 23,
		Name:    "initiative_narrative",
		SQL: `
ALTER TABLE work_items ADD COLUMN narrative TEXT NOT NULL DEFAULT ''
    CHECK(length(narrative) <= 16384);
		`,
	},
	{
		Version: 24,
		Name:    "worker_attempt_evidence",
		SQL: `
CREATE TABLE worker_attempts (
    work_id TEXT NOT NULL,
    attempt_id TEXT PRIMARY KEY,
    lane_id TEXT NOT NULL,
    lane_version INTEGER NOT NULL,
    lane_digest TEXT NOT NULL,
    capability_class TEXT NOT NULL,
    routing_policy_version TEXT NOT NULL,
    resolved_model TEXT NOT NULL,
    readback_model TEXT NOT NULL,
    packet_schema_version TEXT NOT NULL,
    report_schema_version TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL CHECK(lifecycle_state IN ('dispatched','completed','failed')),
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL,
    completed_at TEXT,
    failed_at TEXT,
    CHECK(length(work_id) > 0),
    CHECK(length(attempt_id) BETWEEN 2 AND 128),
    CHECK(length(lane_id) BETWEEN 2 AND 32),
    CHECK(lane_version > 0),
    CHECK(length(lane_digest) = 71 AND substr(lane_digest,1,7)='sha256:'),
    CHECK(length(capability_class) BETWEEN 2 AND 64),
    CHECK(length(routing_policy_version) BETWEEN 1 AND 64),
    CHECK(length(resolved_model) BETWEEN 3 AND 128),
    CHECK(length(readback_model) <= 128),
    CHECK(packet_schema_version = '1.0'),
    CHECK(report_schema_version = '1.0'),
    CHECK((lifecycle_state='dispatched' AND completed_at IS NULL AND failed_at IS NULL) OR
          (lifecycle_state='completed' AND completed_at IS NOT NULL AND failed_at IS NULL AND length(readback_model) >= 3 AND failure_kind='') OR
          (lifecycle_state='failed' AND failed_at IS NOT NULL AND completed_at IS NULL AND length(readback_model) >= 3 AND length(failure_kind) > 0))
);
CREATE INDEX worker_attempts_work ON worker_attempts(work_id, dispatched_at, attempt_id);
CREATE TRIGGER worker_attempts_guard_insert BEFORE INSERT ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_update BEFORE UPDATE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_delete BEFORE DELETE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 25,
		Name:    "worker_routing_resolution_evidence",
		SQL: `
ALTER TABLE worker_attempts ADD COLUMN routing_policy_digest TEXT NOT NULL
    DEFAULT 'sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736'
    CHECK(length(routing_policy_digest) = 71 AND substr(routing_policy_digest,1,7)='sha256:');
ALTER TABLE worker_attempts ADD COLUMN resolution_role TEXT NOT NULL DEFAULT 'preferred'
    CHECK(resolution_role IN ('preferred','fallback'));
ALTER TABLE worker_attempts ADD COLUMN fallback_reason TEXT NOT NULL DEFAULT ''
    CHECK((resolution_role='preferred' AND fallback_reason='') OR
          (resolution_role='fallback' AND fallback_reason IN ('rate_limit','provider_unavailable','budget_exhausted','other')));
		`,
	},
	{
		Version: 26,
		Name:    "declared_urgency_and_provenance",
		SQL: `
ALTER TABLE work_items ADD COLUMN urgency TEXT NOT NULL DEFAULT 'standard'
    CHECK(urgency IN ('standard', 'expedite'));
DROP TRIGGER relations_guard_insert;
DROP TRIGGER relations_guard_update;
DROP TRIGGER relations_guard_delete;
DROP INDEX idx_relations_from_kind;
DROP INDEX idx_relations_to_kind;
DROP INDEX relations_supersedes_target;
ALTER TABLE relations RENAME TO relations_v26;
CREATE TABLE relations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    work_id_from TEXT NOT NULL REFERENCES work_items(id),
    work_id_to   TEXT NOT NULL REFERENCES work_items(id),
    kind         TEXT NOT NULL CHECK(kind IN ('parent', 'includes', 'blocks', 'supersedes', 'implements', 'forward_link', 'raised_from')),
    created_at   TEXT NOT NULL,
    CHECK(work_id_from <> work_id_to),
    UNIQUE(work_id_from, work_id_to, kind)
);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at)
    SELECT id,work_id_from,work_id_to,kind,created_at FROM relations_v26;
DROP TABLE relations_v26;
CREATE INDEX idx_relations_from_kind ON relations(work_id_from, kind, work_id_to);
CREATE INDEX idx_relations_to_kind ON relations(work_id_to, kind, work_id_from);
CREATE UNIQUE INDEX relations_supersedes_target ON relations(work_id_to) WHERE kind = 'supersedes';
CREATE TRIGGER relations_guard_insert BEFORE INSERT ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_update BEFORE UPDATE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_delete BEFORE DELETE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
		`,
	},
	{
		Version: 27,
		Name:    "workflow_executing_model_identity",
		SQL: `
-- CD-0017 D6 evaluates distinctness against readback executing-model identity.
-- The model is a property of one run, not of an actor: workflow_actors is an
-- identity table whose actor_ref is derived from its unique four-tuple, so the
-- observed model is recorded where the actor acts instead.
ALTER TABLE workflow_instances ADD COLUMN execution_model TEXT NOT NULL DEFAULT ''
    CHECK(length(execution_model) <= 128);
		`,
	},
	{
		Version: 28,
		Name:    "workflow_impact_notice_edge_owner",
		SQL: `
-- Impact edges are declared by the dependent work. Notice source and edge owner
-- therefore differ when a completed source notifies reverse dependents.
DROP TRIGGER workflow_impact_notices_guard_insert;
DROP TRIGGER workflow_impact_notices_guard_update;
DROP TRIGGER workflow_impact_notices_guard_delete;
DROP INDEX workflow_notices_target;
ALTER TABLE workflow_impact_notices RENAME TO workflow_impact_notices_v28;
CREATE TABLE workflow_impact_notices (
    notice_id TEXT PRIMARY KEY,
    source_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    source_contract_version INTEGER NOT NULL,
    entity_kind TEXT NOT NULL,
    entity_ref TEXT NOT NULL,
    target_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    edge_owner_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    edge_id TEXT NOT NULL,
    old_hash TEXT,
    new_hash TEXT,
    severity TEXT NOT NULL CHECK(severity IN ('breaking','non-breaking','informational')),
    recorded_at TEXT NOT NULL,
    UNIQUE(source_work_id, source_contract_version, entity_kind, entity_ref, target_work_id, severity),
    FOREIGN KEY(edge_owner_work_id, edge_id) REFERENCES workflow_impact_edges(work_id, edge_id) ON DELETE RESTRICT,
    CHECK(length(notice_id) = 71 AND substr(notice_id,1,7) = 'notice:'),
    CHECK(source_contract_version > 0),
    CHECK(length(entity_kind) BETWEEN 2 AND 128),
    CHECK(length(entity_ref) BETWEEN 2 AND 128),
    CHECK(length(edge_owner_work_id) BETWEEN 2 AND 128),
    CHECK(length(edge_id) BETWEEN 2 AND 128)
);
INSERT INTO workflow_impact_notices(notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_owner_work_id,edge_id,old_hash,new_hash,severity,recorded_at)
SELECT notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,source_work_id,edge_id,old_hash,new_hash,severity,recorded_at
FROM workflow_impact_notices_v28;
DROP TABLE workflow_impact_notices_v28;
CREATE INDEX workflow_notices_target ON workflow_impact_notices(target_work_id, severity);
CREATE TRIGGER workflow_impact_notices_guard_insert BEFORE INSERT ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_notices_guard_update BEFORE UPDATE ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_notices_guard_delete BEFORE DELETE ON workflow_impact_notices FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_notices is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 29,
		Name:    "research_finding_applies_to_scope",
		SQL: `
-- Durable knowledge declares what it applies to; active research did not, so a
-- finding could only say which work item produced it. Promotion at archive was
-- therefore unassisted judgement. Findings now carry the same scope vocabulary
-- as durable records: home means it applies to its owner's home broadly,
-- explicit means it applies to exactly the declared scopes.
--
-- Durable knowledge spreads its scope IDs across four tables. One table with a
-- closed scope_kind is equivalent in strength here (closed enum, composite key,
-- cascade delete) without adding four more tables to an already table-heavy
-- subsystem. The declared shape, not the table count, is what has to match.
ALTER TABLE active_research_findings ADD COLUMN scope_mode TEXT NOT NULL DEFAULT 'home' CHECK(scope_mode IN ('home','explicit'));

CREATE TABLE active_research_finding_scopes (
    pack_id     TEXT NOT NULL,
    revision    INTEGER NOT NULL,
    finding_id  TEXT NOT NULL,
    scope_kind  TEXT NOT NULL CHECK(scope_kind IN ('product','project','component','tag')),
    scope_id    TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision, finding_id, scope_kind, scope_id),
    FOREIGN KEY(pack_id, revision, finding_id) REFERENCES active_research_findings(pack_id, revision, finding_id) ON DELETE CASCADE,
    CHECK(length(scope_id) > 0)
);

CREATE INDEX active_research_finding_scopes_lookup
    ON active_research_finding_scopes(scope_kind, scope_id, pack_id, revision);

-- The home-implies-empty invariant is structural, matching the strength of the
-- durable-side check rather than relying on the mutation path to remember it.
CREATE TRIGGER active_research_finding_scopes_home_guard
BEFORE INSERT ON active_research_finding_scopes FOR EACH ROW
WHEN (SELECT scope_mode FROM active_research_findings WHERE pack_id=NEW.pack_id AND revision=NEW.revision AND finding_id=NEW.finding_id) = 'home'
BEGIN
    SELECT RAISE(ABORT, 'home scope cannot carry explicit scope IDs');
END;

CREATE TRIGGER active_research_findings_home_guard_update
BEFORE UPDATE OF scope_mode ON active_research_findings FOR EACH ROW
WHEN NEW.scope_mode = 'home' AND EXISTS(SELECT 1 FROM active_research_finding_scopes s WHERE s.pack_id=NEW.pack_id AND s.revision=NEW.revision AND s.finding_id=NEW.finding_id)
BEGIN
    SELECT RAISE(ABORT, 'home scope cannot carry explicit scope IDs');
END;
		`,
	},
	{
		Version: 30,
		Name:    "worktree_claims_and_entries",
		SQL: `
-- CD-0008 D1: worktree creation is one durable cross-authority operation.
-- worktree_claims is operational state (like durable_operations): the atomic
-- claim pinning intent before native git creation, reconciled by op id.
CREATE TABLE worktree_claims (
    op_id TEXT NOT NULL PRIMARY KEY,
    work_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    set_id TEXT NOT NULL,
    pinned_branch TEXT NOT NULL,
    pinned_base_sha TEXT NOT NULL CHECK(length(pinned_base_sha) >= 40 AND length(pinned_base_sha) <= 64),
    pinned_path TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending','verified','reclaimed')),
    principal_ref TEXT NOT NULL,
    request_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX worktree_claims_one_active ON worktree_claims(work_id, project_id)
    WHERE state IN ('pending','verified');

-- worktree_entries is the folded domain state: one verified implementation
-- worktree per Project per set (CD-0008 D1).
CREATE TABLE worktree_entries (
    set_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    claim_op_id TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    path TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('active','reclaimed')),
    verified_at TEXT NOT NULL,
    reclaimed_at TEXT,
    git_facts TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY(set_id, project_id)
);
`,
	},

	{
		Version: 31,
		Name:    "resource_claims",
		SQL: `
-- CD-0028: one durable claim per typed resource key. Fold-only projection;
-- work.resource_claimed / work.resource_claim_released own every write.
CREATE TABLE resource_claims (
    resource_key TEXT PRIMARY KEY,
    holder_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    holder_agent TEXT NOT NULL,
    holder_session TEXT NOT NULL,
    reason TEXT NOT NULL CHECK(length(reason) > 0 AND length(reason) <= 512),
    state TEXT NOT NULL CHECK(state IN ('held','released')),
    claimed_at TEXT NOT NULL,
    released_at TEXT,
    CHECK(length(holder_agent) BETWEEN 2 AND 128),
    CHECK(length(holder_session) BETWEEN 2 AND 128)
);
CREATE TRIGGER resource_claims_guard_insert BEFORE INSERT ON resource_claims FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_claims is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER resource_claims_guard_update BEFORE UPDATE ON resource_claims FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_claims is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER resource_claims_guard_delete BEFORE DELETE ON resource_claims FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_claims is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
`,
	},

	{
		Version: 32,
		Name:    "revision_freshness_authoritative",
		SQL: `
-- Issue #122 / CD-0009 D6: freshness is authoritative at the revision
-- consumers pin, not the pack. The pack column remains as a display summary;
-- nothing gates on it. Backfill: every existing revision inherits its pack's
-- state at migration time.
ALTER TABLE active_research_revisions ADD COLUMN freshness TEXT NOT NULL DEFAULT 'current' CHECK(freshness IN ('current','stale','unknown'));
UPDATE active_research_revisions SET freshness = (SELECT p.freshness FROM active_research_packs p WHERE p.pack_id = active_research_revisions.pack_id);
`,
	},

	{
		Version: 33,
		Name:    "work_messages",
		SQL: `
-- CD-0029: durable peer messages addressed to work items. Fold-only; the
-- work.message_sent / work.message_withdrawn folds own every write.
CREATE TABLE work_messages (
    message_id TEXT PRIMARY KEY CHECK(length(message_id) = 36 AND substr(message_id,1,4) = 'msg:'),
    sender_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    recipient_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    body TEXT NOT NULL CHECK(length(body) > 0 AND length(body) <= 4096),
    state TEXT NOT NULL CHECK(state IN ('sent','withdrawn')),
    sent_at TEXT NOT NULL,
    withdrawn_at TEXT,
    CHECK(recipient_work_id != sender_work_id)
);
CREATE INDEX work_messages_recipient ON work_messages(recipient_work_id, state, sent_at);
CREATE TRIGGER work_messages_guard_insert BEFORE INSERT ON work_messages FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_messages is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER work_messages_guard_update BEFORE UPDATE ON work_messages FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_messages is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER work_messages_guard_delete BEFORE DELETE ON work_messages FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_messages is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
`,
	},

	{
		Version: 34,
		Name:    "await_expectation_bounds",
		SQL: `
-- Issue #87: a step delegating completion to an external actor declares how
-- long the wait is expected to take; exceeding it is derived at read time.
-- No timer, no state change — elapsed time never creates authority.
ALTER TABLE workflow_external_conditions ADD COLUMN expected_within_seconds INTEGER
    CHECK(expected_within_seconds IS NULL OR expected_within_seconds > 0);
`,
	},

	{
		Version: 35,
		Name:    "worker_undeclared_resolution_role",
		SQL: `
-- Issue #106: the undeclared role records terminal evidence for a model that
-- ran outside the declared resolution set, or an exhausted chain where no
-- model ran. Such rows are born failed and can never bind a completion, so
-- the empty-model and empty-readback shapes are legal only in that role.
DROP TRIGGER IF EXISTS worker_attempts_guard_insert;
DROP TRIGGER IF EXISTS worker_attempts_guard_update;
DROP TRIGGER IF EXISTS worker_attempts_guard_delete;
ALTER TABLE worker_attempts RENAME TO worker_attempts_v35;
CREATE TABLE worker_attempts (
    work_id TEXT NOT NULL,
    attempt_id TEXT PRIMARY KEY,
    lane_id TEXT NOT NULL,
    lane_version INTEGER NOT NULL,
    lane_digest TEXT NOT NULL,
    capability_class TEXT NOT NULL,
    routing_policy_version TEXT NOT NULL,
    routing_policy_digest TEXT NOT NULL
        DEFAULT 'sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736',
    resolved_model TEXT NOT NULL,
    resolution_role TEXT NOT NULL DEFAULT 'preferred',
    fallback_reason TEXT NOT NULL DEFAULT '',
    readback_model TEXT NOT NULL,
    packet_schema_version TEXT NOT NULL,
    report_schema_version TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL CHECK(lifecycle_state IN ('dispatched','completed','failed')),
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL,
    completed_at TEXT,
    failed_at TEXT,
    CHECK(length(work_id) > 0),
    CHECK(length(attempt_id) BETWEEN 2 AND 128),
    CHECK(length(lane_id) BETWEEN 2 AND 32),
    CHECK(lane_version > 0),
    CHECK(length(lane_digest) = 71 AND substr(lane_digest,1,7)='sha256:'),
    CHECK(length(capability_class) BETWEEN 2 AND 64),
    CHECK(length(routing_policy_version) BETWEEN 1 AND 64),
    CHECK(length(routing_policy_digest) = 71 AND substr(routing_policy_digest,1,7)='sha256:'),
    CHECK(resolution_role IN ('preferred','fallback','undeclared')),
    CHECK((resolution_role='preferred' AND fallback_reason='') OR
          (resolution_role='fallback' AND fallback_reason IN ('rate_limit','provider_unavailable','budget_exhausted','other')) OR
          (resolution_role='undeclared' AND fallback_reason='')),
    CHECK((resolution_role != 'undeclared' AND length(resolved_model) BETWEEN 3 AND 128) OR
          (resolution_role='undeclared' AND length(resolved_model) <= 128)),
    CHECK(length(readback_model) <= 128),
    CHECK(packet_schema_version = '1.0'),
    CHECK(report_schema_version = '1.0'),
    CHECK((lifecycle_state='dispatched' AND completed_at IS NULL AND failed_at IS NULL) OR
          (lifecycle_state='completed' AND completed_at IS NOT NULL AND failed_at IS NULL AND length(readback_model) >= 3 AND failure_kind='') OR
          (lifecycle_state='failed' AND failed_at IS NOT NULL AND completed_at IS NULL AND length(failure_kind) > 0 AND
            ((resolution_role != 'undeclared' AND length(readback_model) >= 3) OR resolution_role='undeclared'))),
    CHECK(resolution_role != 'undeclared' OR lifecycle_state='failed')
);
INSERT INTO worker_attempts
    (work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, routing_policy_version, routing_policy_digest, resolved_model, resolution_role, fallback_reason, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at)
    SELECT work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, routing_policy_version, routing_policy_digest, resolved_model, resolution_role, fallback_reason, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at
    FROM worker_attempts_v35;
DROP TABLE worker_attempts_v35;
CREATE INDEX worker_attempts_work ON worker_attempts(work_id, dispatched_at, attempt_id);
CREATE TRIGGER worker_attempts_guard_insert BEFORE INSERT ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_update BEFORE UPDATE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_delete BEFORE DELETE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
`,
	},

	{
		Version: 36,
		Name:    "work_observations",
		SQL: `
-- CD-0030: durable mid-life observations on work items. Fold-only; the
-- work.observation_recorded fold owns every write. Non-authoritative.
CREATE TABLE work_observations (
    observation_id TEXT PRIMARY KEY CHECK(length(observation_id) = 20 AND substr(observation_id,1,4) = 'obs:'),
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    statement TEXT NOT NULL CHECK(length(statement) > 0 AND length(statement) <= 512),
    refs TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(refs) AND json_type(refs)='array' AND json_array_length(refs) <= 16),
    tags TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(tags) AND json_type(tags)='array' AND json_array_length(tags) <= 8),
    recorded_at TEXT NOT NULL
);
CREATE INDEX work_observations_work ON work_observations(work_id, recorded_at);
CREATE TRIGGER work_observations_guard_insert BEFORE INSERT ON work_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER work_observations_guard_update BEFORE UPDATE ON work_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER work_observations_guard_delete BEFORE DELETE ON work_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 37,
		Name:    "workflow_contract_law_revisions",
		SQL: `
-- CD-0036: event-folded law revision pins for bounded reverse consumer lookup.
-- Git remains the sole law author; this table is rebuilt from contract approval
-- event pins and never records a cutover or law relation.
CREATE TABLE workflow_contract_law_revisions (
    work_id         TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    contract_version INTEGER NOT NULL,
    law_id          TEXT NOT NULL,
    content_hash    TEXT NOT NULL CHECK(length(content_hash)=71 AND substr(content_hash,1,7)='sha256:'),
    PRIMARY KEY(work_id, contract_version, law_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT,
    CHECK(length(law_id) BETWEEN 2 AND 256)
);
CREATE INDEX workflow_contract_law_revisions_reverse
    ON workflow_contract_law_revisions(law_id, content_hash, work_id, contract_version);
CREATE TRIGGER workflow_contract_law_revisions_guard_insert BEFORE INSERT ON workflow_contract_law_revisions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_revisions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_revisions_guard_update BEFORE UPDATE ON workflow_contract_law_revisions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_revisions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_revisions_guard_delete BEFORE DELETE ON workflow_contract_law_revisions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_revisions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 38,
		Name:    "domain_law_homes_and_managed_resource_attachments",
		SQL: `
-- CD-0041: Git-authored Domain identity and law ownership projections. These
-- tables survive event-log rebuilds and are refreshed only by the knowledge
-- index projection.
CREATE TABLE domain_registries (
    product_id       TEXT NOT NULL PRIMARY KEY REFERENCES products(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    home_project_id  TEXT NOT NULL,
    home_locator_id  TEXT NOT NULL,
    product_key      TEXT NOT NULL UNIQUE CHECK(length(product_key) BETWEEN 1 AND 256),
    root_domain_id   TEXT NOT NULL,
    schema_version   TEXT NOT NULL CHECK(schema_version='1.0'),
    content_hash     TEXT NOT NULL CHECK(length(content_hash)=71 AND substr(content_hash,1,7)='sha256:'),
    scanned_commit_oid TEXT NOT NULL,
    UNIQUE(home_project_id, home_locator_id),
    UNIQUE(product_id, content_hash),
    FOREIGN KEY(product_id, root_domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE domains (
    home_project_id       TEXT NOT NULL,
    home_locator_id       TEXT NOT NULL,
    product_id             TEXT NOT NULL REFERENCES products(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    domain_id              TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    name                   TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 256),
    purpose                TEXT NOT NULL CHECK(length(purpose) BETWEEN 1 AND 4096),
    parent_domain_id       TEXT,
    status                 TEXT NOT NULL CHECK(status IN ('current','deprecated')),
    registry_content_hash  TEXT NOT NULL CHECK(length(registry_content_hash)=71 AND substr(registry_content_hash,1,7)='sha256:'),
    scanned_commit_oid     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(product_id, domain_id),
    CHECK(parent_domain_id IS NULL OR parent_domain_id <> domain_id),
    FOREIGN KEY(product_id, registry_content_hash) REFERENCES domain_registries(product_id, content_hash) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(product_id, parent_domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX domains_product_status ON domains(product_id, status, name, domain_id);

CREATE TABLE domain_architecture_relations (
    home_project_id        TEXT NOT NULL,
    home_locator_id        TEXT NOT NULL,
    product_id            TEXT NOT NULL,
    source_domain_id      TEXT NOT NULL,
    kind                  TEXT NOT NULL CHECK(kind IN ('depends_on','shares_contract_with','replaces')),
    target_domain_id      TEXT NOT NULL,
    state                 TEXT NOT NULL DEFAULT 'active',
    registry_content_hash TEXT NOT NULL CHECK(length(registry_content_hash)=71 AND substr(registry_content_hash,1,7)='sha256:'),
    scanned_commit_oid   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(home_project_id, home_locator_id, product_id, source_domain_id, kind, target_domain_id),
    CHECK(source_domain_id <> target_domain_id),
    CHECK(kind <> 'shares_contract_with' OR source_domain_id < target_domain_id),
    CHECK((kind='replaces' AND state IN ('declared','building','coexisting','cutover','retired')) OR (kind <> 'replaces' AND state='active')),
    FOREIGN KEY(product_id, registry_content_hash) REFERENCES domain_registries(product_id, content_hash) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(product_id, source_domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(product_id, target_domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX domain_architecture_relations_target ON domain_architecture_relations(product_id, target_domain_id, kind, source_domain_id);
CREATE UNIQUE INDEX law_subjects_identity_hash ON law_subjects(home_project_id, home_locator_id, law_id, content_hash);

CREATE TABLE domain_relation_governing_laws (
    home_project_id  TEXT NOT NULL,
    home_locator_id  TEXT NOT NULL,
    product_id       TEXT NOT NULL,
    source_domain_id TEXT NOT NULL,
    kind             TEXT NOT NULL,
    target_domain_id TEXT NOT NULL,
    law_id           TEXT NOT NULL,
    law_content_hash TEXT NOT NULL CHECK(length(law_content_hash)=71 AND substr(law_content_hash,1,7)='sha256:'),
    PRIMARY KEY(home_project_id, home_locator_id, product_id, source_domain_id, kind, target_domain_id, law_id),
    FOREIGN KEY(home_project_id, home_locator_id, product_id, source_domain_id, kind, target_domain_id) REFERENCES domain_architecture_relations(home_project_id, home_locator_id, product_id, source_domain_id, kind, target_domain_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(home_project_id, home_locator_id, law_id, law_content_hash) REFERENCES law_subjects(home_project_id, home_locator_id, law_id, content_hash) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE law_domain_homes (
    home_project_id  TEXT NOT NULL,
    home_locator_id  TEXT NOT NULL,
    law_id           TEXT NOT NULL,
    product_id       TEXT NOT NULL,
    domain_id        TEXT NOT NULL,
    law_content_hash TEXT NOT NULL CHECK(length(law_content_hash)=71 AND substr(law_content_hash,1,7)='sha256:'),
    scanned_commit_oid TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(home_project_id, home_locator_id, law_id),
    FOREIGN KEY(home_project_id, home_locator_id, law_id, law_content_hash) REFERENCES law_subjects(home_project_id, home_locator_id, law_id, content_hash) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(product_id, domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX law_domain_homes_domain ON law_domain_homes(product_id, domain_id, law_id);

CREATE TABLE law_domain_applicability (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    law_id          TEXT NOT NULL,
    product_id      TEXT NOT NULL,
    domain_id       TEXT NOT NULL,
    scanned_commit_oid TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(home_project_id, home_locator_id, law_id, product_id, domain_id),
    FOREIGN KEY(home_project_id, home_locator_id, law_id) REFERENCES law_subjects(home_project_id, home_locator_id, law_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(home_project_id, home_locator_id, law_id) REFERENCES law_domain_homes(home_project_id, home_locator_id, law_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(product_id, domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE archived_work_domains (
    work_id   TEXT NOT NULL REFERENCES archived_work(id) ON DELETE RESTRICT,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, domain_id)
);
CREATE INDEX archived_work_domains_lookup ON archived_work_domains(domain_id, work_id);
ALTER TABLE archived_work ADD COLUMN manifest_schema_version TEXT NOT NULL DEFAULT '';

CREATE TABLE domain_project_attachment_sets (
    product_id TEXT NOT NULL,
    domain_id  TEXT NOT NULL,
    version    INTEGER NOT NULL CHECK(version >= 0),
    PRIMARY KEY(product_id, domain_id),
    FOREIGN KEY(product_id, domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE domain_project_attachment_edges (
    product_id TEXT NOT NULL,
    domain_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    role       TEXT NOT NULL CHECK(role IN ('primary','supporting')),
    PRIMARY KEY(product_id, domain_id, project_id),
    FOREIGN KEY(product_id, domain_id) REFERENCES domain_project_attachment_sets(product_id, domain_id) ON DELETE RESTRICT,
    FOREIGN KEY(product_id, project_id) REFERENCES product_projects(product_id, project_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);
CREATE UNIQUE INDEX domain_project_attachment_one_primary
    ON domain_project_attachment_edges(product_id, domain_id) WHERE role='primary';

CREATE TABLE domain_resource_attachment_sets (
    product_id TEXT NOT NULL,
    domain_id  TEXT NOT NULL,
    version    INTEGER NOT NULL CHECK(version >= 0),
    PRIMARY KEY(product_id, domain_id),
    FOREIGN KEY(product_id, domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE domain_resource_attachment_edges (
    product_id  TEXT NOT NULL,
    domain_id   TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    purpose     TEXT NOT NULL CHECK(length(purpose) BETWEEN 1 AND 512),
    environments TEXT NOT NULL CHECK(json_valid(environments) AND json_type(environments)='array' AND json_array_length(environments) BETWEEN 0 AND 16),
    PRIMARY KEY(product_id, domain_id, resource_id),
    FOREIGN KEY(product_id, domain_id) REFERENCES domain_resource_attachment_sets(product_id, domain_id) ON DELETE RESTRICT,
    FOREIGN KEY(resource_id, product_id) REFERENCES resource_products(resource_id, product_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE managed_resources (
    resource_id              TEXT PRIMARY KEY CHECK(length(resource_id) BETWEEN 1 AND 256),
    display_name             TEXT NOT NULL CHECK(length(display_name) BETWEEN 1 AND 256),
    class                    TEXT NOT NULL CHECK(class IN ('infrastructure','saas')),
    kind                     TEXT NOT NULL CHECK(kind IN ('service','database','queue','job','schedule','runner_pool','storage','observability','identity','saas_account','saas_project','other')),
    purpose                  TEXT NOT NULL CHECK(length(purpose) BETWEEN 1 AND 4096),
    stage_maturity           TEXT NOT NULL CHECK(stage_maturity IN ('prototype','alpha','beta','production','deprecated')),
    stage_audience_commitment TEXT NOT NULL CHECK(stage_audience_commitment IN ('operator_only','limited','public')),
    environments             TEXT NOT NULL CHECK(json_valid(environments) AND json_type(environments)='array' AND json_array_length(environments) BETWEEN 0 AND 16),
    locator_absence_reason   TEXT CHECK(locator_absence_reason IS NULL OR locator_absence_reason IN ('planned','not_addressable')),
    metadata_schema_version  TEXT NOT NULL CHECK(length(metadata_schema_version) BETWEEN 1 AND 64),
    metadata                 TEXT NOT NULL CHECK(length(CAST(metadata AS BLOB)) BETWEEN 2 AND 16384 AND json_valid(metadata) AND json_type(metadata)='object'),
    version                  INTEGER NOT NULL CHECK(version > 0),
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    CHECK(kind <> 'other' OR (json_type(metadata, '$.kind_detail')='text' AND length(trim(json_extract(metadata, '$.kind_detail'))) BETWEEN 1 AND 256 AND json_extract(metadata, '$.kind_detail')=trim(json_extract(metadata, '$.kind_detail'))))
);

CREATE TABLE resource_products (
    resource_id  TEXT NOT NULL REFERENCES managed_resources(resource_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    product_id   TEXT NOT NULL REFERENCES products(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    role         TEXT NOT NULL CHECK(role IN ('owner','consumer')),
    purpose      TEXT NOT NULL CHECK(length(purpose) BETWEEN 1 AND 512),
    environments TEXT NOT NULL CHECK(json_valid(environments) AND json_type(environments)='array' AND json_array_length(environments) BETWEEN 0 AND 16),
    PRIMARY KEY(resource_id, product_id),
    CHECK(role='owner' OR role='consumer')
);
CREATE UNIQUE INDEX resource_products_one_owner ON resource_products(resource_id) WHERE role='owner';
CREATE INDEX resource_products_product ON resource_products(product_id, role, resource_id);

CREATE TRIGGER domain_registries_guard_insert BEFORE INSERT ON domain_registries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_registries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_registries_guard_update BEFORE UPDATE ON domain_registries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_registries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_registries_guard_delete BEFORE DELETE ON domain_registries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_registries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domains_guard_insert BEFORE INSERT ON domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domains_guard_update BEFORE UPDATE ON domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domains_guard_delete BEFORE DELETE ON domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_architecture_relations_guard_insert BEFORE INSERT ON domain_architecture_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_architecture_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_architecture_relations_guard_update BEFORE UPDATE ON domain_architecture_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_architecture_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_architecture_relations_guard_delete BEFORE DELETE ON domain_architecture_relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_architecture_relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_relation_governing_laws_guard_insert BEFORE INSERT ON domain_relation_governing_laws FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_relation_governing_laws is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_relation_governing_laws_guard_update BEFORE UPDATE ON domain_relation_governing_laws FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_relation_governing_laws is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_relation_governing_laws_guard_delete BEFORE DELETE ON domain_relation_governing_laws FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_relation_governing_laws is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_homes_guard_insert BEFORE INSERT ON law_domain_homes FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_homes is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_homes_guard_update BEFORE UPDATE ON law_domain_homes FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_homes is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_homes_guard_delete BEFORE DELETE ON law_domain_homes FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_homes is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_applicability_guard_insert BEFORE INSERT ON law_domain_applicability FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_applicability is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_applicability_guard_update BEFORE UPDATE ON law_domain_applicability FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_applicability is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER law_domain_applicability_guard_delete BEFORE DELETE ON law_domain_applicability FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'law_domain_applicability is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER archived_work_domains_guard_insert BEFORE INSERT ON archived_work_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'archived_work_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER archived_work_domains_guard_update BEFORE UPDATE ON archived_work_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'archived_work_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER archived_work_domains_guard_delete BEFORE DELETE ON archived_work_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'archived_work_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_sets_guard_insert BEFORE INSERT ON domain_project_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_sets_guard_update BEFORE UPDATE ON domain_project_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_sets_guard_delete BEFORE DELETE ON domain_project_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_edges_guard_insert BEFORE INSERT ON domain_project_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_edges_guard_update BEFORE UPDATE ON domain_project_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_project_attachment_edges_guard_delete BEFORE DELETE ON domain_project_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_project_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_sets_guard_insert BEFORE INSERT ON domain_resource_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_sets_guard_update BEFORE UPDATE ON domain_resource_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_sets_guard_delete BEFORE DELETE ON domain_resource_attachment_sets FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_sets is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_edges_guard_insert BEFORE INSERT ON domain_resource_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_edges_guard_update BEFORE UPDATE ON domain_resource_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_resource_attachment_edges_guard_delete BEFORE DELETE ON domain_resource_attachment_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_resource_attachment_edges is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER managed_resources_guard_insert BEFORE INSERT ON managed_resources FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'managed_resources is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER managed_resources_guard_update BEFORE UPDATE ON managed_resources FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'managed_resources is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER managed_resources_guard_delete BEFORE DELETE ON managed_resources FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'managed_resources is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER resource_products_guard_insert BEFORE INSERT ON resource_products FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_products is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER resource_products_guard_update BEFORE UPDATE ON resource_products FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_products is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER resource_products_guard_delete BEFORE DELETE ON resource_products FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'resource_products is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 39,
		Name:    "workflow_architecture_bindings",
		SQL: `
-- CD-0041 D5: immutable contract bindings retain historical Domain and law
-- identities without foreign keys to today's Git-derived architecture rows.
CREATE TABLE workflow_architecture_bindings (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    domain_registry_content_hash TEXT NOT NULL CHECK(length(domain_registry_content_hash)=71 AND substr(domain_registry_content_hash,1,7)='sha256:'),
    home_domain_id TEXT NOT NULL CHECK(length(home_domain_id) BETWEEN 1 AND 256),
    projection_hash TEXT NOT NULL CHECK(length(projection_hash)=71 AND substr(projection_hash,1,7)='sha256:'),
    PRIMARY KEY(work_id, contract_version),
    UNIQUE(work_id, contract_version, product_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT
);

CREATE TABLE workflow_contract_affected_domains (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, domain_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
CREATE INDEX workflow_contract_affected_domains_lookup ON workflow_contract_affected_domains(domain_id, work_id, contract_version);

CREATE TABLE workflow_contract_domain_modifications (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, domain_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);

CREATE TABLE workflow_contract_domain_relation_modifications (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    source_domain_id TEXT NOT NULL CHECK(length(source_domain_id) BETWEEN 1 AND 256),
    kind TEXT NOT NULL CHECK(kind IN ('depends_on','shares_contract_with','replaces')),
    target_domain_id TEXT NOT NULL CHECK(length(target_domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, source_domain_id, kind, target_domain_id),
    CHECK(source_domain_id <> target_domain_id),
    CHECK(kind <> 'shares_contract_with' OR source_domain_id < target_domain_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);

CREATE TABLE workflow_law_addition_reservations (
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    owner_work_id TEXT NOT NULL,
    owner_contract_version INTEGER NOT NULL,
    home_domain_id TEXT NOT NULL CHECK(length(home_domain_id) BETWEEN 1 AND 256),
    FOREIGN KEY(owner_work_id, owner_contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT,
    FOREIGN KEY(owner_work_id, owner_contract_version, product_id) REFERENCES workflow_architecture_bindings(work_id, contract_version, product_id) ON DELETE RESTRICT,
    PRIMARY KEY(product_id, law_id),
    UNIQUE(product_id, law_id, owner_work_id, owner_contract_version, home_domain_id)
);
CREATE INDEX workflow_law_addition_reservations_owner ON workflow_law_addition_reservations(product_id, owner_work_id, owner_contract_version);

CREATE TABLE workflow_contract_law_additions (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    product_id TEXT NOT NULL,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    home_domain_id TEXT NOT NULL CHECK(length(home_domain_id) BETWEEN 1 AND 256),
    reservation_owner_work_id TEXT NOT NULL,
    reservation_owner_contract_version INTEGER NOT NULL,
    CHECK(reservation_owner_work_id = work_id),
    PRIMARY KEY(work_id, contract_version, law_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT,
    FOREIGN KEY(work_id, contract_version, product_id) REFERENCES workflow_architecture_bindings(work_id, contract_version, product_id) ON DELETE RESTRICT,
    FOREIGN KEY(product_id, law_id, reservation_owner_work_id, reservation_owner_contract_version, home_domain_id) REFERENCES workflow_law_addition_reservations(product_id, law_id, owner_work_id, owner_contract_version, home_domain_id) ON DELETE RESTRICT
);

CREATE TABLE workflow_contract_verification_obligations (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    obligation_id TEXT NOT NULL CHECK(length(obligation_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, law_id, obligation_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);

CREATE TRIGGER workflow_architecture_bindings_guard_insert BEFORE INSERT ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_architecture_bindings_guard_update BEFORE UPDATE ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is immutable'); END;
CREATE TRIGGER workflow_architecture_bindings_guard_delete BEFORE DELETE ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_insert BEFORE INSERT ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_update BEFORE UPDATE ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_delete BEFORE DELETE ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_insert BEFORE INSERT ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_update BEFORE UPDATE ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_delete BEFORE DELETE ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_insert BEFORE INSERT ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_update BEFORE UPDATE ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_delete BEFORE DELETE ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_insert BEFORE INSERT ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_update BEFORE UPDATE ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is immutable'); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_delete BEFORE DELETE ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_insert BEFORE INSERT ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_update BEFORE UPDATE ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_delete BEFORE DELETE ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_insert BEFORE INSERT ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_update BEFORE UPDATE ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_delete BEFORE DELETE ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 40,
		Name:    "workflow_domain_overlap_resolutions",
		SQL: `
-- CD-0041 D6-D7: normalized law writes and version-pinned overlap
-- resolutions. Detected overlap remains derived; this table stores only an
-- operator-approved resolution event projection.
CREATE TABLE workflow_contract_law_modifications (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    PRIMARY KEY(work_id, contract_version, law_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
CREATE INDEX workflow_contract_law_modifications_lookup
    ON workflow_contract_law_modifications(law_id, work_id, contract_version);
CREATE TRIGGER workflow_contract_law_modifications_guard_insert BEFORE INSERT ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_modifications_guard_update BEFORE UPDATE ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_modifications_guard_delete BEFORE DELETE ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;

INSERT INTO fold_guard(active) VALUES (1);
INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id)
SELECT c.work_id,c.contract_version,j.value
FROM workflow_contracts c, json_each(c.law_modifies) j
WHERE json_valid(c.law_modifies) AND json_type(c.law_modifies)='array';
DELETE FROM fold_guard WHERE active = 1;

CREATE TABLE workflow_overlap_resolutions (
    resolution_id TEXT PRIMARY KEY,
    event_seq INTEGER NOT NULL UNIQUE REFERENCES domain_events(seq) ON DELETE RESTRICT,
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    from_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    to_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    from_contract_version INTEGER NOT NULL,
    to_contract_version INTEGER NOT NULL,
    resolution_kind TEXT NOT NULL CHECK(resolution_kind IN ('compatible_with','depends_on','blocks','merged_into','supersedes')),
    reason TEXT NOT NULL CHECK(length(reason) BETWEEN 1 AND 4096),
    approval_ref TEXT NOT NULL CHECK(length(approval_ref) > 0),
    created_at TEXT NOT NULL,
    invalidated_seq INTEGER REFERENCES domain_events(seq) ON DELETE RESTRICT,
    CHECK(from_work_id <> to_work_id),
    CHECK(from_contract_version > 0 AND to_contract_version > 0),
    FOREIGN KEY(from_work_id,from_contract_version) REFERENCES workflow_contracts(work_id,contract_version) ON DELETE RESTRICT,
    FOREIGN KEY(to_work_id,to_contract_version) REFERENCES workflow_contracts(work_id,contract_version) ON DELETE RESTRICT
);
CREATE INDEX workflow_overlap_resolutions_pair ON workflow_overlap_resolutions(product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,event_seq);
CREATE INDEX workflow_overlap_resolutions_reverse_pair ON workflow_overlap_resolutions(product_id,to_work_id,from_work_id,to_contract_version,from_contract_version,event_seq);
CREATE TRIGGER workflow_overlap_resolutions_guard_insert BEFORE INSERT ON workflow_overlap_resolutions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_overlap_resolutions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_overlap_resolutions_guard_update BEFORE UPDATE ON workflow_overlap_resolutions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_overlap_resolutions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_overlap_resolutions_guard_delete BEFORE DELETE ON workflow_overlap_resolutions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_overlap_resolutions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;

-- The public relation vocabulary includes resolution companions. These rows
-- are read-side context only; overlap authority remains the projection above.
DROP TRIGGER relations_guard_insert;
DROP TRIGGER relations_guard_update;
DROP TRIGGER relations_guard_delete;
DROP INDEX idx_relations_from_kind;
DROP INDEX idx_relations_to_kind;
DROP INDEX relations_supersedes_target;
ALTER TABLE relations RENAME TO relations_v40;
CREATE TABLE relations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    work_id_from TEXT NOT NULL REFERENCES work_items(id),
    work_id_to   TEXT NOT NULL REFERENCES work_items(id),
    kind         TEXT NOT NULL CHECK(kind IN ('parent','includes','blocks','implements','supersedes','forward_link','raised_from','depends_on','compatible_with','merged_into')),
    created_at   TEXT NOT NULL,
    resolution_id TEXT,
    CHECK(work_id_from <> work_id_to),
    UNIQUE(work_id_from, work_id_to, kind)
);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id)
    SELECT id,work_id_from,work_id_to,kind,created_at,NULL FROM relations_v40;
DROP TABLE relations_v40;
CREATE INDEX idx_relations_from_kind ON relations(work_id_from, kind, work_id_to);
CREATE INDEX idx_relations_to_kind ON relations(work_id_to, kind, work_id_from);
CREATE UNIQUE INDEX relations_supersedes_target ON relations(work_id_to) WHERE kind = 'supersedes';
CREATE UNIQUE INDEX relations_merged_into_source ON relations(work_id_from) WHERE kind = 'merged_into';
CREATE TRIGGER relations_guard_insert BEFORE INSERT ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_update BEFORE UPDATE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER relations_guard_delete BEFORE DELETE ON relations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'relations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
		`,
	},
	{
		Version: 41,
		Name:    "workflow_native_runs",
		SQL: `
-- CD-0039 D4: fold-only projection of attributed native-run reports. One row
-- holds the latest report per (work, run, phase); every read must carry the
-- reporting authority and evidence alongside the status.
CREATE TABLE workflow_native_runs (
    work_id                TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    run_id                 TEXT NOT NULL CHECK(length(run_id) BETWEEN 1 AND 128),
    phase                  TEXT NOT NULL CHECK(phase IN ('start','health','rollback','cleanup')),
    status                 TEXT NOT NULL,
    event_id               TEXT NOT NULL,
    reporting_authority_ref TEXT NOT NULL CHECK(length(reporting_authority_ref) BETWEEN 1 AND 128),
    actor_ref              TEXT NOT NULL CHECK(length(actor_ref) BETWEEN 1 AND 256),
    native_subject_ref     TEXT NOT NULL CHECK(length(native_subject_ref) BETWEEN 1 AND 2048),
    subject_digest         TEXT NOT NULL CHECK(length(subject_digest)=71 AND substr(subject_digest,1,7)='sha256:'),
    evidence_ref           TEXT NOT NULL CHECK(length(evidence_ref) BETWEEN 1 AND 2048),
    evidence_digest        TEXT NOT NULL CHECK(length(evidence_digest) BETWEEN 1 AND 256),
    asserted_at            TEXT NOT NULL,
    recorded_at            TEXT NOT NULL,
    capture_method         TEXT NOT NULL CHECK(capture_method='trusted_client_report'),
    observed_universe      TEXT NOT NULL CHECK(json_valid(observed_universe) AND json_type(observed_universe)='object'),
    freshness_policy_ref   TEXT NOT NULL CHECK(length(freshness_policy_ref) BETWEEN 1 AND 256),
    divergence_policy_ref  TEXT NOT NULL CHECK(length(divergence_policy_ref) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, run_id, phase)
);
CREATE INDEX workflow_native_runs_work ON workflow_native_runs(work_id, run_id);
CREATE TRIGGER workflow_native_runs_guard_insert BEFORE INSERT ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_update BEFORE UPDATE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_delete BEFORE DELETE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
		`,
	},
	{
		Version: 42,
		Name:    "external_observations",
		SQL: `
-- CD-0040 D3/D6: the shared external-observation projection. Capture rows are
-- append-only; verification columns hold the derived fold state, never an
-- edited claim.
CREATE TABLE external_observations (
    observation_id TEXT PRIMARY KEY CHECK(observation_id LIKE 'xobs:%'),
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    subject_kind TEXT NOT NULL CHECK(length(subject_kind) BETWEEN 1 AND 64),
    subject_ref TEXT NOT NULL CHECK(length(subject_ref) BETWEEN 1 AND 2048),
    capture_method TEXT NOT NULL CHECK(capture_method IN ('trusted_client_report','git_probe')),
    captured_at TEXT NOT NULL,
    reporting_authority_ref TEXT NOT NULL CHECK(length(reporting_authority_ref) BETWEEN 1 AND 256),
    subject_digest TEXT CHECK(subject_digest IS NULL OR subject_digest LIKE 'sha256:%'),
    observed_universe TEXT NOT NULL,
    freshness_policy_ref TEXT NOT NULL,
    divergence_policy_ref TEXT NOT NULL,
    verification_state TEXT NOT NULL DEFAULT 'unverified' CHECK(verification_state IN ('unverified','verified','diverged_expected','diverged_unexpected','unverifiable')),
    verification_method TEXT,
    verified_at TEXT,
    verifying_authority_ref TEXT,
    verification_result TEXT,
    verification_omissions TEXT,
    created_event_seq INTEGER NOT NULL REFERENCES domain_events(seq) ON DELETE RESTRICT,
    verified_event_seq INTEGER REFERENCES domain_events(seq) ON DELETE RESTRICT
);
CREATE INDEX external_observations_work ON external_observations(work_id, created_event_seq);
CREATE TRIGGER external_observations_guard_insert BEFORE INSERT ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER external_observations_guard_update BEFORE UPDATE ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER external_observations_guard_delete BEFORE DELETE ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;

-- CD-0040 D11 verification participation: native-run records embed the shared
-- component, so each row names its observation and carries the derived
-- verification state a verification event folds into it.
ALTER TABLE workflow_native_runs ADD COLUMN observation_id TEXT;
ALTER TABLE workflow_native_runs ADD COLUMN verification_state TEXT NOT NULL DEFAULT 'unverified';
        `,
	},
	{
		Version: 43,
		Name:    "routing_policy_manifest_digest_default",
		SQL: `
-- Keep the applied v35 definition immutable while making the current default
-- resolve through the generated routing-policy digest at insert time.
DROP TRIGGER IF EXISTS worker_attempts_guard_insert;
DROP TRIGGER IF EXISTS worker_attempts_guard_update;
DROP TRIGGER IF EXISTS worker_attempts_guard_delete;
ALTER TABLE worker_attempts RENAME TO worker_attempts_v43;
CREATE TABLE worker_attempts (
    work_id TEXT NOT NULL,
    attempt_id TEXT PRIMARY KEY,
    lane_id TEXT NOT NULL,
    lane_version INTEGER NOT NULL,
    lane_digest TEXT NOT NULL,
    capability_class TEXT NOT NULL,
    routing_policy_version TEXT NOT NULL,
    routing_policy_digest TEXT NOT NULL
        DEFAULT (concord_routing_policy_manifest_digest()),
    resolved_model TEXT NOT NULL,
    resolution_role TEXT NOT NULL DEFAULT 'preferred',
    fallback_reason TEXT NOT NULL DEFAULT '',
    readback_model TEXT NOT NULL,
    packet_schema_version TEXT NOT NULL,
    report_schema_version TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL CHECK(lifecycle_state IN ('dispatched','completed','failed')),
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL,
    completed_at TEXT,
    failed_at TEXT,
    CHECK(length(work_id) > 0),
    CHECK(length(attempt_id) BETWEEN 2 AND 128),
    CHECK(length(lane_id) BETWEEN 2 AND 32),
    CHECK(lane_version > 0),
    CHECK(length(lane_digest) = 71 AND substr(lane_digest,1,7)='sha256:'),
    CHECK(length(capability_class) BETWEEN 2 AND 64),
    CHECK(length(routing_policy_version) BETWEEN 1 AND 64),
    CHECK(length(routing_policy_digest) = 71 AND substr(routing_policy_digest,1,7)='sha256:'),
    CHECK(resolution_role IN ('preferred','fallback','undeclared')),
    CHECK((resolution_role='preferred' AND fallback_reason='') OR
          (resolution_role='fallback' AND fallback_reason IN ('rate_limit','provider_unavailable','budget_exhausted','other')) OR
          (resolution_role='undeclared' AND fallback_reason='')),
    CHECK((resolution_role != 'undeclared' AND length(resolved_model) BETWEEN 3 AND 128) OR
          (resolution_role='undeclared' AND length(resolved_model) <= 128)),
    CHECK(length(readback_model) <= 128),
    CHECK(packet_schema_version = '1.0'),
    CHECK(report_schema_version = '1.0'),
    CHECK((lifecycle_state='dispatched' AND completed_at IS NULL AND failed_at IS NULL) OR
          (lifecycle_state='completed' AND completed_at IS NOT NULL AND failed_at IS NULL AND length(readback_model) >= 3 AND failure_kind='') OR
          (lifecycle_state='failed' AND failed_at IS NOT NULL AND completed_at IS NULL AND length(failure_kind) > 0 AND
            ((resolution_role != 'undeclared' AND length(readback_model) >= 3) OR resolution_role='undeclared'))),
    CHECK(resolution_role != 'undeclared' OR lifecycle_state='failed')
);
INSERT INTO worker_attempts
    (work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, routing_policy_version, routing_policy_digest, resolved_model, resolution_role, fallback_reason, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at)
    SELECT work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, routing_policy_version, routing_policy_digest, resolved_model, resolution_role, fallback_reason, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at
    FROM worker_attempts_v43;
DROP TABLE worker_attempts_v43;
CREATE INDEX worker_attempts_work ON worker_attempts(work_id, dispatched_at, attempt_id);
CREATE TRIGGER worker_attempts_guard_insert BEFORE INSERT ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_update BEFORE UPDATE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_delete BEFORE DELETE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 44,
		Name:    "drop_worker_routing_evidence",
		SQL: `
-- CD-0058: Concord performs no model resolution. The declared-side attempt
-- columns (routing_policy_version, routing_policy_digest, resolved_model,
-- resolution_role, fallback_reason) describe a decision the system no longer
-- makes, so the table is rebuilt without them. Every existing row is
-- preserved; the lifecycle CHECK that references readback_model is kept.
DROP TRIGGER IF EXISTS worker_attempts_guard_insert;
DROP TRIGGER IF EXISTS worker_attempts_guard_update;
DROP TRIGGER IF EXISTS worker_attempts_guard_delete;
ALTER TABLE worker_attempts RENAME TO worker_attempts_v44;
CREATE TABLE worker_attempts (
    work_id TEXT NOT NULL,
    attempt_id TEXT PRIMARY KEY,
    lane_id TEXT NOT NULL,
    lane_version INTEGER NOT NULL,
    lane_digest TEXT NOT NULL,
    capability_class TEXT NOT NULL,
    readback_model TEXT NOT NULL,
    packet_schema_version TEXT NOT NULL,
    report_schema_version TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL CHECK(lifecycle_state IN ('dispatched','completed','failed')),
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL,
    completed_at TEXT,
    failed_at TEXT,
    CHECK(length(work_id) > 0),
    CHECK(length(attempt_id) BETWEEN 2 AND 128),
    CHECK(length(lane_id) BETWEEN 2 AND 32),
    CHECK(lane_version > 0),
    CHECK(length(lane_digest) = 71 AND substr(lane_digest,1,7)='sha256:'),
    CHECK(length(capability_class) BETWEEN 2 AND 64),
    CHECK(length(readback_model) <= 128),
    CHECK(packet_schema_version = '1.0'),
    CHECK(report_schema_version = '1.0'),
    CHECK((lifecycle_state='dispatched' AND completed_at IS NULL AND failed_at IS NULL) OR
          (lifecycle_state='completed' AND completed_at IS NOT NULL AND failed_at IS NULL AND length(readback_model) >= 3 AND failure_kind='') OR
          (lifecycle_state='failed' AND failed_at IS NOT NULL AND completed_at IS NULL AND length(failure_kind) > 0 AND length(readback_model) >= 3))
);
INSERT INTO worker_attempts
    (work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at)
    SELECT work_id, attempt_id, lane_id, lane_version, lane_digest, capability_class, readback_model, packet_schema_version, report_schema_version, lifecycle_state, failure_kind, failure_detail, dispatched_at, completed_at, failed_at
    FROM worker_attempts_v44;
DROP TABLE worker_attempts_v44;
CREATE INDEX worker_attempts_work ON worker_attempts(work_id, dispatched_at, attempt_id);
CREATE TRIGGER worker_attempts_guard_insert BEFORE INSERT ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_update BEFORE UPDATE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER worker_attempts_guard_delete BEFORE DELETE ON worker_attempts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'worker_attempts is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 45,
		Name:    "law_domain_home_product_wide_rationale",
		SQL: `
-- A law record homed to the Product root states why no child Domain owns it.
-- The projection has to carry that claim or PM1.Q10 cannot hold: Q10 rebuilds
-- the declared record from this table and requires byte equality with the
-- manifest declaration, so a field the projection drops makes every
-- root-homed record fail its own proof. Child-homed rows keep the empty
-- default, which is what their absent claim means.
ALTER TABLE law_domain_homes ADD COLUMN product_wide_rationale TEXT NOT NULL DEFAULT '';
		`,
	},
	{
		Version: 46,
		Name:    "drop_orchestrator_reservation",
		SQL: `
-- CD-0061 D3: the typed_agent_type, typed_agent_version, and
-- typed_agent_ruleset_digest columns on workflow_context_boundaries were
-- reserved for a restart-dispatch path CD-0027 excludes; no code reads or
-- writes them, and the only member of boundary_kind that gated on them
-- ('restart') is closed by the same decision. The reservation is removed so
-- the misreading CD-0049 made cannot recur, and boundary_kind narrows to
-- admit only 'summary' at the column level. The table-level CHECK that
-- summary-bound rows carry a non-empty summary is preserved.
CREATE TABLE workflow_context_boundaries_new (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL,
    boundary_sequence INTEGER NOT NULL,
    boundary_count INTEGER NOT NULL,
    boundary_id TEXT NOT NULL,
    boundary_kind TEXT NOT NULL CHECK(boundary_kind='summary'),
    checkpoint_id TEXT NOT NULL,
    checkpoint_sequence INTEGER NOT NULL,
    attempt_epoch INTEGER NOT NULL,
    summary TEXT NOT NULL,
    workflow_ref TEXT NOT NULL,
    workflow_definition_version INTEGER NOT NULL,
    workflow_definition_digest TEXT NOT NULL,
    actor_ref TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    request_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(work_id, boundary_sequence),
    UNIQUE(work_id, boundary_id),
    CHECK(work_version > 0),
    CHECK(boundary_sequence > 0 AND boundary_count = boundary_sequence),
    CHECK(length(boundary_id) BETWEEN 2 AND 128),
    CHECK(boundary_kind='summary' AND length(summary) BETWEEN 1 AND 16384),
    CHECK(attempt_epoch > 0),
    CHECK(length(checkpoint_id) BETWEEN 2 AND 128),
    CHECK(checkpoint_sequence > 0),
    CHECK(length(workflow_ref) BETWEEN 2 AND 128),
    CHECK(workflow_definition_version > 0),
    CHECK(length(workflow_definition_digest) = 71 AND substr(workflow_definition_digest,1,7)='sha256:'),
    CHECK(length(request_id) > 0)
);
INSERT INTO workflow_context_boundaries_new
    (work_id, work_version, boundary_sequence, boundary_count, boundary_id, boundary_kind,
     checkpoint_id, checkpoint_sequence, attempt_epoch, summary, workflow_ref,
     workflow_definition_version, workflow_definition_digest, actor_ref, request_id, recorded_at)
    SELECT work_id, work_version, boundary_sequence, boundary_count, boundary_id, boundary_kind,
           checkpoint_id, checkpoint_sequence, attempt_epoch, summary, workflow_ref,
           workflow_definition_version, workflow_definition_digest, actor_ref, request_id, recorded_at
    FROM workflow_context_boundaries;
DROP TABLE workflow_context_boundaries;
ALTER TABLE workflow_context_boundaries_new RENAME TO workflow_context_boundaries;
CREATE INDEX workflow_context_boundaries_history ON workflow_context_boundaries(work_id, boundary_sequence DESC);
CREATE TRIGGER workflow_context_boundaries_guard_insert BEFORE INSERT ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_context_boundaries_guard_update BEFORE UPDATE ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is immutable'); END;
CREATE TRIGGER workflow_context_boundaries_guard_delete BEFORE DELETE ON workflow_context_boundaries FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_context_boundaries is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},

	{
		Version: 47,
		Name:    "domain_observations",
		SQL: `
-- CD-0068 D1: a Domain is a second observation anchor. Domain identity is a
-- Git projection, so the row is keyed by (product_id, domain_id) against
-- domains with ON DELETE NO ACTION, matching the attachment-set pattern.
-- Fold-only; the domain.observation_recorded / domain.observation_dismissed
-- folds own every write. Non-authoritative (CD-0068 D5).
CREATE TABLE domain_observations (
    observation_id TEXT PRIMARY KEY CHECK(length(observation_id) = 20 AND substr(observation_id,1,4) = 'dob:'),
    product_id TEXT NOT NULL,
    domain_id  TEXT NOT NULL,
    statement TEXT NOT NULL CHECK(length(statement) > 0 AND length(statement) <= 512),
    refs TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(refs) AND json_type(refs)='array' AND json_array_length(refs) <= 16),
    tags TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(tags) AND json_type(tags)='array' AND json_array_length(tags) <= 8),
    state TEXT NOT NULL CHECK(state IN ('open','dismissed')),
    recorded_at TEXT NOT NULL,
    dismissed_at TEXT,
    -- CD-0068 D3: dismissal flips state and never deletes. The timestamp is
    -- present exactly when the row is dismissed, so the two-state shape cannot
    -- drift from its audit trail.
    CHECK((state = 'dismissed') = (dismissed_at IS NOT NULL)),
    FOREIGN KEY(product_id, domain_id) REFERENCES domains(product_id, domain_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX domain_observations_open ON domain_observations(product_id, domain_id, state, recorded_at);
CREATE TRIGGER domain_observations_guard_insert BEFORE INSERT ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_observations_guard_update BEFORE UPDATE ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER domain_observations_guard_delete BEFORE DELETE ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
		`,
	},
	{
		Version: 48,
		Name:    "research_finding_scopes_use_domain",
		SQL: `
-- CD-0041 retires component as an authority identity; the active research
-- scope surface therefore renames scope_kind 'component' to 'domain'.
-- Migration 29 created active_research_finding_scopes with CHECK
-- (scope_kind IN ('product','project','component','tag')) and a home-guard
-- trigger. SQLite cannot ALTER a CHECK, so the table is rebuilt with the new
-- enum and every legacy 'component' row is rewritten to 'domain' in the
-- same transaction. The rewrite happens in the SELECT that copies rows
-- into the new table: the SELECT cannot write to _v48 because its CHECK
-- does not admit 'domain', and the new CHECK does not admit 'component',
-- so a literal CASE bridges the gap in one statement. The closed-kind
-- index and home-guard trigger are recreated against the new table; their
-- definitions are byte-identical to migration 29.
DROP TRIGGER IF EXISTS active_research_finding_scopes_home_guard;
DROP TRIGGER IF EXISTS active_research_findings_home_guard_update;
ALTER TABLE active_research_finding_scopes RENAME TO active_research_finding_scopes_v48;
CREATE TABLE active_research_finding_scopes (
    pack_id     TEXT NOT NULL,
    revision    INTEGER NOT NULL,
    finding_id  TEXT NOT NULL,
    scope_kind  TEXT NOT NULL CHECK(scope_kind IN ('product','project','domain','tag')),
    scope_id    TEXT NOT NULL,
    PRIMARY KEY(pack_id, revision, finding_id, scope_kind, scope_id),
    FOREIGN KEY(pack_id, revision, finding_id) REFERENCES active_research_findings(pack_id, revision, finding_id) ON DELETE CASCADE,
    CHECK(length(scope_id) > 0)
);
INSERT INTO active_research_finding_scopes
    (pack_id, revision, finding_id, scope_kind, scope_id)
    SELECT pack_id, revision, finding_id,
           CASE WHEN scope_kind = 'component' THEN 'domain' ELSE scope_kind END,
           scope_id
    FROM active_research_finding_scopes_v48;
DROP TABLE active_research_finding_scopes_v48;
CREATE INDEX active_research_finding_scopes_lookup
    ON active_research_finding_scopes(scope_kind, scope_id, pack_id, revision);

CREATE TRIGGER active_research_finding_scopes_home_guard
BEFORE INSERT ON active_research_finding_scopes FOR EACH ROW
WHEN (SELECT scope_mode FROM active_research_findings WHERE pack_id=NEW.pack_id AND revision=NEW.revision AND finding_id=NEW.finding_id) = 'home'
BEGIN
    SELECT RAISE(ABORT, 'home scope cannot carry explicit scope IDs');
END;

CREATE TRIGGER active_research_findings_home_guard_update
BEFORE UPDATE OF scope_mode ON active_research_findings FOR EACH ROW
WHEN NEW.scope_mode = 'home' AND EXISTS(SELECT 1 FROM active_research_finding_scopes s WHERE s.pack_id=NEW.pack_id AND s.revision=NEW.revision AND s.finding_id=NEW.finding_id)
BEGIN
    SELECT RAISE(ABORT, 'home scope cannot carry explicit scope IDs');
END;
		`,
	},
	{
		Version: 49,
		Name:    "work_kind_and_native_run_vocabularies",
		SQL: `
CREATE TABLE work_kinds (
    kind         TEXT PRIMARY KEY,
    stored       INTEGER NOT NULL CHECK(stored IN (0,1)),
    fold_create  INTEGER NOT NULL CHECK(fold_create IN (0,1)),
    fold_revise  INTEGER NOT NULL CHECK(fold_revise IN (0,1)),
    agent_capture INTEGER NOT NULL CHECK(agent_capture IN (0,1))
);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('bug',1,1,1,1);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('decision',1,1,1,1);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('epic',0,0,0,0);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('initiative',1,1,0,0);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('other',1,1,1,1);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('research',1,1,1,1);
INSERT INTO work_kinds(kind,stored,fold_create,fold_revise,agent_capture) VALUES ('task',1,1,1,1);

CREATE TABLE workflow_native_run_statuses (
    phase   TEXT NOT NULL,
    status  TEXT NOT NULL,
    failure INTEGER NOT NULL CHECK(failure IN (0,1)),
    PRIMARY KEY(phase,status)
);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('start','started',0);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('start','failed_to_start',1);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('health','healthy',0);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('health','degraded',0);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('health','failed',1);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('rollback','rolled_back',1);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('rollback','partially_rolled_back',1);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('rollback','rollback_failed',1);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('cleanup','cleaned',0);
INSERT INTO workflow_native_run_statuses(phase,status,failure) VALUES ('cleanup','cleanup_failed',1);

CREATE TRIGGER work_kinds_registry_no_insert
BEFORE INSERT ON work_kinds FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_kinds registry is immutable');
END;
CREATE TRIGGER work_kinds_registry_no_update
BEFORE UPDATE ON work_kinds FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_kinds registry is immutable');
END;
CREATE TRIGGER work_kinds_registry_no_delete
BEFORE DELETE ON work_kinds FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_kinds registry is immutable');
END;

CREATE TRIGGER workflow_native_run_statuses_registry_no_insert
BEFORE INSERT ON workflow_native_run_statuses FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'workflow_native_run_statuses registry is immutable');
END;
CREATE TRIGGER workflow_native_run_statuses_registry_no_update
BEFORE UPDATE ON workflow_native_run_statuses FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'workflow_native_run_statuses registry is immutable');
END;
CREATE TRIGGER workflow_native_run_statuses_registry_no_delete
BEFORE DELETE ON workflow_native_run_statuses FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'workflow_native_run_statuses registry is immutable');
END;

CREATE TRIGGER work_items_kind_registry_insert
BEFORE INSERT ON work_items FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM work_kinds WHERE kind=NEW.kind AND stored=1)
BEGIN
    SELECT RAISE(ABORT, 'work_items kind is not a stored work kind');
END;
CREATE TRIGGER work_items_kind_registry_update
BEFORE UPDATE OF kind ON work_items FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM work_kinds WHERE kind=NEW.kind AND stored=1)
BEGIN
    SELECT RAISE(ABORT, 'work_items kind is not a stored work kind');
END;

CREATE TRIGGER workflow_native_runs_status_registry_insert
BEFORE INSERT ON workflow_native_runs FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM workflow_native_run_statuses WHERE phase=NEW.phase AND status=NEW.status)
BEGIN
    SELECT RAISE(ABORT, 'workflow native run phase and status are not a declared pair');
END;
CREATE TRIGGER workflow_native_runs_status_registry_update
BEFORE UPDATE OF phase,status ON workflow_native_runs FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM workflow_native_run_statuses WHERE phase=NEW.phase AND status=NEW.status)
BEGIN
    SELECT RAISE(ABORT, 'workflow native run phase and status are not a declared pair');
END;

INSERT OR IGNORE INTO fold_guard(active) VALUES (1);
UPDATE work_items SET kind=kind;
UPDATE workflow_native_runs SET phase=phase,status=status;
DELETE FROM fold_guard;
		`,
	},
	{
		Version: 50,
		Name:    "display_name_bounds",
		SQL: `
-- The agent read surface bounds a Product or Project display name at 256
-- characters. Without the same bound at capture, a longer name is storable and
-- then fails outbound schema validation on every read, so the row becomes
-- authority that no agent can represent. SQLite cannot add a CHECK to an
-- existing table, and rebuilding these two tables would drop the foreign keys
-- that make Project identity stable, so the bound is a trigger.
CREATE TRIGGER products_display_name_bound_insert
BEFORE INSERT ON products FOR EACH ROW
WHEN length(NEW.display_name) NOT BETWEEN 1 AND 256
BEGIN
    SELECT RAISE(ABORT, 'Product display name must be 1 to 256 characters');
END;
CREATE TRIGGER products_display_name_bound_update
BEFORE UPDATE OF display_name ON products FOR EACH ROW
WHEN length(NEW.display_name) NOT BETWEEN 1 AND 256
BEGIN
    SELECT RAISE(ABORT, 'Product display name must be 1 to 256 characters');
END;

CREATE TRIGGER projects_display_name_bound_insert
BEFORE INSERT ON projects FOR EACH ROW
WHEN length(NEW.display_name) NOT BETWEEN 1 AND 256
BEGIN
    SELECT RAISE(ABORT, 'Project display name must be 1 to 256 characters');
END;
CREATE TRIGGER projects_display_name_bound_update
BEFORE UPDATE OF display_name ON projects FOR EACH ROW
WHEN length(NEW.display_name) NOT BETWEEN 1 AND 256
BEGIN
    SELECT RAISE(ABORT, 'Project display name must be 1 to 256 characters');
END;

-- Prove the stored corpus already conforms. A no-op update fires the new
-- triggers on every existing row, so migration refuses rather than admitting a
-- row the read surface cannot return.
INSERT OR IGNORE INTO fold_guard(active) VALUES (1);
UPDATE products SET display_name=display_name;
UPDATE projects SET display_name=display_name;
DELETE FROM fold_guard;
		`,
	},
	{
		Version: 51,
		Name:    "product_knowledge_home_fold_guards",
		SQL: `
-- Product knowledge homes are event-folded operator configuration (PM6 §2/§3),
-- like every other Product projection. The fold guard refuses direct writes
-- outside a fold.
CREATE TRIGGER product_knowledge_homes_guard_insert
BEFORE INSERT ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER product_knowledge_homes_guard_update
BEFORE UPDATE ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
CREATE TRIGGER product_knowledge_homes_guard_delete
BEFORE DELETE ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is fold-only')
    WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1);
END;
		`,
	},
	{
		Version: 52,
		Name:    "home_pair_identity_binding",
		SQL: `
-- The eleven Git-derived knowledge tables and product_knowledge_homes key
-- their rows on a (home_project_id, home_locator_id) pair that has no foreign
-- key to Project identity: every existing constraint on those columns points
-- inside the law/domain cluster, so an unanchored pair was storable (issue
-- #539). A composite foreign key cannot be added without rebuilding every
-- table: migrations run inside one transaction where PRAGMA foreign_keys is a
-- no-op, and the rebuilt set includes non-leaf parents (law_subjects,
-- archived_work) whose referencing children hold immediate keys, so the
-- cascade reaches roughly twenty hand-copied definitions where one missed
-- CHECK silently weakens an existing constraint. The house pattern for
-- binding an existing table is a trigger (migrations 20, 49, 50, 51), so the
-- pair is bound here: child-side triggers refuse a row whose pair names no
-- project_locators row, and a parent-side trigger refuses removing a locator
-- that Git-derived knowledge still references. Locators are stable identity
-- (PM6 section 7); attribute updates keep the pair valid without removal.
CREATE UNIQUE INDEX project_locators_project_locator
    ON project_locators(project_id, locator_id);
CREATE TRIGGER archived_work_home_pair_bound_insert
BEFORE INSERT ON archived_work FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'archived_work home pair does not reference a Project locator');
END;
CREATE TRIGGER archived_work_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON archived_work FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'archived_work home pair does not reference a Project locator');
END;
CREATE TRIGGER knowledge_index_watermark_home_pair_bound_insert
BEFORE INSERT ON knowledge_index_watermark FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'knowledge_index_watermark home pair does not reference a Project locator');
END;
CREATE TRIGGER knowledge_index_watermark_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON knowledge_index_watermark FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'knowledge_index_watermark home pair does not reference a Project locator');
END;
CREATE TRIGGER knowledge_kind_coverage_home_pair_bound_insert
BEFORE INSERT ON knowledge_kind_coverage FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'knowledge_kind_coverage home pair does not reference a Project locator');
END;
CREATE TRIGGER knowledge_kind_coverage_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON knowledge_kind_coverage FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'knowledge_kind_coverage home pair does not reference a Project locator');
END;
CREATE TRIGGER law_subjects_home_pair_bound_insert
BEFORE INSERT ON law_subjects FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_subjects home pair does not reference a Project locator');
END;
CREATE TRIGGER law_subjects_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON law_subjects FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_subjects home pair does not reference a Project locator');
END;
CREATE TRIGGER law_relations_home_pair_bound_insert
BEFORE INSERT ON law_relations FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_relations home pair does not reference a Project locator');
END;
CREATE TRIGGER law_relations_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON law_relations FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_relations home pair does not reference a Project locator');
END;
CREATE TRIGGER domains_home_pair_bound_insert
BEFORE INSERT ON domains FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domains home pair does not reference a Project locator');
END;
CREATE TRIGGER domains_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON domains FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domains home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_registries_home_pair_bound_insert
BEFORE INSERT ON domain_registries FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_registries home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_registries_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON domain_registries FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_registries home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_architecture_relations_home_pair_bound_insert
BEFORE INSERT ON domain_architecture_relations FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_architecture_relations home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_architecture_relations_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON domain_architecture_relations FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_architecture_relations home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_relation_governing_laws_home_pair_bound_insert
BEFORE INSERT ON domain_relation_governing_laws FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_relation_governing_laws home pair does not reference a Project locator');
END;
CREATE TRIGGER domain_relation_governing_laws_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON domain_relation_governing_laws FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'domain_relation_governing_laws home pair does not reference a Project locator');
END;
CREATE TRIGGER law_domain_homes_home_pair_bound_insert
BEFORE INSERT ON law_domain_homes FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_domain_homes home pair does not reference a Project locator');
END;
CREATE TRIGGER law_domain_homes_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON law_domain_homes FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_domain_homes home pair does not reference a Project locator');
END;
CREATE TRIGGER law_domain_applicability_home_pair_bound_insert
BEFORE INSERT ON law_domain_applicability FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_domain_applicability home pair does not reference a Project locator');
END;
CREATE TRIGGER law_domain_applicability_home_pair_bound_update
BEFORE UPDATE OF home_project_id, home_locator_id ON law_domain_applicability FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.home_project_id AND pl.locator_id = NEW.home_locator_id)
BEGIN
    SELECT RAISE(ABORT, 'law_domain_applicability home pair does not reference a Project locator');
END;
CREATE TRIGGER product_knowledge_homes_pair_bound_insert
BEFORE INSERT ON product_knowledge_homes FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.project_id AND pl.locator_id = NEW.locator_id)
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes pair does not reference a Project locator');
END;
CREATE TRIGGER product_knowledge_homes_pair_bound_update
BEFORE UPDATE OF project_id, locator_id ON product_knowledge_homes FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM project_locators pl
                 WHERE pl.project_id = NEW.project_id AND pl.locator_id = NEW.locator_id)
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes pair does not reference a Project locator');
END;
CREATE TRIGGER project_locators_referenced_by_knowledge_no_delete
BEFORE DELETE ON project_locators FOR EACH ROW
WHEN     EXISTS (SELECT 1 FROM archived_work k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM knowledge_index_watermark k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM knowledge_kind_coverage k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM law_subjects k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM law_relations k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM domains k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM domain_registries k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM domain_architecture_relations k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM domain_relation_governing_laws k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM law_domain_homes k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
 OR
    EXISTS (SELECT 1 FROM law_domain_applicability k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
BEGIN
    SELECT RAISE(ABORT, 'Project locator is referenced by Git-derived knowledge');
END;


-- Prove the stored corpus already conforms: a no-op update of the bound
-- columns fires the new triggers on every existing row, so migration refuses
-- rather than admitting an unanchored pair the binding then protects.
INSERT OR IGNORE INTO fold_guard(active) VALUES (1);
UPDATE archived_work SET home_project_id=home_project_id;
UPDATE knowledge_index_watermark SET home_project_id=home_project_id;
UPDATE knowledge_kind_coverage SET home_project_id=home_project_id;
UPDATE law_subjects SET home_project_id=home_project_id;
UPDATE law_relations SET home_project_id=home_project_id;
UPDATE domains SET home_project_id=home_project_id;
UPDATE domain_registries SET home_project_id=home_project_id;
UPDATE domain_architecture_relations SET home_project_id=home_project_id;
UPDATE domain_relation_governing_laws SET home_project_id=home_project_id;
UPDATE law_domain_homes SET home_project_id=home_project_id;
UPDATE law_domain_applicability SET home_project_id=home_project_id;
UPDATE product_knowledge_homes SET project_id=project_id;
DELETE FROM fold_guard;
		`,
	},
	{
		Version: 53,
		Name:    "native_run_verification_state_check",
		SQL: `
DROP TRIGGER IF EXISTS workflow_native_runs_guard_insert;
DROP TRIGGER IF EXISTS workflow_native_runs_guard_update;
DROP TRIGGER IF EXISTS workflow_native_runs_guard_delete;
DROP TRIGGER IF EXISTS workflow_native_runs_status_registry_insert;
DROP TRIGGER IF EXISTS workflow_native_runs_status_registry_update;
ALTER TABLE workflow_native_runs RENAME TO workflow_native_runs_v53;
CREATE TABLE workflow_native_runs (
    work_id                TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    run_id                 TEXT NOT NULL CHECK(length(run_id) BETWEEN 1 AND 128),
    phase                  TEXT NOT NULL CHECK(phase IN ('start','health','rollback','cleanup')),
    status                 TEXT NOT NULL,
    event_id               TEXT NOT NULL,
    reporting_authority_ref TEXT NOT NULL CHECK(length(reporting_authority_ref) BETWEEN 1 AND 128),
    actor_ref              TEXT NOT NULL CHECK(length(actor_ref) BETWEEN 1 AND 256),
    native_subject_ref     TEXT NOT NULL CHECK(length(native_subject_ref) BETWEEN 1 AND 2048),
    subject_digest         TEXT NOT NULL CHECK(length(subject_digest)=71 AND substr(subject_digest,1,7)='sha256:'),
    evidence_ref           TEXT NOT NULL CHECK(length(evidence_ref) BETWEEN 1 AND 2048),
    evidence_digest        TEXT NOT NULL CHECK(length(evidence_digest) BETWEEN 1 AND 256),
    asserted_at            TEXT NOT NULL,
    recorded_at            TEXT NOT NULL,
    capture_method         TEXT NOT NULL CHECK(capture_method='trusted_client_report'),
    observed_universe      TEXT NOT NULL CHECK(json_valid(observed_universe) AND json_type(observed_universe)='object'),
    freshness_policy_ref   TEXT NOT NULL CHECK(length(freshness_policy_ref) BETWEEN 1 AND 256),
    divergence_policy_ref  TEXT NOT NULL CHECK(length(divergence_policy_ref) BETWEEN 1 AND 256),
    observation_id         TEXT,
    verification_state     TEXT NOT NULL DEFAULT 'unverified' CHECK(verification_state IN ('unverified','verified','diverged_expected','diverged_unexpected','unverifiable')),
    PRIMARY KEY(work_id, run_id, phase)
);
INSERT INTO workflow_native_runs
    (work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state)
    SELECT work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state
    FROM workflow_native_runs_v53;
DROP TABLE workflow_native_runs_v53;
CREATE INDEX workflow_native_runs_work ON workflow_native_runs(work_id, run_id);
CREATE TRIGGER workflow_native_runs_guard_insert BEFORE INSERT ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_update BEFORE UPDATE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_delete BEFORE DELETE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_status_registry_insert
BEFORE INSERT ON workflow_native_runs FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM workflow_native_run_statuses WHERE phase=NEW.phase AND status=NEW.status)
BEGIN
    SELECT RAISE(ABORT, 'workflow native run phase and status are not a declared pair');
END;
CREATE TRIGGER workflow_native_runs_status_registry_update
BEFORE UPDATE OF phase,status ON workflow_native_runs FOR EACH ROW
WHEN NOT EXISTS (SELECT 1 FROM workflow_native_run_statuses WHERE phase=NEW.phase AND status=NEW.status)
BEGIN
    SELECT RAISE(ABORT, 'workflow native run phase and status are not a declared pair');
END;
		`,
	},
	{
		Version: 54,
		Name:    "rigor_class_vocabulary",
		SQL: `
CREATE TRIGGER workflow_contracts_rigor_class_insert
BEFORE INSERT ON workflow_contracts FOR EACH ROW
WHEN NEW.rigor_class NOT IN (
    'prototype_internal', 'prototype_trusted', 'prototype_public', 'prototype_safety_critical',
    'production_internal', 'production_trusted', 'production_public', 'production_safety_critical',
    'critical_internal', 'critical_trusted', 'critical_public', 'critical_safety_critical'
)
BEGIN
    SELECT RAISE(ABORT, 'workflow contract rigor class is not a declared maturity-audience composition');
END;
CREATE TRIGGER workflow_contracts_rigor_class_update
BEFORE UPDATE OF rigor_class ON workflow_contracts FOR EACH ROW
WHEN NEW.rigor_class NOT IN (
    'prototype_internal', 'prototype_trusted', 'prototype_public', 'prototype_safety_critical',
    'production_internal', 'production_trusted', 'production_public', 'production_safety_critical',
    'critical_internal', 'critical_trusted', 'critical_public', 'critical_safety_critical'
)
BEGIN
    SELECT RAISE(ABORT, 'workflow contract rigor class is not a declared maturity-audience composition');
END;

INSERT OR IGNORE INTO fold_guard(active) VALUES (1);
UPDATE workflow_contracts SET rigor_class=rigor_class;
DELETE FROM fold_guard;
		`,
	},
	{
		Version: 55,
		Name:    "approval_consequence_surface_closure",
		SQL: `
DROP INDEX agent_approvals_lookup;
DROP INDEX agent_approval_challenges_grant;
ALTER TABLE agent_approvals RENAME TO agent_approvals_v55;
ALTER TABLE agent_approval_challenges RENAME TO agent_approval_challenges_v55;
CREATE TABLE agent_approvals (
    approval_ref TEXT PRIMARY KEY,
    operation_digest TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK(json_valid(scope_json) AND json_type(scope_json)='object'),
    version_json TEXT NOT NULL CHECK(json_valid(version_json) AND json_type(version_json)='object'),
    consequence TEXT NOT NULL CHECK(consequence IN ('read','intent','lifecycle','workflow_action','scope','relation','supersession','publication','recovery','research','claim')),
    human_principal_ref TEXT NOT NULL,
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    session_ref TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    max_uses INTEGER NOT NULL CHECK(max_uses > 0 AND max_uses <= 100),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
    revoked_at TEXT,
    protected_evidence_ref TEXT NOT NULL,
    protected_evidence_digest TEXT NOT NULL,
    CHECK(length(approval_ref) = 64),
    CHECK(length(operation_digest) > 0 AND length(operation_digest) <= 128),
    CHECK(length(human_principal_ref) > 0 AND length(human_principal_ref) <= 128),
    CHECK(length(session_ref) > 0 AND length(session_ref) <= 128),
    CHECK(length(protected_evidence_ref) > 0 AND length(protected_evidence_ref) <= 256),
    CHECK(length(protected_evidence_digest) > 0 AND length(protected_evidence_digest) <= 128),
    CHECK(expires_at > issued_at)
);
INSERT INTO agent_approvals
    (approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,revoked_at,protected_evidence_ref,protected_evidence_digest)
    SELECT approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,revoked_at,protected_evidence_ref,protected_evidence_digest
    FROM agent_approvals_v55;
DROP TABLE agent_approvals_v55;
CREATE TABLE agent_approval_challenges (
    challenge_ref TEXT PRIMARY KEY,
    grant_ref TEXT NOT NULL REFERENCES agent_grants(grant_ref) ON DELETE RESTRICT,
    operation_digest TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK(json_valid(scope_json) AND json_type(scope_json)='object'),
    version_json TEXT NOT NULL CHECK(json_valid(version_json) AND json_type(version_json)='object'),
    consequence TEXT NOT NULL CHECK(consequence IN ('read','intent','lifecycle','workflow_action','scope','relation','supersession','publication','recovery','research','claim')),
    host_assertion_digest TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','consumed','revoked')),
    consumed_at TEXT,
    max_uses INTEGER NOT NULL DEFAULT 1 CHECK(max_uses > 0 AND max_uses <= 100),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
    CHECK(length(challenge_ref) = 64),
    CHECK(length(operation_digest) > 0 AND length(operation_digest) <= 128),
    CHECK(length(host_assertion_digest) > 0 AND length(host_assertion_digest) <= 128),
    CHECK(expires_at > issued_at),
    CHECK((status='active' AND consumed_at IS NULL) OR (status IN ('consumed','revoked')))
);
INSERT INTO agent_approval_challenges
    (challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count)
    SELECT challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count
    FROM agent_approval_challenges_v55;
DROP TABLE agent_approval_challenges_v55;
CREATE INDEX agent_approvals_lookup ON agent_approvals(client_ref, session_ref, operation_digest);
CREATE INDEX agent_approval_challenges_grant ON agent_approval_challenges(grant_ref, status);
		`,
	},
	{
		Version: 56,
		Name:    "archived_work_kind_vocabulary",
		SQL: `
CREATE TRIGGER archived_work_kind_insert
BEFORE INSERT ON archived_work FOR EACH ROW
WHEN NEW.type NOT IN (
    'work_note', 'constitution', 'decision', 'spec', 'lesson', 'reference', 'research'
)
BEGIN
    SELECT RAISE(ABORT, 'archived work type is not a declared knowledge kind');
END;
CREATE TRIGGER archived_work_kind_update
BEFORE UPDATE OF type ON archived_work FOR EACH ROW
WHEN NEW.type NOT IN (
    'work_note', 'constitution', 'decision', 'spec', 'lesson', 'reference', 'research'
)
BEGIN
    SELECT RAISE(ABORT, 'archived work type is not a declared knowledge kind');
END;

INSERT OR IGNORE INTO fold_guard(active) VALUES (1);
UPDATE archived_work SET type=type;
DELETE FROM fold_guard;
		`,
	},
	{
		Version: 57,
		Name:    "capability_scoping_replaces_the_grant_token",
		SQL: `
DROP INDEX agent_approval_challenges_grant;
ALTER TABLE agent_approval_challenges RENAME TO agent_approval_challenges_v57;
CREATE TABLE agent_approval_challenges (
    challenge_ref TEXT PRIMARY KEY,
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    principal_ref TEXT NOT NULL,
    session_ref TEXT NOT NULL,
    agent_ref TEXT NOT NULL,
    directory TEXT NOT NULL,
    worktree TEXT NOT NULL,
    product_scope_json TEXT NOT NULL CHECK(json_valid(product_scope_json) AND json_type(product_scope_json)='array'),
    operation_digest TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK(json_valid(scope_json) AND json_type(scope_json)='object'),
    version_json TEXT NOT NULL CHECK(json_valid(version_json) AND json_type(version_json)='object'),
    consequence TEXT NOT NULL CHECK(consequence IN ('read','intent','lifecycle','workflow_action','scope','relation','supersession','publication','recovery','research','claim')),
    host_assertion_digest TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','consumed','revoked')),
    consumed_at TEXT,
    max_uses INTEGER NOT NULL DEFAULT 1 CHECK(max_uses > 0 AND max_uses <= 100),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
    CHECK(length(challenge_ref) = 64),
    CHECK(length(principal_ref) > 0 AND length(principal_ref) <= 128),
    CHECK(length(session_ref) > 0 AND length(session_ref) <= 128),
    CHECK(length(agent_ref) > 0 AND length(agent_ref) <= 128),
    CHECK(length(directory) > 0 AND length(directory) <= 4096),
    CHECK(length(worktree) > 0 AND length(worktree) <= 4096),
    CHECK(length(operation_digest) > 0 AND length(operation_digest) <= 128),
    CHECK(length(host_assertion_digest) > 0 AND length(host_assertion_digest) <= 128),
    CHECK(expires_at > issued_at),
    CHECK((status='active' AND consumed_at IS NULL) OR (status IN ('consumed','revoked')))
);
INSERT INTO agent_approval_challenges
    (challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count)
    SELECT c.challenge_ref,g.client_ref,g.principal_ref,g.session_ref,g.agent_ref,g.directory,g.worktree,g.product_scope_json,c.operation_digest,c.scope_json,c.version_json,c.consequence,c.host_assertion_digest,c.issued_at,c.expires_at,c.status,c.consumed_at,c.max_uses,c.used_count
    FROM agent_approval_challenges_v57 c
    JOIN agent_grants g ON g.grant_ref = c.grant_ref;
DROP TABLE agent_approval_challenges_v57;
CREATE INDEX agent_approval_challenges_session ON agent_approval_challenges(client_ref, session_ref, status);
CREATE INDEX agent_approval_challenges_active ON agent_approval_challenges(status, issued_at);
DROP INDEX agent_grants_lookup;
DROP TABLE agent_grants;
		`,
	},
	{
		Version: 58,
		Name:    "work_bootstrap_operations",
		SQL: `
CREATE TABLE bootstrap_operations (
    idempotency_key TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE,
    request_digest TEXT NOT NULL CHECK(length(request_digest) = 71),
    request_json TEXT NOT NULL CHECK(json_valid(request_json) AND json_type(request_json)='object'),
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    work_id TEXT NOT NULL UNIQUE REFERENCES work_items(id) ON DELETE RESTRICT,
    repo_path TEXT NOT NULL,
    expected_version INTEGER NOT NULL CHECK(expected_version > 0),
    state TEXT NOT NULL CHECK(state IN ('pending','creating','native_ready','completed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX bootstrap_operations_state ON bootstrap_operations(state, updated_at);
		`,
	},
	{
		Version: 59,
		Name:    "work_bootstrap_launch_recovery",
		SQL: `
DROP INDEX bootstrap_operations_state;
ALTER TABLE bootstrap_operations RENAME TO bootstrap_operations_v58;
CREATE TABLE bootstrap_operations (
    idempotency_key TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE,
    request_digest TEXT NOT NULL CHECK(length(request_digest) = 71),
    request_json TEXT NOT NULL CHECK(json_valid(request_json) AND json_type(request_json)='object'),
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    work_id TEXT NOT NULL UNIQUE REFERENCES work_items(id) ON DELETE RESTRICT,
    repo_path TEXT NOT NULL,
    expected_version INTEGER NOT NULL CHECK(expected_version > 0),
    state TEXT NOT NULL CHECK(state IN ('pending','creating','native_ready','completed','rolling_back','rolled_back')),
    launch_state TEXT NOT NULL DEFAULT 'not_started' CHECK(launch_state IN ('not_started','prepared','running','completed','failed')),
    launch_attempt_id TEXT UNIQUE,
    launch_session_id TEXT UNIQUE,
    launch_owner_pid INTEGER,
    launch_owner_start TEXT,
    launch_process_pid INTEGER,
    launch_process_start TEXT,
    launch_agent TEXT,
    launch_directory TEXT,
    launch_model TEXT,
    launch_error TEXT,
    launch_started_at TEXT,
    launch_finished_at TEXT,
    failure_reason TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO bootstrap_operations
    (idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at)
    SELECT idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at
    FROM bootstrap_operations_v58;
DROP TABLE bootstrap_operations_v58;
CREATE INDEX bootstrap_operations_state ON bootstrap_operations(state, updated_at);
CREATE INDEX bootstrap_operations_launch_state ON bootstrap_operations(launch_state, updated_at);
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

// CurrentSchemaVersion returns the highest checksummed migration known to this
// binary.
func CurrentSchemaVersion() int {
	return migrations[len(migrations)-1].Version
}

// migrationLockBudget bounds how long a concurrent opener waits for another
// process to finish migrating. Every migration lengthens the single migrating
// transaction while the connection busy timeout stays fixed, so one timeout is
// not a safe ceiling: the budget must cover a full manifest apply, not one
// lock acquisition. It is bounded to fail rather than hang.
const migrationLockBudget = 90 * time.Second

const (
	migrationRetryInitialDelay = 25 * time.Millisecond
	migrationRetryMaxDelay     = 500 * time.Millisecond
)

// Migrate brings the database up to this binary's schema version. It is
// idempotent, applies the manifest and all pending steps in one transaction,
// and fails closed on drift or on a database written by a newer binary.
//
// Concurrent openers of one database are serialized by SQLite. The first
// caller applies the manifest; the rest observe a busy database. Because the
// migrating transaction grows with every added step while the connection busy
// timeout does not, a single blocked attempt is retried within a bounded
// budget, and each retry re-checks the read-only fast path so a waiter returns
// as soon as the winner commits.
func Migrate(ctx context.Context, db *sql.DB, clock ...func() time.Time) error {
	deadline := time.Now().Add(migrationLockBudget)
	delay := migrationRetryInitialDelay
	for {
		current, err := migrationManifestCurrent(ctx, db)
		if err != nil {
			return err
		}
		if current {
			return nil
		}
		err = migrateOnce(ctx, db, clock...)
		if err == nil {
			return nil
		}
		if !migrationLockContended(err) || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return wrapFailure(KindUnavailable, "migrate", "schema migration was cancelled while waiting for another process", true,
				"retry once the database is writable", ctx.Err())
		case <-timer.C:
		}
		if delay < migrationRetryMaxDelay {
			delay *= 2
			if delay > migrationRetryMaxDelay {
				delay = migrationRetryMaxDelay
			}
		}
	}
}

// migrationManifestCurrent reports whether the database is already at this
// binary's schema version. It reads without opening a write transaction, so a
// routine open of an up-to-date database never contends for the write lock.
// Drift and newer-binary detection still run here: skipping the write
// transaction must never skip the manifest check.
func migrationManifestCurrent(ctx context.Context, db *sql.DB) (bool, error) {
	var present string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&present)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		if migrationLockContended(err) {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "migrate", "cannot inspect the schema manifest", true,
			"confirm the database is readable", err)
	}
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return false, nil
	}
	if err := checkManifest(applied); err != nil {
		return false, err
	}
	for _, m := range migrations {
		if _, done := applied[m.Version]; !done {
			return false, nil
		}
	}
	return true, nil
}

// migrationLockContended reports whether a failure means another process holds
// the write lock. It never treats drift, unsupported schema, or a membership
// migration requirement as contention.
func migrationLockContended(err error) bool {
	var failure *Failure
	if errors.As(err, &failure) {
		if failure.Kind != KindUnavailable {
			return false
		}
		err = failure.Err
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "database table is locked")
}

func migrateOnce(ctx context.Context, db *sql.DB, clock ...func() time.Time) error {
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
			m.Version, m.Name, m.checksum(), nowFromClock(firstClock(clock)).Format(time.RFC3339Nano)); err != nil {
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

// appliedMigrations takes a queryer so the manifest can be read without
// opening a write transaction.
func appliedMigrations(ctx context.Context, tx queryer) (map[int]string, error) {
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

// migrationShippedVariantChecksums lists, per migration version, the SHA-256
// checksums of SQL texts that differ from this binary's definition but shipped
// in published releases. Thirteen migrations had their text edited after
// first release (digest corrections, reformatting, comment edits), so
// databases created by those releases recorded checksums no later binary
// reproduced. The manifest check accepts exactly these recorded variants and
// no others: a fresh edit to an applied migration still fails, and the table
// is closed — extending it requires naming the release range that shipped
// the new variant.
var migrationShippedVariantChecksums = map[int][]string{
	3:  {"c8ca3aa3d712044cab66d22184c20cae39472401fd2ea778f29b3c50dee94b90"},
	7:  {"5c5d5aa28ef3d5bac4a345a860d700410c81821dc7a68ddb6930205f99c1b60d"},
	8:  {"27f8a3b16b6f91cd9426c1ac2ebf8ca740f97c2384831bb5af435c9da917ce45"},
	9:  {"3259fafb1a03e42c9bf517c7bd7944dc878dc8b2141ea4377bde096e1c340caa", "cca4181d6aab84794437c6a1af2c17699464bd91669de267e62e87a1bd2083dc"},
	15: {"35bd6711599c74b0706803c5bd573e5f3f77d021f8d67ce1c4899de3528519a7"},
	16: {"0a2320b819cb1ddde64959f35706626585d886814362f211890017231c798824"},
	18: {"e9c11ac13ccec24c316cb4f1c01424e6a41532dcc7cdb016f15abe7e87aed844"},
	20: {"8b7c75bf900d33d78877080b5072460bffd52038101881757ffb6144d4e498cc"},
	22: {"25ad36500176b287b84f68452da329835b9e813b9357a424e9f2ece6b79fe8c1"},
	25: {"d9aa7f0af4181da194cc2628f811efb89291c1e44039a420efa06fbec4e04f95"},
	26: {"d67c0de98eb09bd893c4eaaa09ddb6457ddfaef39834dca138eed2a95f5c99e4"},
	35: {"d56ac4ee075336870ce89a117d23fda2b5252dfb561362f293730d243fc1c154"},
	36: {"36fbfca75d718d656bba2d8b81dcbe372ad0a025b44239672ed2e3de2f25d129"},
	37: {"680277cff79364bdfcaae1cf518a3939079cfc9154b8a338a41d6f6237ecdbec"},
	39: {"51a509c1cda0d1d992b205944c2758eac0aef5c87ca88f15df9b19f8b0d3060a"},
	40: {"95793496a0186de993c15950c3209cada38fe6d5e00985e53edab54d0a75519d", "c0da41426b30025a1e2c2ec3e1d6276b6964e074a39fba1678ac8f8b59bf89ff"},
}

func shippedVariantAccepted(version int, checksum string) bool {
	for _, variant := range migrationShippedVariantChecksums[version] {
		if variant == checksum {
			return true
		}
	}
	return false
}

// checkManifest compares manifest against this binary's
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
		if m.checksum() != checksum && !shippedVariantAccepted(version, checksum) {
			return newFailure(KindSchemaDrift, "migrate",
				fmt.Sprintf("migration %d (%s) no longer matches its recorded checksum", version, m.Name),
				false, "restore the original migration definition; applied migrations are immutable")
		}
	}
	return nil
}
