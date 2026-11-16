package store

const schemaBaseline198 = `
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
CREATE TABLE agent_clients (
    client_ref TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK(status IN ('active','revoked')),
    principal_ref TEXT NOT NULL,
    capabilities_json TEXT NOT NULL CHECK(json_valid(capabilities_json) AND json_type(capabilities_json)='array' AND json_array_length(capabilities_json) <= 32),
    product_scope_json TEXT NOT NULL CHECK(json_valid(product_scope_json) AND json_type(product_scope_json)='array' AND json_array_length(product_scope_json) <= 100),
    project_scope_json TEXT NOT NULL CHECK(json_valid(project_scope_json) AND json_type(project_scope_json)='array' AND json_array_length(project_scope_json) <= 100),
    created_at TEXT NOT NULL,
    rotated_at TEXT,
    revoked_at TEXT, agent_scope_json TEXT NOT NULL DEFAULT '[]'
    CHECK(json_valid(agent_scope_json) AND json_type(agent_scope_json)='array' AND json_array_length(agent_scope_json) <= 100), policy_revision INTEGER NOT NULL DEFAULT 1,
    CHECK(length(client_ref) > 0 AND length(client_ref) <= 128),
    CHECK(length(principal_ref) > 0 AND length(principal_ref) <= 128),
    CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL))
);
CREATE TABLE agent_installation_keys (
    key_name  TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL CHECK(length(key_bytes) = 32),
    created_at TEXT NOT NULL
);
CREATE TABLE agent_nonce_replay (
    client_ref TEXT NOT NULL REFERENCES agent_clients(client_ref) ON DELETE RESTRICT,
    nonce TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY(client_ref, nonce),
    CHECK(length(nonce) >= 16 AND length(nonce) <= 256),
    CHECK(length(observed_at) > 0 AND length(expires_at) > 0)
);
CREATE TABLE archived_work (
    id              TEXT NOT NULL,
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
    content_hash    TEXT NOT NULL,
    scope_mode      TEXT NOT NULL DEFAULT 'explicit' CHECK(scope_mode IN ('home','explicit')),
    manifest_schema_version TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(home_project_id, home_locator_id, id)
);
CREATE TABLE archived_work_domains (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    work_id TEXT NOT NULL,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(home_project_id, home_locator_id, work_id, domain_id),
    FOREIGN KEY(home_project_id, home_locator_id, work_id) REFERENCES archived_work(home_project_id, home_locator_id, id) ON DELETE RESTRICT
);
CREATE TABLE archived_work_products (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    work_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, work_id, product_id),
    FOREIGN KEY(home_project_id, home_locator_id, work_id) REFERENCES archived_work(home_project_id, home_locator_id, id) ON DELETE RESTRICT
);
CREATE TABLE archived_work_projects (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    work_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, work_id, project_id),
    FOREIGN KEY(home_project_id, home_locator_id, work_id) REFERENCES archived_work(home_project_id, home_locator_id, id) ON DELETE RESTRICT
);
CREATE TABLE archived_work_tags (
    home_project_id TEXT NOT NULL,
    home_locator_id TEXT NOT NULL,
    work_id TEXT NOT NULL,
    tag_id TEXT NOT NULL,
    PRIMARY KEY(home_project_id, home_locator_id, work_id, tag_id),
    FOREIGN KEY(home_project_id, home_locator_id, work_id) REFERENCES archived_work(home_project_id, home_locator_id, id) ON DELETE RESTRICT
);
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
    CHECK((state = 'dismissed') = (dismissed_at IS NOT NULL))
);
CREATE TABLE durability_commits (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    bit INTEGER NOT NULL CHECK(bit IN (0,1))
);
CREATE TABLE "durable_operations" (
 op_id TEXT PRIMARY KEY,
 attempt_epoch INTEGER NOT NULL DEFAULT 1 CHECK(attempt_epoch=1),
 work_id TEXT NOT NULL,
 workflow_type_ref TEXT NOT NULL,
 workflow_type_digest TEXT NOT NULL DEFAULT '',
 step_id TEXT NOT NULL,
 step_kind TEXT NOT NULL DEFAULT 'internal_sqlite' CHECK(step_kind='internal_sqlite'),
 accepted_inputs_digest TEXT NOT NULL,
 accepted_scope_snapshot TEXT,
 result_kind TEXT NOT NULL DEFAULT 'completed' CHECK(result_kind='completed'),
 result_payload TEXT,
 evidence_refs TEXT NOT NULL DEFAULT '[]',
 changed_refs TEXT NOT NULL DEFAULT '[]',
 principal_ref TEXT NOT NULL,
 request_id TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 completed_at TEXT,
 contract_digest TEXT NOT NULL
);
CREATE TABLE effect_receipts (
 event_id TEXT PRIMARY KEY,
 effect_seq INTEGER NOT NULL UNIQUE,
 kind TEXT NOT NULL,
 subject_type TEXT NOT NULL,
 subject_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 payload_version INTEGER NOT NULL,
 payload_digest TEXT
, relation_seq INTEGER, delivery_assertion INTEGER NOT NULL DEFAULT 0);
CREATE TABLE "external_observations" (
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
    created_event_seq INTEGER NOT NULL REFERENCES effect_receipts(effect_seq) ON DELETE RESTRICT,
    verified_event_seq INTEGER REFERENCES effect_receipts(effect_seq) ON DELETE RESTRICT
);
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
    last_observed_at TEXT NOT NULL, result_payload TEXT NOT NULL DEFAULT '{}', changed_refs TEXT NOT NULL DEFAULT '[]', authorized_scope_snapshot TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY(principal_ref,tool,operation_kind,idempotency_key)
);
CREATE TABLE "linear_issue_links" (
    work_id            TEXT PRIMARY KEY CHECK(length(work_id) BETWEEN 2 AND 128),
    remote_issue_uuid  TEXT NOT NULL CHECK(length(remote_issue_uuid) BETWEEN 2 AND 128),
    human_key          TEXT NOT NULL CHECK(length(human_key) BETWEEN 1 AND 64),
    url                TEXT NOT NULL CHECK(length(url) BETWEEN 1 AND 2048),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE TABLE product_knowledge_homes (
    product_id TEXT PRIMARY KEY REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    locator_id TEXT NOT NULL REFERENCES project_locators(locator_id) ON DELETE RESTRICT,
    UNIQUE(project_id, locator_id)
);
CREATE TABLE product_projects (
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    role       TEXT NOT NULL CHECK(role IN ('primary','secondary')),
    PRIMARY KEY(product_id, project_id)
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
CREATE TABLE project_handoffs (
    handoff_id              TEXT    PRIMARY KEY,
    work_id                 TEXT    NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    contract_version        INTEGER NOT NULL CHECK(contract_version > 0),
    source_project_id       TEXT    NOT NULL,
    target_project_id       TEXT    NOT NULL,
    source_session_ref      TEXT    NOT NULL,
    bounded_job             TEXT    NOT NULL,
    changes                 TEXT    NOT NULL CHECK(json_valid(changes)),
    verification            TEXT    NOT NULL CHECK(json_valid(verification)),
    artifact_refs           TEXT    NOT NULL CHECK(json_valid(artifact_refs)),
    blockers                TEXT    NOT NULL CHECK(json_valid(blockers)),
    next_action             TEXT    NOT NULL,
    state                   TEXT    NOT NULL CHECK(state IN ('recorded','consumed')),
    consumed_by_session_ref TEXT    NOT NULL DEFAULT '',
    consumed_at             TEXT    NOT NULL DEFAULT '',
    recorded_at             TEXT    NOT NULL, recorded_seq INTEGER NOT NULL DEFAULT 0,
    CHECK(source_project_id <> target_project_id)
);
CREATE TABLE project_locators (
    locator_id       TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    kind             TEXT NOT NULL CHECK(kind IN ('git_remote','canonical_path')),
    locator_value    TEXT NOT NULL,
    normalized_value TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL, normalization_version INTEGER NOT NULL DEFAULT 1 CHECK(normalization_version IN (1,2)),
    CHECK(length(locator_id) > 0 AND length(locator_id) <= 128),
    CHECK(length(locator_value) > 0 AND length(locator_value) <= 4096),
    CHECK(length(normalized_value) > 0 AND length(normalized_value) <= 4096),
    UNIQUE(kind, normalized_value)
);
CREATE TABLE projects (
    id           TEXT    PRIMARY KEY,
    display_name TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
, stage_maturity_override TEXT
    CHECK(stage_maturity_override IS NULL OR stage_maturity_override IN ('prototype','alpha','beta','production','deprecated')), stage_audience_commitment_override TEXT
    CHECK(stage_audience_commitment_override IS NULL OR stage_audience_commitment_override IN ('operator_only','limited','public')), repository_mode TEXT NOT NULL DEFAULT 'off' CHECK(repository_mode IN ('on','off')));
CREATE TABLE "relations" (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 work_id_from TEXT NOT NULL REFERENCES work_items(id),
 work_id_to TEXT NOT NULL REFERENCES work_items(id),
 kind TEXT NOT NULL CHECK(kind IN ('parent','blocks','implements','forward_link','raised_from','depends_on','compatible_with')),
 created_at TEXT NOT NULL,
 resolution_id TEXT, reason TEXT NOT NULL DEFAULT '', record_version INTEGER NOT NULL DEFAULT 1,
 CHECK(work_id_from<>work_id_to), UNIQUE(work_id_from,work_id_to,kind)
);
CREATE TABLE "runtime_state_writers" (
    surface TEXT NOT NULL CHECK(surface IN ('knowledge','workflow')),
    definition_ref TEXT NOT NULL,
    knowledge_projection_version INTEGER NOT NULL,
    digest TEXT NOT NULL,
    binary_version TEXT NOT NULL,
    PRIMARY KEY(surface, definition_ref, knowledge_projection_version, digest),
    CHECK((surface='knowledge' AND knowledge_projection_version > 0) OR
          (surface='workflow' AND knowledge_projection_version=0))
);
CREATE TABLE store_revision (
    id INTEGER PRIMARY KEY CHECK(id=1),
    revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>=0)
);
CREATE TABLE store_sequences (
 name TEXT PRIMARY KEY CHECK(name IN ('effect','relation')),
 last INTEGER NOT NULL CHECK(last>=0)
);
CREATE TABLE "work_items" (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL,
 title TEXT NOT NULL,
 lifecycle TEXT NOT NULL CHECK(lifecycle IN ('needed','in_progress','completed','cancelled')),
 version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 terminal_time TEXT,
 intent_json TEXT NOT NULL DEFAULT '{}',
 narrative TEXT NOT NULL DEFAULT '' CHECK(length(narrative)<=16384),
 effect_seq INTEGER NOT NULL DEFAULT 0 CHECK(typeof(effect_seq)='integer' AND effect_seq>=0),
 last_progress_at TEXT NOT NULL DEFAULT '',
 version_actions_floor INTEGER NOT NULL DEFAULT 0,
 transition_from TEXT,
 transition_reason TEXT NOT NULL DEFAULT '',
 transition_actor TEXT NOT NULL DEFAULT '',
 transition_evidence_refs TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(transition_evidence_refs) AND json_type(transition_evidence_refs)='array'),
 transition_seq INTEGER NOT NULL DEFAULT 0,
 transition_version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE work_kinds (
    kind         TEXT PRIMARY KEY,
    stored       INTEGER NOT NULL CHECK(stored IN (0,1)),
    fold_create  INTEGER NOT NULL CHECK(fold_create IN (0,1)),
    fold_revise  INTEGER NOT NULL CHECK(fold_revise IN (0,1)),
    agent_capture INTEGER NOT NULL CHECK(agent_capture IN (0,1))
);
CREATE TABLE work_projects (
    work_id    TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    role       TEXT NOT NULL CHECK(role IN ('primary','secondary')),
    PRIMARY KEY(work_id, project_id)
);
CREATE TABLE work_removal_operations (
    operation_id     TEXT PRIMARY KEY CHECK(length(operation_id) BETWEEN 2 AND 128),
    idempotency_key  TEXT NOT NULL UNIQUE CHECK(length(idempotency_key) BETWEEN 2 AND 128),
    work_id          TEXT NOT NULL UNIQUE CHECK(length(work_id) BETWEEN 2 AND 128),
    expected_version INTEGER NOT NULL CHECK(expected_version > 0),
    reason           TEXT NOT NULL CHECK(reason IN ('shelved','cancelled')),
    actor            TEXT NOT NULL CHECK(length(actor) BETWEEN 2 AND 128),
    product_id       TEXT NOT NULL DEFAULT '',
    handoff_json     TEXT NOT NULL CHECK(json_valid(handoff_json) AND json_type(handoff_json)='object'),
    handoff_digest   TEXT NOT NULL CHECK(length(handoff_digest)=71 AND substr(handoff_digest,1,7)='sha256:'),
    state            TEXT NOT NULL CHECK(state IN ('prepared','committed')),
    event_id         TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
, linear_confirmation_json TEXT NOT NULL DEFAULT '{}'
    CHECK(json_valid(linear_confirmation_json) AND json_type(linear_confirmation_json)='object'));
CREATE TABLE work_version_actions (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 resulting_version INTEGER NOT NULL,
 action_id TEXT NOT NULL,
 session_ref TEXT NOT NULL,
 effect_seq INTEGER NOT NULL,
 PRIMARY KEY(work_id,resulting_version,action_id)
);
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
CREATE TABLE workflow_architecture_bindings (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    home_domain_id TEXT NOT NULL CHECK(length(home_domain_id) BETWEEN 1 AND 256),
    projection_hash TEXT NOT NULL CHECK(length(projection_hash)=71 AND substr(projection_hash,1,7)='sha256:'),
    PRIMARY KEY(work_id, contract_version),
    UNIQUE(work_id, contract_version, product_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_contracts(work_id, contract_version) ON DELETE RESTRICT
);
CREATE TABLE "workflow_checkpoints" (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    checkpoint_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    step_kind TEXT NOT NULL CHECK(step_kind IN ('internal_sqlite','cross_authority','external_effect')),
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
CREATE TABLE workflow_clarify_records (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL CHECK(work_version > 0),
    payload TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload)='object'),
    recorded_at TEXT,
    actor TEXT REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    event_ref TEXT
);
CREATE TABLE workflow_completed_exits (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 definition_ref TEXT NOT NULL,
 step_id TEXT NOT NULL,
 action_id TEXT NOT NULL,
 exit_seq INTEGER NOT NULL,
 PRIMARY KEY(work_id,definition_ref,step_id,action_id)
);
CREATE TABLE workflow_contract_affected_domains (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, domain_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
CREATE TABLE workflow_contract_domain_additions (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    domain_id TEXT NOT NULL CHECK(length(domain_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, domain_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
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
CREATE TABLE workflow_contract_law_modifications (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    PRIMARY KEY(work_id, contract_version, law_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
CREATE TABLE workflow_contract_verification_obligations (
    work_id TEXT NOT NULL,
    contract_version INTEGER NOT NULL,
    law_id TEXT NOT NULL CHECK(length(law_id) BETWEEN 2 AND 256),
    obligation_id TEXT NOT NULL CHECK(length(obligation_id) BETWEEN 1 AND 256),
    PRIMARY KEY(work_id, contract_version, law_id, obligation_id),
    FOREIGN KEY(work_id, contract_version) REFERENCES workflow_architecture_bindings(work_id, contract_version) ON DELETE RESTRICT
);
CREATE TABLE "workflow_contracts" (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    contract_version INTEGER NOT NULL,
    consequence_class TEXT NOT NULL CHECK(consequence_class IN ('internal_sqlite','cross_authority','external_effect')),
    approved_at TEXT NOT NULL,
    approved_by TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    superseded_by INTEGER,
    spec_mandate TEXT NOT NULL CHECK(json_valid(spec_mandate) AND json_type(spec_mandate)='array'),
    law_modifies TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(law_modifies) AND json_type(law_modifies)='array' AND json_array_length(law_modifies) BETWEEN 0 AND 32),
    law_boundary_version INTEGER NOT NULL DEFAULT 0 CHECK(law_boundary_version IN (0,1)),
    self_repair_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(self_repair_json) AND json_type(self_repair_json) IN ('null','object')),
    supersession_seq INTEGER NOT NULL DEFAULT 0,
    last_healthy_seq INTEGER NOT NULL DEFAULT 0,
    changes_product_truth INTEGER CHECK(changes_product_truth IS NULL OR changes_product_truth IN (0,1)),
    legislative_subjects TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(legislative_subjects) AND json_type(legislative_subjects)='array'),
    PRIMARY KEY(work_id, contract_version),
    FOREIGN KEY(work_id, superseded_by) REFERENCES "workflow_contracts"(work_id, contract_version) ON DELETE RESTRICT,
    CHECK(contract_version > 0 AND contract_version <= 2147483647),
    CHECK(json_array_length(spec_mandate) BETWEEN 0 AND 32)
);
CREATE TABLE workflow_coordinator_actions (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 actor_ref TEXT NOT NULL,
 last_effect_seq INTEGER NOT NULL,
 last_action_id TEXT NOT NULL,
 last_acted_at TEXT NOT NULL,
 PRIMARY KEY(work_id,actor_ref)
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
CREATE TABLE "workflow_delivery_state" (
 work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
 assertion_seq INTEGER NOT NULL,
 assertion_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 payload_version INTEGER NOT NULL,
 payload TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload,'$.delivery_artifact')='text' AND length(json_extract(payload,'$.delivery_artifact'))>1 AND json_extract(payload,'$.delivery_state')='asserted')
);
CREATE TABLE workflow_design_records (
    work_id       TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version  INTEGER NOT NULL,
    approach      TEXT NOT NULL,
    decisions     TEXT NOT NULL CHECK(json_valid(decisions) AND json_type(decisions)='array'),
    touched_refs  TEXT NOT NULL CHECK(json_valid(touched_refs) AND json_type(touched_refs)='array'),
    recorded_at   TEXT NOT NULL,
    PRIMARY KEY(work_id, work_version),
    CHECK(work_version > 0),
    CHECK(length(approach) BETWEEN 2 AND 4096),
    CHECK(json_array_length(decisions) BETWEEN 1 AND 16),
    CHECK(json_array_length(touched_refs) BETWEEN 1 AND 64)
);
CREATE TABLE workflow_evidence_bindings (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 binding_seq INTEGER NOT NULL,
 binding_id TEXT NOT NULL UNIQUE,
 actor TEXT NOT NULL,
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 PRIMARY KEY(work_id,binding_seq)
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
CREATE TABLE "workflow_instances" (
 work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
 workflow_type TEXT CHECK(workflow_type IS NULL OR workflow_type IN ('change','fix','research','removal')),
 step_plan TEXT CHECK(step_plan IS NULL OR (json_valid(step_plan) AND json_type(step_plan)='object')),
 prelude_state TEXT CHECK(prelude_state IS NULL OR (json_valid(prelude_state) AND json_type(prelude_state)='object')),
 instance_state TEXT NOT NULL CHECK(instance_state IN ('intake','running','blocked','completed','cancelled')),
 execution_actor_ref TEXT REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
 execution_model TEXT NOT NULL DEFAULT '' CHECK(length(execution_model)<=128),
 started_at TEXT, completed_at TEXT, last_checkpoint_at TEXT, execution_started_at TEXT,
 completion_payload TEXT CHECK(completion_payload IS NULL OR json_valid(completion_payload)),
 completion_occurred_at TEXT
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
CREATE TABLE workflow_native_run_statuses (
    phase   TEXT NOT NULL,
    status  TEXT NOT NULL,
    failure INTEGER NOT NULL CHECK(failure IN (0,1)),
    PRIMARY KEY(phase,status)
);
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
CREATE TABLE "workflow_overlap_resolutions" (
 resolution_id TEXT PRIMARY KEY,
 event_seq INTEGER NOT NULL UNIQUE REFERENCES effect_receipts(effect_seq) ON DELETE RESTRICT,
 product_id TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
 from_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
 to_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
 from_contract_version INTEGER NOT NULL, to_contract_version INTEGER NOT NULL,
 resolution_kind TEXT NOT NULL CHECK(resolution_kind IN ('compatible_with','depends_on','blocks')),
 reason TEXT NOT NULL CHECK(length(reason) BETWEEN 1 AND 4096), created_at TEXT NOT NULL,
 invalidated_seq INTEGER REFERENCES effect_receipts(effect_seq) ON DELETE RESTRICT,
 CHECK(from_work_id<>to_work_id), CHECK(from_contract_version>0 AND to_contract_version>0),
 FOREIGN KEY(from_work_id,from_contract_version) REFERENCES workflow_contracts(work_id,contract_version) ON DELETE RESTRICT,
 FOREIGN KEY(to_work_id,to_contract_version) REFERENCES workflow_contracts(work_id,contract_version) ON DELETE RESTRICT
);
CREATE TABLE workflow_phase_exits (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 action_id TEXT NOT NULL,
 exit_seq INTEGER NOT NULL, actor TEXT NOT NULL DEFAULT '', step_id TEXT NOT NULL DEFAULT '', contract_version INTEGER NOT NULL DEFAULT 0, record_version INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(work_id,action_id)
);
CREATE TABLE workflow_plan_records (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL CHECK(work_version > 0),
    payload TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload)='object'),
    recorded_at TEXT NOT NULL,
    actor TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    event_ref TEXT NOT NULL
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
CREATE TABLE workflow_proposal_records (
    work_id        TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version   INTEGER NOT NULL,
    problem        TEXT NOT NULL,
    affected       TEXT NOT NULL CHECK(json_valid(affected) AND json_type(affected)='array'),
    stakes         TEXT NOT NULL,
    user_outcomes  TEXT NOT NULL CHECK(json_valid(user_outcomes) AND json_type(user_outcomes)='array'),
    constraints    TEXT NOT NULL CHECK(json_valid(constraints) AND json_type(constraints)='array'),
    open_questions TEXT NOT NULL CHECK(json_valid(open_questions) AND json_type(open_questions)='array'),
    recorded_at    TEXT NOT NULL, out_of_scope TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(out_of_scope) AND json_type(out_of_scope)='array'),
    PRIMARY KEY(work_id, work_version),
    CHECK(work_version > 0),
    CHECK(length(problem) BETWEEN 1 AND 4096),
    CHECK(json_array_length(affected) BETWEEN 1 AND 16),
    CHECK(length(stakes) BETWEEN 1 AND 2048),
    CHECK(json_array_length(user_outcomes) BETWEEN 1 AND 16),
    CHECK(json_array_length(constraints) BETWEEN 0 AND 16),
    CHECK(json_array_length(open_questions) BETWEEN 0 AND 16)
);
CREATE TABLE workflow_removal_scopes (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    work_version INTEGER NOT NULL CHECK(work_version > 0),
    payload TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload)='object'),
    recorded_at TEXT NOT NULL,
    actor TEXT NOT NULL REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    event_ref TEXT NOT NULL
);
CREATE TABLE workflow_step_epochs (
 work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
 step_id TEXT NOT NULL,
 attempt_epoch INTEGER NOT NULL, latest_start_seq INTEGER NOT NULL DEFAULT 0, latest_start_event_id TEXT NOT NULL DEFAULT '', latest_start_payload TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(latest_start_payload)),
 PRIMARY KEY(work_id,step_id)
);
CREATE TABLE "write_guard" (
    active INTEGER PRIMARY KEY,
    CHECK (active = 1)
);
INSERT INTO work_kinds VALUES('bug',1,1,1,1);
INSERT INTO work_kinds VALUES('decision',1,1,1,1);
INSERT INTO work_kinds VALUES('epic',0,0,0,0);
INSERT INTO work_kinds VALUES('initiative',0,0,0,0);
INSERT INTO work_kinds VALUES('other',1,1,1,1);
INSERT INTO work_kinds VALUES('research',1,1,1,1);
INSERT INTO work_kinds VALUES('task',1,1,1,1);
INSERT INTO workflow_native_run_statuses VALUES('start','started',0);
INSERT INTO workflow_native_run_statuses VALUES('start','failed_to_start',1);
INSERT INTO workflow_native_run_statuses VALUES('health','healthy',0);
INSERT INTO workflow_native_run_statuses VALUES('health','degraded',0);
INSERT INTO workflow_native_run_statuses VALUES('health','failed',1);
INSERT INTO workflow_native_run_statuses VALUES('rollback','rolled_back',1);
INSERT INTO workflow_native_run_statuses VALUES('rollback','partially_rolled_back',1);
INSERT INTO workflow_native_run_statuses VALUES('rollback','rollback_failed',1);
INSERT INTO workflow_native_run_statuses VALUES('cleanup','cleaned',0);
INSERT INTO workflow_native_run_statuses VALUES('cleanup','cleanup_failed',1);
INSERT INTO durability_commits VALUES(1,0);
INSERT INTO store_revision VALUES(1,61);
INSERT INTO store_sequences VALUES('effect',0);
INSERT INTO store_sequences VALUES('relation',0);
CREATE UNIQUE INDEX agent_client_one_active_key ON agent_client_keys(client_ref) WHERE status='active';
CREATE INDEX agent_nonce_replay_expiry ON agent_nonce_replay(expires_at);
CREATE INDEX archived_work_completed_order ON archived_work(completed_at DESC, id);
CREATE INDEX archived_work_domains_lookup ON archived_work_domains(domain_id, work_id);
CREATE INDEX archived_work_products_lookup ON archived_work_products(product_id, work_id);
CREATE INDEX archived_work_projects_lookup ON archived_work_projects(project_id, work_id);
CREATE INDEX archived_work_tags_lookup ON archived_work_tags(tag_id, work_id);
CREATE INDEX domain_observations_open ON domain_observations(product_id, domain_id, state, recorded_at);
CREATE INDEX effect_receipts_relation_seq ON effect_receipts(relation_seq) WHERE relation_seq IS NOT NULL;
CREATE INDEX effect_receipts_subject ON effect_receipts(subject_type,subject_id,effect_seq);
CREATE INDEX external_observations_work ON external_observations(work_id,created_event_seq);
CREATE INDEX idx_relations_from_kind ON relations(work_id_from,kind,work_id_to);
CREATE INDEX idx_relations_to_kind ON relations(work_id_to,kind,work_id_from);
CREATE INDEX linear_issue_links_human_key ON linear_issue_links(human_key);
CREATE INDEX linear_issue_links_remote_uuid ON linear_issue_links(remote_issue_uuid);
CREATE INDEX product_knowledge_homes_locator ON product_knowledge_homes(locator_id);
CREATE INDEX product_projects_by_project
    ON product_projects(project_id, product_id);
CREATE UNIQUE INDEX product_projects_one_primary
    ON product_projects(product_id) WHERE role='primary';
CREATE INDEX products_display_name_order
    ON products(display_name, id);
CREATE INDEX project_handoffs_recorded_seq ON project_handoffs (work_id, recorded_seq);
CREATE INDEX project_handoffs_source ON project_handoffs (work_id, source_project_id, source_session_ref);
CREATE INDEX project_handoffs_work_state ON project_handoffs (work_id, state);
CREATE INDEX project_locators_by_project ON project_locators(project_id, kind, normalized_value);
CREATE UNIQUE INDEX project_locators_project_locator
    ON project_locators(project_id, locator_id);
CREATE INDEX relations_resolution ON relations(resolution_id);
CREATE INDEX work_projects_by_project
    ON work_projects(project_id, work_id);
CREATE UNIQUE INDEX work_projects_one_primary
    ON work_projects(work_id) WHERE role='primary';
CREATE INDEX workflow_contract_affected_domains_lookup ON workflow_contract_affected_domains(domain_id, work_id, contract_version);
CREATE INDEX workflow_contract_law_additions_architecture ON workflow_contract_law_additions(work_id,contract_version,product_id);
CREATE INDEX workflow_contract_law_additions_reservation ON workflow_contract_law_additions(product_id,law_id,reservation_owner_work_id,reservation_owner_contract_version,home_domain_id);
CREATE INDEX workflow_contract_law_modifications_lookup
    ON workflow_contract_law_modifications(law_id, work_id, contract_version);
CREATE INDEX workflow_contracts_superseded_by ON workflow_contracts(work_id,superseded_by);
CREATE INDEX workflow_evidence_bindings_subject ON workflow_evidence_bindings(work_id,json_extract(payload,'$.immutable_subject_ref'));
CREATE INDEX workflow_impact_edges_target ON workflow_impact_edges(target_work_id, edge_kind, edge_class);
CREATE INDEX workflow_instances_state ON workflow_instances(instance_state,work_id);
CREATE INDEX workflow_law_addition_reservations_owner ON workflow_law_addition_reservations(product_id, owner_work_id, owner_contract_version);
CREATE INDEX workflow_law_addition_reservations_owner_contract ON workflow_law_addition_reservations(owner_work_id,owner_contract_version);
CREATE INDEX workflow_overlap_resolutions_from_contract ON workflow_overlap_resolutions(from_work_id,from_contract_version);
CREATE INDEX workflow_overlap_resolutions_from_to_invalidated ON workflow_overlap_resolutions(from_work_id,to_work_id,invalidated_seq);
CREATE INDEX workflow_overlap_resolutions_pair ON workflow_overlap_resolutions(product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,event_seq);
CREATE INDEX workflow_overlap_resolutions_reverse_pair ON workflow_overlap_resolutions(product_id,to_work_id,from_work_id,to_contract_version,from_contract_version,event_seq);
CREATE INDEX workflow_overlap_resolutions_to_contract ON workflow_overlap_resolutions(to_work_id,to_contract_version);
CREATE INDEX workflow_overlap_resolutions_to_from_invalidated ON workflow_overlap_resolutions(to_work_id,from_work_id,invalidated_seq);
CREATE TRIGGER archived_work_domains_guard_delete
BEFORE DELETE ON archived_work_domains FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_domains is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_domains_guard_insert
BEFORE INSERT ON archived_work_domains FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_domains is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_domains_guard_update
BEFORE UPDATE ON archived_work_domains FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_domains is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_guard_delete
BEFORE DELETE ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_guard_insert
BEFORE INSERT ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_guard_update
BEFORE UPDATE ON archived_work FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
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
CREATE TRIGGER archived_work_products_guard_delete
BEFORE DELETE ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_products_guard_insert
BEFORE INSERT ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_products_guard_update
BEFORE UPDATE ON archived_work_products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_projects_guard_delete
BEFORE DELETE ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_projects_guard_insert
BEFORE INSERT ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_projects_guard_update
BEFORE UPDATE ON archived_work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_tags_guard_delete
BEFORE DELETE ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_tags_guard_insert
BEFORE INSERT ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER archived_work_tags_guard_update
BEFORE UPDATE ON archived_work_tags FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'archived_work_tags is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER domain_observations_guard_delete BEFORE DELETE ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER domain_observations_guard_insert BEFORE INSERT ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER domain_observations_guard_update BEFORE UPDATE ON domain_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'domain_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER effect_receipts_guard_delete BEFORE DELETE ON effect_receipts
BEGIN SELECT RAISE(ABORT, 'effect_receipts is write-seam-only')
 WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER effect_receipts_no_update BEFORE UPDATE ON effect_receipts
BEGIN SELECT RAISE(ABORT, 'effect_receipts is immutable'); END;
CREATE TRIGGER external_observations_guard_delete BEFORE DELETE ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER external_observations_guard_insert BEFORE INSERT ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER external_observations_guard_update BEFORE UPDATE ON external_observations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'external_observations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER linear_issue_links_guard_delete BEFORE DELETE ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER linear_issue_links_guard_insert BEFORE INSERT ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER linear_issue_links_guard_update BEFORE UPDATE ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER product_knowledge_homes_guard_delete
BEFORE DELETE ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER product_knowledge_homes_guard_insert
BEFORE INSERT ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER product_knowledge_homes_guard_update
BEFORE UPDATE ON product_knowledge_homes FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_knowledge_homes is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
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
CREATE TRIGGER product_projects_guard_delete
BEFORE DELETE ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER product_projects_guard_insert
BEFORE INSERT ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER product_projects_guard_update
BEFORE UPDATE ON product_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'product_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
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
CREATE TRIGGER products_guard_delete
BEFORE DELETE ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER products_guard_insert
BEFORE INSERT ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER products_guard_update
BEFORE UPDATE ON products FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'products is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER project_handoffs_guard_delete BEFORE DELETE ON project_handoffs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'project_handoffs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER project_handoffs_guard_insert BEFORE INSERT ON project_handoffs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'project_handoffs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER project_handoffs_guard_update BEFORE UPDATE ON project_handoffs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'project_handoffs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER project_locators_guard_delete
BEFORE DELETE ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER project_locators_guard_insert
BEFORE INSERT ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER project_locators_guard_update
BEFORE UPDATE ON project_locators FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'project_locators is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER project_locators_referenced_by_knowledge_no_delete
BEFORE DELETE ON project_locators FOR EACH ROW
WHEN     EXISTS (SELECT 1 FROM archived_work k
            WHERE k.home_project_id = OLD.project_id AND k.home_locator_id = OLD.locator_id)
BEGIN
    SELECT RAISE(ABORT, 'Project locator is referenced by Git-derived knowledge');
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
CREATE TRIGGER projects_guard_delete
BEFORE DELETE ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER projects_guard_insert
BEFORE INSERT ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER projects_guard_update
BEFORE UPDATE ON projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
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
CREATE TRIGGER relations_guard_delete BEFORE DELETE ON relations BEGIN SELECT RAISE(ABORT,'relations is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER relations_guard_insert BEFORE INSERT ON relations BEGIN SELECT RAISE(ABORT,'relations is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER relations_guard_update BEFORE UPDATE ON relations BEGIN SELECT RAISE(ABORT,'relations is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_items_guard_delete BEFORE DELETE ON work_items BEGIN SELECT RAISE(ABORT,'work_items is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_items_guard_insert BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'work_items is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_items_guard_update BEFORE UPDATE ON work_items BEGIN SELECT RAISE(ABORT,'work_items is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_items_kind_registry_insert BEFORE INSERT ON work_items WHEN NOT EXISTS(SELECT 1 FROM work_kinds WHERE kind=NEW.kind AND stored=1) BEGIN SELECT RAISE(ABORT,'work_items kind is not a stored work kind'); END;
CREATE TRIGGER work_items_kind_registry_update BEFORE UPDATE OF kind ON work_items WHEN NOT EXISTS(SELECT 1 FROM work_kinds WHERE kind=NEW.kind AND stored=1) BEGIN SELECT RAISE(ABORT,'work_items kind is not a stored work kind'); END;
CREATE TRIGGER work_kinds_registry_no_delete
BEFORE DELETE ON work_kinds FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_kinds registry is immutable');
END;
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
CREATE TRIGGER work_projects_guard_delete
BEFORE DELETE ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER work_projects_guard_insert
BEFORE INSERT ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER work_projects_guard_update
BEFORE UPDATE ON work_projects FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'work_projects is write-seam-only')
    WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1);
END;
CREATE TRIGGER work_removal_operations_guard_delete BEFORE DELETE ON work_removal_operations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_removal_operations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_removal_operations_guard_insert BEFORE INSERT ON work_removal_operations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_removal_operations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_removal_operations_guard_update BEFORE UPDATE ON work_removal_operations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'work_removal_operations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_version_actions_guard_delete BEFORE DELETE ON work_version_actions BEGIN SELECT RAISE(ABORT, 'work_version_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_version_actions_guard_insert BEFORE INSERT ON work_version_actions BEGIN SELECT RAISE(ABORT, 'work_version_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER work_version_actions_guard_update BEFORE UPDATE ON work_version_actions BEGIN SELECT RAISE(ABORT, 'work_version_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_actors_guard_delete BEFORE DELETE ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is immutable') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_actors_guard_insert BEFORE INSERT ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_actors_guard_update BEFORE UPDATE ON workflow_actors FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_actors is immutable') ; END;
CREATE TRIGGER workflow_architecture_bindings_guard_delete BEFORE DELETE ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_architecture_bindings_guard_insert BEFORE INSERT ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_architecture_bindings_guard_update BEFORE UPDATE ON workflow_architecture_bindings FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_architecture_bindings is immutable'); END;
CREATE TRIGGER workflow_checkpoints_guard_delete BEFORE DELETE ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_checkpoints_guard_insert BEFORE INSERT ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_checkpoints_guard_update BEFORE UPDATE ON workflow_checkpoints FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_checkpoints is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_clarify_records_guard_delete BEFORE DELETE ON workflow_clarify_records BEGIN SELECT RAISE(ABORT, 'workflow_clarify_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_clarify_records_guard_insert BEFORE INSERT ON workflow_clarify_records BEGIN SELECT RAISE(ABORT, 'workflow_clarify_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_clarify_records_guard_update BEFORE UPDATE ON workflow_clarify_records BEGIN SELECT RAISE(ABORT, 'workflow_clarify_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_completed_exits_guard_delete BEFORE DELETE ON workflow_completed_exits BEGIN SELECT RAISE(ABORT, 'workflow_completed_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_completed_exits_guard_insert BEFORE INSERT ON workflow_completed_exits BEGIN SELECT RAISE(ABORT, 'workflow_completed_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_completed_exits_guard_update BEFORE UPDATE ON workflow_completed_exits BEGIN SELECT RAISE(ABORT, 'workflow_completed_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_delete BEFORE DELETE ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_insert BEFORE INSERT ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_affected_domains_guard_update BEFORE UPDATE ON workflow_contract_affected_domains FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_affected_domains is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_additions_guard_delete BEFORE DELETE ON workflow_contract_domain_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_additions_guard_insert BEFORE INSERT ON workflow_contract_domain_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_additions_guard_update BEFORE UPDATE ON workflow_contract_domain_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_delete BEFORE DELETE ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_insert BEFORE INSERT ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_modifications_guard_update BEFORE UPDATE ON workflow_contract_domain_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_delete BEFORE DELETE ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_insert BEFORE INSERT ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_domain_relation_modifications_guard_update BEFORE UPDATE ON workflow_contract_domain_relation_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_domain_relation_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_delete BEFORE DELETE ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_insert BEFORE INSERT ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_additions_guard_update BEFORE UPDATE ON workflow_contract_law_additions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_additions is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_modifications_guard_delete BEFORE DELETE ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_modifications_guard_insert BEFORE INSERT ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_law_modifications_guard_update BEFORE UPDATE ON workflow_contract_law_modifications FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_law_modifications is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_delete BEFORE DELETE ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_insert BEFORE INSERT ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contract_verification_obligations_guard_update BEFORE UPDATE ON workflow_contract_verification_obligations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contract_verification_obligations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_delete BEFORE DELETE ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_insert BEFORE INSERT ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_contracts_guard_update BEFORE UPDATE ON workflow_contracts FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_contracts is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_coordinator_actions_guard_delete BEFORE DELETE ON workflow_coordinator_actions BEGIN SELECT RAISE(ABORT, 'workflow_coordinator_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_coordinator_actions_guard_insert BEFORE INSERT ON workflow_coordinator_actions BEGIN SELECT RAISE(ABORT, 'workflow_coordinator_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_coordinator_actions_guard_update BEFORE UPDATE ON workflow_coordinator_actions BEGIN SELECT RAISE(ABORT, 'workflow_coordinator_actions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_delete BEFORE DELETE ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_insert BEFORE INSERT ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_decision_records_guard_update BEFORE UPDATE ON workflow_decision_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_decision_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_delivery_state_guard_delete BEFORE DELETE ON workflow_delivery_state BEGIN SELECT RAISE(ABORT, 'workflow_delivery_state is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_delivery_state_guard_insert BEFORE INSERT ON workflow_delivery_state BEGIN SELECT RAISE(ABORT, 'workflow_delivery_state is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_delivery_state_guard_update BEFORE UPDATE ON workflow_delivery_state BEGIN SELECT RAISE(ABORT, 'workflow_delivery_state is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_design_records_guard_delete BEFORE DELETE ON workflow_design_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_design_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_design_records_guard_insert BEFORE INSERT ON workflow_design_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_design_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_design_records_guard_update BEFORE UPDATE ON workflow_design_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_design_records is immutable'); END;
CREATE TRIGGER workflow_evidence_bindings_guard_delete BEFORE DELETE ON workflow_evidence_bindings BEGIN SELECT RAISE(ABORT, 'workflow_evidence_bindings is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_evidence_bindings_guard_insert BEFORE INSERT ON workflow_evidence_bindings BEGIN SELECT RAISE(ABORT, 'workflow_evidence_bindings is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_evidence_bindings_guard_update BEFORE UPDATE ON workflow_evidence_bindings BEGIN SELECT RAISE(ABORT, 'workflow_evidence_bindings is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_delete BEFORE DELETE ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_insert BEFORE INSERT ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_impact_edges_guard_update BEFORE UPDATE ON workflow_impact_edges FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_impact_edges is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_delete BEFORE DELETE ON workflow_instances BEGIN SELECT RAISE(ABORT,'workflow_instances is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_insert BEFORE INSERT ON workflow_instances BEGIN SELECT RAISE(ABORT,'workflow_instances is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_update BEFORE UPDATE ON workflow_instances BEGIN SELECT RAISE(ABORT,'workflow_instances is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_delete BEFORE DELETE ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_insert BEFORE INSERT ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_law_addition_reservations_guard_update BEFORE UPDATE ON workflow_law_addition_reservations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_law_addition_reservations is immutable'); END;
CREATE TRIGGER workflow_native_run_statuses_registry_no_delete
BEFORE DELETE ON workflow_native_run_statuses FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'workflow_native_run_statuses registry is immutable');
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
CREATE TRIGGER workflow_native_runs_guard_delete BEFORE DELETE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_insert BEFORE INSERT ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1); END;
CREATE TRIGGER workflow_native_runs_guard_update BEFORE UPDATE ON workflow_native_runs FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_native_runs is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active = 1); END;
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
CREATE TRIGGER workflow_overlap_resolutions_guard_delete BEFORE DELETE ON workflow_overlap_resolutions BEGIN SELECT RAISE(ABORT,'workflow_overlap_resolutions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_overlap_resolutions_guard_insert BEFORE INSERT ON workflow_overlap_resolutions BEGIN SELECT RAISE(ABORT,'workflow_overlap_resolutions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_overlap_resolutions_guard_update BEFORE UPDATE ON workflow_overlap_resolutions BEGIN SELECT RAISE(ABORT,'workflow_overlap_resolutions is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_phase_exits_guard_delete BEFORE DELETE ON workflow_phase_exits BEGIN SELECT RAISE(ABORT, 'workflow_phase_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_phase_exits_guard_insert BEFORE INSERT ON workflow_phase_exits BEGIN SELECT RAISE(ABORT, 'workflow_phase_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_phase_exits_guard_update BEFORE UPDATE ON workflow_phase_exits BEGIN SELECT RAISE(ABORT, 'workflow_phase_exits is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_plan_records_guard_delete BEFORE DELETE ON workflow_plan_records BEGIN SELECT RAISE(ABORT, 'workflow_plan_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_plan_records_guard_insert BEFORE INSERT ON workflow_plan_records BEGIN SELECT RAISE(ABORT, 'workflow_plan_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_plan_records_guard_update BEFORE UPDATE ON workflow_plan_records BEGIN SELECT RAISE(ABORT, 'workflow_plan_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_delete BEFORE DELETE ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_insert BEFORE INSERT ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_premise_confirmations_guard_update BEFORE UPDATE ON workflow_premise_confirmations FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_premise_confirmations is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_proposal_records_guard_delete BEFORE DELETE ON workflow_proposal_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_proposal_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_proposal_records_guard_insert BEFORE INSERT ON workflow_proposal_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_proposal_records is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_proposal_records_guard_update BEFORE UPDATE ON workflow_proposal_records FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_proposal_records is immutable'); END;
CREATE TRIGGER workflow_removal_scopes_guard_delete BEFORE DELETE ON workflow_removal_scopes BEGIN SELECT RAISE(ABORT, 'workflow_removal_scopes is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_removal_scopes_guard_insert BEFORE INSERT ON workflow_removal_scopes BEGIN SELECT RAISE(ABORT, 'workflow_removal_scopes is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_removal_scopes_guard_update BEFORE UPDATE ON workflow_removal_scopes BEGIN SELECT RAISE(ABORT, 'workflow_removal_scopes is write-seam-only') WHERE NOT EXISTS (SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_delete BEFORE DELETE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_insert BEFORE INSERT ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TRIGGER workflow_step_epochs_guard_update BEFORE UPDATE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT, 'workflow_step_epochs is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
`
