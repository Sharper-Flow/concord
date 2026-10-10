package store

// Frozen CON-902 WHISTORY baseline journals. Each constant is the verbatim
// capture output of a synthetic temporary-store journal driven through the
// baseline engine's own initialization, admission, and fold paths at commit
// d95dd9d0293f01317c5744b555dd3c59be0acbd4; the embedded provenance names the
// capture route. No live or installed store was read. The bytes are frozen
// provenance: workflow_historical_journal_replay_test.go restores them
// verbatim and requires the recorded pins, projection hash, terminal states,
// and as-of states to reproduce. Regenerating a journal is a new capture, not
// an edit of these bytes.

const historicalJournalFixtureCompletedStaticAnalysisV15 = `{
 "fixture_version": 1,
 "journal": [
  {
   "seq": 1,
   "event_id": "create-product",
   "kind": "product.created",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Concord",
    "stage_audience_commitment": "operator_only",
    "stage_maturity": "prototype"
   }
  },
  {
   "seq": 2,
   "event_id": "create-project",
   "kind": "project.created",
   "subject_type": "project",
   "subject_id": "project",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Core"
   }
  },
  {
   "seq": 3,
   "event_id": "product-project",
   "kind": "product_project.added",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "product_id": "product",
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary"
   }
  },
  {
   "seq": 4,
   "event_id": "create-con902-sa-complete",
   "kind": "work.created",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "priority": 10,
    "title": "con902-sa-complete",
    "work_kind": "task"
   }
  },
  {
   "seq": 5,
   "event_id": "membership-con902-sa-complete",
   "kind": "work_project.added",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 6,
   "event_id": "con902-actor-executor-con902-sa-complete",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
    "agent_ref": "agent/executor",
    "client_ref": "client/concord-1",
    "expected_version": 2,
    "principal_ref": "principal/operator",
    "resulting_version": 3,
    "session_ref": "session/con902-sa-complete",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 7,
   "event_id": "con902-actor-operator-con902-sa-complete",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "operator",
    "actor_ref": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
    "agent_ref": "agent/reviewer",
    "client_ref": "client/concord-1",
    "expected_version": 3,
    "principal_ref": "principal/operator",
    "resulting_version": 4,
    "session_ref": "session/con902-sa-complete",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 8,
   "event_id": "con902-definition-con902-sa-complete",
   "kind": "workflow.definition_selected",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "digest": "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade",
    "expected_version": 4,
    "ref": "workflow.static_analysis",
    "resulting_version": 5,
    "version": 15,
    "work_id": "con902-sa-complete",
    "work_kind": "static_analysis"
   }
  },
  {
   "seq": 9,
   "event_id": "con902-contract-con902-sa-complete",
   "kind": "workflow.contract_approved",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "consequence_class": "internal_sqlite",
    "contract_version": 1,
    "expected_version": 5,
    "law_modifies": [],
    "outcome_kind": "check",
    "outcome_payload": {
     "check_ref": "check:con902-sa-complete",
     "expected_result": "pass",
     "immutable_subject_ref": "commit:con902-sa-complete",
     "kind": "check"
    },
    "premise": "CON-902 WHISTORY synthetic baseline contract for con902-sa-complete",
    "required_evidence": [
     "verification"
    ],
    "resulting_version": 6,
    "rigor_class": "prototype_internal",
    "route_conventions": [],
    "spec_mandate": [],
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 10,
   "event_id": "con902-sa-complete:fixture-walk:scope",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "approve_contract",
    "actor_ref": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-sa-complete"
    ],
    "expected_version": 6,
    "result_evidence_refs": [],
    "resulting_version": 7,
    "step_id": "scope",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 11,
   "event_id": "con902-sa-complete:fixture-walk:analyze",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "record_delivery",
    "actor_ref": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-sa-complete"
    ],
    "expected_version": 7,
    "result_evidence_refs": [],
    "resulting_version": 8,
    "step_id": "analyze",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 12,
   "event_id": "con902-sa-complete:fixture-walk:report",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "record_report",
    "actor_ref": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-sa-complete"
    ],
    "expected_version": 8,
    "result_evidence_refs": [],
    "resulting_version": 9,
    "step_id": "report",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 13,
   "event_id": "con902-sa-complete:fixture-walk:review",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "confirm_premise",
    "actor_ref": "actor:33856991558c405d8dc01eb46926910b1d54d92002d8b29ea518d1c019a22a4b",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-sa-complete"
    ],
    "expected_version": 9,
    "result_evidence_refs": [],
    "resulting_version": 10,
    "step_id": "review",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 14,
   "event_id": "con902-sa-complete-verification-bound",
   "kind": "workflow.evidence_bound",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:test",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_kind": "verification",
    "expected_version": 10,
    "immutable_subject_ref": "evidence:con902-sa-complete-verify",
    "observed_at": "2026-08-07T12:30:00Z",
    "producer_id": "principal/verify",
    "producer_run_ref": "con902-sa-complete-verification",
    "producer_watermark": "request/verify",
    "resulting_version": 11,
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 15,
   "event_id": "con902-sa-complete-review-bound",
   "kind": "workflow.evidence_bound",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:test",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_kind": "review",
    "expected_version": 11,
    "immutable_subject_ref": "evidence:con902-sa-complete-review",
    "observed_at": "2026-08-07T12:30:00Z",
    "producer_id": "principal/review",
    "producer_run_ref": "con902-sa-complete-review",
    "producer_watermark": "request/review",
    "resulting_version": 12,
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 16,
   "event_id": "con902-sa-complete-artifact-bound",
   "kind": "workflow.evidence_bound",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:test",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_kind": "artifact",
    "expected_version": 12,
    "immutable_subject_ref": "artifact:con902-sa-complete",
    "observed_at": "2026-08-07T12:30:00Z",
    "producer_id": "principal/artifact",
    "producer_run_ref": "con902-sa-complete-artifact",
    "producer_watermark": "request/artifact",
    "resulting_version": 13,
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 17,
   "event_id": "con902-verdict-con902-sa-complete",
   "kind": "workflow.verdict_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "contract_version": 1,
    "evaluation_evidence": [
     "evidence:con902-sa-complete-verify"
    ],
    "expected_version": 13,
    "incomparable_with_approved": false,
    "predicate_id": "predicate:primary",
    "resulting_version": 14,
    "verdict_actor_ref": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
    "verdict_kind": "ok",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 18,
   "event_id": "con902-premise-con902-sa-complete",
   "kind": "workflow.premise_confirmed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "confirming_actor_ref": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
    "contract_version": 1,
    "expected_version": 14,
    "resulting_version": 15,
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 19,
   "event_id": "con902-completion-con902-sa-complete",
   "kind": "workflow.completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "changed_refs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "evidence_count": 3,
    "expected_version": 15,
    "final_verdict_kind": "ok",
    "impact_verdict": "non-breaking",
    "premise_confirmed": true,
    "resulting_version": 16,
    "terminal_state": "completed",
    "verdict_actor_ref": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
    "work_id": "con902-sa-complete"
   }
  },
  {
   "seq": 20,
   "event_id": "con902-completion-con902-sa-complete:lifecycle",
   "kind": "work.transitioned",
   "subject_type": "work_item",
   "subject_id": "con902-sa-complete",
   "actor": "actor:28d2b88bbcfcc89363b3b74ca1ec9e278b53b335a573d1c5120ec5e90262f047",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_refs": [],
    "expected_version": 16,
    "from": "in_progress",
    "reason": "workflow completed",
    "resulting_version": 17,
    "to": "completed"
   }
  }
 ],
 "provenance": {
  "as_of_samples": [
   {
    "kind": "reconstruct",
    "label": "contract-approved",
    "seq": 9,
    "state_at_sequence": "needed",
    "current_step": "scope",
    "instance_state": "planned"
   },
   {
    "kind": "reconstruct",
    "label": "delivery-walk-complete",
    "seq": 13,
    "state_at_sequence": "running",
    "current_step": "complete",
    "instance_state": "running"
   },
   {
    "kind": "prefix",
    "label": "final",
    "seq": 20,
    "state_at_sequence": "",
    "current_step": "complete",
    "instance_state": "completed"
   }
  ],
  "baseline_commit": "d95dd9d0293f01317c5744b555dd3c59be0acbd4",
  "capture": "Synthetic journal captured in a temporary store through the baseline engine's own initialization, admission, and fold paths at this commit. No live or installed store was read.",
  "definition_digest": "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade",
  "definition_ref": "workflow.static_analysis",
  "definition_version": 15,
  "replay_contract": "Restore journal events in seq order and receipt rows verbatim, rebuild from the log, and require the frozen projection hash, terminal state, and as-of states to reproduce.",
  "terminal_state": "completed",
  "workflow_projection_hash": "sha256:bff55a12230066aae292c5c158bb6d51803888ffa6bfc422f9e55778e3b846dd"
 },
 "receipts": [
  {
   "op_id": "con902-sa-complete-artifact",
   "attempt_epoch": 1,
   "work_id": "con902-sa-complete",
   "workflow_type_ref": "workflow.static_analysis",
   "workflow_type_version": 15,
   "step_id": "complete",
   "step_kind": "internal_sqlite",
   "accepted_inputs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "accepted_scope_snapshot": "{}",
   "result_kind": "completed",
   "result_payload": "",
   "evidence_refs": "[\"artifact:con902-sa-complete\"]",
   "changed_refs": "[]",
   "principal_ref": "principal/artifact",
   "request_id": "request/artifact",
   "observed_at": "2026-08-09T00:00:00Z",
   "completed_at": "2026-08-09T00:00:01Z",
   "contract_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  },
  {
   "op_id": "con902-sa-complete-review",
   "attempt_epoch": 1,
   "work_id": "con902-sa-complete",
   "workflow_type_ref": "workflow.static_analysis",
   "workflow_type_version": 15,
   "step_id": "complete",
   "step_kind": "internal_sqlite",
   "accepted_inputs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "accepted_scope_snapshot": "{}",
   "result_kind": "completed",
   "result_payload": "",
   "evidence_refs": "[\"evidence:con902-sa-complete-review\"]",
   "changed_refs": "[]",
   "principal_ref": "principal/review",
   "request_id": "request/review",
   "observed_at": "2026-08-09T00:00:00Z",
   "completed_at": "2026-08-09T00:00:01Z",
   "contract_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  },
  {
   "op_id": "con902-sa-complete-verification",
   "attempt_epoch": 1,
   "work_id": "con902-sa-complete",
   "workflow_type_ref": "workflow.static_analysis",
   "workflow_type_version": 15,
   "step_id": "complete",
   "step_kind": "internal_sqlite",
   "accepted_inputs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "accepted_scope_snapshot": "{}",
   "result_kind": "completed",
   "result_payload": "",
   "evidence_refs": "[\"evidence:con902-sa-complete-verify\"]",
   "changed_refs": "[]",
   "principal_ref": "principal/verify",
   "request_id": "request/verify",
   "observed_at": "2026-08-09T00:00:00Z",
   "completed_at": "2026-08-09T00:00:01Z",
   "contract_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  }
 ]
}`

const historicalJournalFixtureFailedStaticAnalysisV15 = `{
 "fixture_version": 1,
 "journal": [
  {
   "seq": 1,
   "event_id": "create-product",
   "kind": "product.created",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Concord",
    "stage_audience_commitment": "operator_only",
    "stage_maturity": "prototype"
   }
  },
  {
   "seq": 2,
   "event_id": "create-project",
   "kind": "project.created",
   "subject_type": "project",
   "subject_id": "project",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Core"
   }
  },
  {
   "seq": 3,
   "event_id": "product-project",
   "kind": "product_project.added",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "product_id": "product",
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary"
   }
  },
  {
   "seq": 4,
   "event_id": "create-con902-sa-failed",
   "kind": "work.created",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "priority": 10,
    "title": "con902-sa-failed",
    "work_kind": "task"
   }
  },
  {
   "seq": 5,
   "event_id": "membership-con902-sa-failed",
   "kind": "work_project.added",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 6,
   "event_id": "con902-actor-executor-con902-sa-failed",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
    "agent_ref": "agent/executor",
    "client_ref": "client/concord-1",
    "expected_version": 2,
    "principal_ref": "principal/operator",
    "resulting_version": 3,
    "session_ref": "session/con902-sa-failed",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 7,
   "event_id": "con902-actor-operator-con902-sa-failed",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9dae15ae265b764e6e0c82956aabd07d523f36ba14929ad390eb53198c923335",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "operator",
    "actor_ref": "actor:9dae15ae265b764e6e0c82956aabd07d523f36ba14929ad390eb53198c923335",
    "agent_ref": "agent/reviewer",
    "client_ref": "client/concord-1",
    "expected_version": 3,
    "principal_ref": "principal/operator",
    "resulting_version": 4,
    "session_ref": "session/con902-sa-failed",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 8,
   "event_id": "con902-actor-owner-con902-sa-failed",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
    "agent_ref": "agent/owner",
    "client_ref": "client/concord-1",
    "expected_version": 4,
    "principal_ref": "principal/operator",
    "resulting_version": 5,
    "session_ref": "session/con902-sa-failed",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 9,
   "event_id": "con902-definition-con902-sa-failed",
   "kind": "workflow.definition_selected",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "digest": "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade",
    "expected_version": 5,
    "ref": "workflow.static_analysis",
    "resulting_version": 6,
    "version": 15,
    "work_id": "con902-sa-failed",
    "work_kind": "static_analysis"
   }
  },
  {
   "seq": 10,
   "event_id": "con902-contract-con902-sa-failed",
   "kind": "workflow.contract_approved",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "consequence_class": "internal_sqlite",
    "contract_version": 1,
    "expected_version": 6,
    "law_modifies": [],
    "outcome_kind": "check",
    "outcome_payload": {
     "check_ref": "check:con902-sa-failed",
     "expected_result": "pass",
     "immutable_subject_ref": "commit:con902-sa-failed",
     "kind": "check"
    },
    "premise": "CON-902 WHISTORY synthetic baseline contract for con902-sa-failed",
    "required_evidence": [
     "verification"
    ],
    "resulting_version": 7,
    "rigor_class": "prototype_internal",
    "route_conventions": [],
    "spec_mandate": [],
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 11,
   "event_id": "con902-sa-failed:fixture-walk:scope",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "approve_contract",
    "actor_ref": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-sa-failed"
    ],
    "expected_version": 7,
    "result_evidence_refs": [],
    "resulting_version": 8,
    "step_id": "scope",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 12,
   "event_id": "con902-start-sa-failed:analyze:1",
   "kind": "workflow.action_started",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "accepted_inputs_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "action_id": "run_analysis",
    "actor_ref": "actor:9309c4708647670c122a55cbe07d0546a3afdd1421cf2fca8cabaa288cb32ed9",
    "attempt_epoch": 1,
    "expected_version": 8,
    "idempotency_identity": "con902:sa-failed:analyze:1",
    "resulting_version": 9,
    "step_id": "analyze",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 13,
   "event_id": "con902-dispatch-authorization-attempt:con902-sa-failed:1",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "dispatch_worker",
    "actor_ref": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
    "attempt_epoch": 1,
    "expected_version": 9,
    "resulting_version": 10,
    "step_id": "analyze",
    "work_id": "con902-sa-failed",
    "worker_attempt_id": "attempt:con902-sa-failed:1",
    "worker_capability_class": "research",
    "worker_lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "worker_lane_id": "research",
    "worker_lane_version": 1,
    "worker_packet_digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "worker_packet_predicate_ids": []
   }
  },
  {
   "seq": 14,
   "event_id": "con902-lane-actor-con902-sa-failed",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:2e716b842263278c448de188b2e07a8c0aff39933a84d1a50566d6c5261f2270",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:2e716b842263278c448de188b2e07a8c0aff39933a84d1a50566d6c5261f2270",
    "agent_ref": "agent/lane",
    "client_ref": "client/concord-1",
    "expected_version": 10,
    "principal_ref": "principal/operator",
    "resulting_version": 11,
    "session_ref": "session/con902-sa-failed:attempt1",
    "work_id": "con902-sa-failed"
   }
  },
  {
   "seq": 15,
   "event_id": "con902-worker-dispatched-attempt:con902-sa-failed:1",
   "kind": "worker.dispatched",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:10:00Z",
   "payload_version": 5,
   "payload": {
    "attempt_id": "attempt:con902-sa-failed:1",
    "lane_id": "research",
    "lane_version": 1,
    "lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "capability_class": "research",
    "packet_schema_version": "1.1",
    "report_schema_version": "1.1",
    "packet_digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "readback_model": "openai/gpt-5.6-luna",
    "lane_actor_ref": "actor:2e716b842263278c448de188b2e07a8c0aff39933a84d1a50566d6c5261f2270"
   }
  },
  {
   "seq": 16,
   "event_id": "con902-worker-failed-attempt:con902-sa-failed:1",
   "kind": "worker.failed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:11:00Z",
   "payload_version": 2,
   "payload": {
    "attempt_id": "attempt:con902-sa-failed:1",
    "readback_model": "openai/gpt-5.6-luna",
    "failure_kind": "worker_error",
    "detail": "CON-902 WHISTORY synthetic worker failure: the analysis attempt returned a bounded error"
   }
  },
  {
   "seq": 17,
   "event_id": "con902-failure-record-attempt:con902-sa-failed:1",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-sa-failed",
   "actor": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "record_worker_failure",
    "actor_ref": "actor:8dc45a9eee4ef17c487c4fd37132924cb88f6dba2d153c311580a6f4895b6329",
    "attempt_epoch": 1,
    "expected_version": 11,
    "resulting_version": 12,
    "step_id": "analyze",
    "work_id": "con902-sa-failed",
    "worker_attempt_id": "attempt:con902-sa-failed:1"
   }
  }
 ],
 "provenance": {
  "as_of_samples": [
   {
    "kind": "reconstruct",
    "label": "contract-approved",
    "seq": 10,
    "state_at_sequence": "needed",
    "current_step": "scope",
    "instance_state": "planned"
   },
   {
    "kind": "reconstruct",
    "label": "worker-failed",
    "seq": 16,
    "state_at_sequence": "running",
    "current_step": "analyze",
    "instance_state": "running"
   },
   {
    "kind": "reconstruct",
    "label": "failure-recorded",
    "seq": 17,
    "state_at_sequence": "running",
    "current_step": "analyze",
    "instance_state": "running"
   }
  ],
  "baseline_commit": "d95dd9d0293f01317c5744b555dd3c59be0acbd4",
  "capture": "Synthetic journal captured in a temporary store through the baseline engine's own initialization, admission, and fold paths at this commit. No live or installed store was read.",
  "definition_digest": "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade",
  "definition_ref": "workflow.static_analysis",
  "definition_version": 15,
  "replay_contract": "Restore journal events in seq order and receipt rows verbatim, rebuild from the log, and require the frozen projection hash, terminal state, and as-of states to reproduce.",
  "terminal_state": "running",
  "workflow_projection_hash": "sha256:4b7d9090d446743f65f03d45a4ac638986aa3e595a9d77b2436b1f7c251d484a"
 },
 "receipts": []
}`

const historicalJournalFixtureCorrectedGenericOneOffV16 = `{
 "fixture_version": 1,
 "journal": [
  {
   "seq": 1,
   "event_id": "create-product",
   "kind": "product.created",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Concord",
    "stage_audience_commitment": "operator_only",
    "stage_maturity": "prototype"
   }
  },
  {
   "seq": 2,
   "event_id": "create-project",
   "kind": "project.created",
   "subject_type": "project",
   "subject_id": "project",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "display_name": "Core"
   }
  },
  {
   "seq": 3,
   "event_id": "product-project",
   "kind": "product_project.added",
   "subject_type": "product",
   "subject_id": "product",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "product_id": "product",
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary"
   }
  },
  {
   "seq": 4,
   "event_id": "create-con902-go-corrected",
   "kind": "work.created",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "priority": 10,
    "title": "con902-go-corrected",
    "work_kind": "task"
   }
  },
  {
   "seq": 5,
   "event_id": "membership-con902-go-corrected",
   "kind": "work_project.added",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "operator",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "expected_version": 1,
    "project_id": "project",
    "reason": "test",
    "resulting_version": 2,
    "role": "primary",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 6,
   "event_id": "con902-actor-executor-con902-go-corrected",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
    "agent_ref": "agent/executor",
    "client_ref": "client/concord-1",
    "expected_version": 2,
    "principal_ref": "principal/operator",
    "resulting_version": 3,
    "session_ref": "session/con902-go-corrected",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 7,
   "event_id": "con902-actor-operator-con902-go-corrected",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "operator",
    "actor_ref": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
    "agent_ref": "agent/reviewer",
    "client_ref": "client/concord-1",
    "expected_version": 3,
    "principal_ref": "principal/operator",
    "resulting_version": 4,
    "session_ref": "session/con902-go-corrected",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 8,
   "event_id": "con902-actor-owner-con902-go-corrected",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
    "agent_ref": "agent/owner",
    "client_ref": "client/concord-1",
    "expected_version": 4,
    "principal_ref": "principal/operator",
    "resulting_version": 5,
    "session_ref": "session/con902-go-corrected",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 9,
   "event_id": "con902-definition-con902-go-corrected",
   "kind": "workflow.definition_selected",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "digest": "sha256:05e53c76e3f6c712cc62f7421c22a3df4719767aba91f7a07049ba973190d8d7",
    "expected_version": 5,
    "ref": "workflow.generic_one_off",
    "resulting_version": 6,
    "version": 16,
    "work_id": "con902-go-corrected",
    "work_kind": "generic_one_off"
   }
  },
  {
   "seq": 10,
   "event_id": "con902-contract-con902-go-corrected",
   "kind": "workflow.contract_approved",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "consequence_class": "internal_sqlite",
    "contract_version": 1,
    "expected_version": 6,
    "law_modifies": [],
    "outcome_predicates": [
     {
      "ordinal": 0,
      "outcome_kind": "check",
      "outcome_payload": {
       "check_ref": "check:con902-go-corrected",
       "expected_result": "pass",
       "immutable_subject_ref": "commit:con902-go-corrected",
       "kind": "check"
      },
      "predicate_id": "predicate:primary"
     }
    ],
    "premise": "CON-902 WHISTORY synthetic baseline contract for con902-go-corrected",
    "required_evidence": [
     "verification"
    ],
    "resulting_version": 7,
    "rigor_class": "prototype_internal",
    "route_conventions": [],
    "spec_mandate": [],
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 11,
   "event_id": "con902-go-corrected:fixture-walk:define",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "approve_contract",
    "actor_ref": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-go-corrected"
    ],
    "expected_version": 7,
    "result_evidence_refs": [],
    "resulting_version": 8,
    "step_id": "define",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 12,
   "event_id": "con902-start-go-corrected:execute:1",
   "kind": "workflow.action_started",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "accepted_inputs_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "action_id": "start_action",
    "actor_ref": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
    "attempt_epoch": 1,
    "expected_version": 8,
    "idempotency_identity": "con902:go-corrected:execute:1",
    "resulting_version": 9,
    "step_id": "execute",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 13,
   "event_id": "con902-dispatch-authorization-attempt:con902-go-corrected:1",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "dispatch_worker",
    "actor_ref": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
    "attempt_epoch": 1,
    "expected_version": 9,
    "resulting_version": 10,
    "step_id": "execute",
    "work_id": "con902-go-corrected",
    "worker_attempt_id": "attempt:con902-go-corrected:1",
    "worker_capability_class": "research",
    "worker_lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "worker_lane_id": "research",
    "worker_lane_version": 1,
    "worker_packet_digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "worker_packet_predicate_ids": []
   }
  },
  {
   "seq": 14,
   "event_id": "con902-lane-actor-failed-con902-go-corrected",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:5f21c4cbb03a5d05931dd4f1e652bac9b573e296f1771b146cd2bcd6a98ee803",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:5f21c4cbb03a5d05931dd4f1e652bac9b573e296f1771b146cd2bcd6a98ee803",
    "agent_ref": "agent/lane",
    "client_ref": "client/concord-1",
    "expected_version": 10,
    "principal_ref": "principal/operator",
    "resulting_version": 11,
    "session_ref": "session/con902-go-corrected:attempt1",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 15,
   "event_id": "con902-worker-dispatched-attempt:con902-go-corrected:1",
   "kind": "worker.dispatched",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:10:00Z",
   "payload_version": 5,
   "payload": {
    "attempt_id": "attempt:con902-go-corrected:1",
    "lane_id": "research",
    "lane_version": 1,
    "lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "capability_class": "research",
    "packet_schema_version": "1.1",
    "report_schema_version": "1.1",
    "packet_digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "readback_model": "openai/gpt-5.6-luna",
    "lane_actor_ref": "actor:5f21c4cbb03a5d05931dd4f1e652bac9b573e296f1771b146cd2bcd6a98ee803"
   }
  },
  {
   "seq": 16,
   "event_id": "con902-worker-failed-attempt:con902-go-corrected:1",
   "kind": "worker.failed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:11:00Z",
   "payload_version": 2,
   "payload": {
    "attempt_id": "attempt:con902-go-corrected:1",
    "readback_model": "openai/gpt-5.6-luna",
    "failure_kind": "worker_error",
    "detail": "CON-902 WHISTORY synthetic worker failure: the first execute attempt returned a bounded error"
   }
  },
  {
   "seq": 17,
   "event_id": "con902-failure-record-attempt:con902-go-corrected:1",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "record_worker_failure",
    "actor_ref": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
    "attempt_epoch": 1,
    "expected_version": 11,
    "resulting_version": 12,
    "step_id": "execute",
    "work_id": "con902-go-corrected",
    "worker_attempt_id": "attempt:con902-go-corrected:1"
   }
  },
  {
   "seq": 18,
   "event_id": "con902-start-go-corrected:execute:2",
   "kind": "workflow.action_started",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "accepted_inputs_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "action_id": "start_action",
    "actor_ref": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
    "attempt_epoch": 2,
    "expected_version": 12,
    "idempotency_identity": "con902:go-corrected:execute:2",
    "resulting_version": 13,
    "step_id": "execute",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 19,
   "event_id": "con902-dispatch-authorization-attempt:con902-go-corrected:2",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "dispatch_worker",
    "actor_ref": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
    "attempt_epoch": 2,
    "expected_version": 13,
    "resulting_version": 14,
    "step_id": "execute",
    "work_id": "con902-go-corrected",
    "worker_attempt_id": "attempt:con902-go-corrected:2",
    "worker_capability_class": "research",
    "worker_lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "worker_lane_id": "research",
    "worker_lane_version": 1,
    "worker_packet_digest": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    "worker_packet_predicate_ids": []
   }
  },
  {
   "seq": 20,
   "event_id": "con902-lane-actor-retry-con902-go-corrected",
   "kind": "workflow.actor_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0f019b724d7da4d6803a4cb4682c0c0d429c16dfcfd26b8874cd2d2bc9e078c",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "actor_class": "agent",
    "actor_ref": "actor:b0f019b724d7da4d6803a4cb4682c0c0d429c16dfcfd26b8874cd2d2bc9e078c",
    "agent_ref": "agent/lane",
    "client_ref": "client/concord-1",
    "expected_version": 14,
    "principal_ref": "principal/operator",
    "resulting_version": 15,
    "session_ref": "session/con902-go-corrected:attempt2",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 21,
   "event_id": "con902-worker-dispatched-attempt:con902-go-corrected:2",
   "kind": "worker.dispatched",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:10:00Z",
   "payload_version": 5,
   "payload": {
    "attempt_id": "attempt:con902-go-corrected:2",
    "lane_id": "research",
    "lane_version": 1,
    "lane_digest": "sha256:4091e91c9c095b96be54aba40d9704d97ab025238663409768fcf205c171102f",
    "capability_class": "research",
    "packet_schema_version": "1.1",
    "report_schema_version": "1.1",
    "packet_digest": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    "readback_model": "openai/gpt-5.6-luna",
    "lane_actor_ref": "actor:b0f019b724d7da4d6803a4cb4682c0c0d429c16dfcfd26b8874cd2d2bc9e078c"
   }
  },
  {
   "seq": 22,
   "event_id": "con902-worker-completed-attempt:con902-go-corrected:2",
   "kind": "worker.completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "worker:test",
   "occurred_at": "2026-08-07T12:12:00Z",
   "payload_version": 6,
   "payload": {
    "attempt_id": "attempt:con902-go-corrected:2",
    "readback_model": "openai/gpt-5.6-luna",
    "report_schema_version": "1.1",
    "evidence": [
     {
      "obligation": "source_citations",
      "detail": "discharged source_citations"
     },
     {
      "obligation": "bounded_findings",
      "detail": "discharged bounded_findings"
     },
     {
      "obligation": "uncertainties",
      "detail": "discharged uncertainties"
     }
    ],
    "evidence_origin": "reported"
   }
  },
  {
   "seq": 23,
   "event_id": "con902-accept-attempt:con902-go-corrected:2",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 4,
   "payload": {
    "action_id": "accept_worker_result",
    "actor_ref": "actor:b0bc380233757bc4fb1e58677aedaf6a8237aee1fcf7733643023a020aba5a08",
    "attempt_epoch": 2,
    "expected_version": 15,
    "resulting_version": 16,
    "step_id": "execute",
    "work_id": "con902-go-corrected",
    "worker_attempt_id": "attempt:con902-go-corrected:2"
   }
  },
  {
   "seq": 24,
   "event_id": "con902-go-corrected:fixture-walk:verify",
   "kind": "workflow.action_completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "action_id": "confirm_premise",
    "actor_ref": "actor:e7f50225d2ca649ac8a39bc79557d52c754dfa6d16d5e1b3cab45a0130faf6b0",
    "attempt_epoch": 1,
    "changed_refs": [
     "con902-go-corrected"
    ],
    "expected_version": 16,
    "result_evidence_refs": [],
    "resulting_version": 17,
    "step_id": "verify",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 25,
   "event_id": "con902-go-corrected-verification-bound",
   "kind": "workflow.evidence_bound",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:test",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_kind": "verification",
    "expected_version": 17,
    "immutable_subject_ref": "evidence:con902-go-corrected-verify",
    "observed_at": "2026-08-07T12:30:00Z",
    "producer_id": "principal/verify",
    "producer_run_ref": "con902-go-corrected-verification",
    "producer_watermark": "request/verify",
    "resulting_version": 18,
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 26,
   "event_id": "con902-go-corrected-artifact-bound",
   "kind": "workflow.evidence_bound",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:test",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_kind": "artifact",
    "expected_version": 18,
    "immutable_subject_ref": "artifact:con902-go-corrected",
    "observed_at": "2026-08-07T12:30:00Z",
    "producer_id": "principal/artifact",
    "producer_run_ref": "con902-go-corrected-artifact",
    "producer_watermark": "request/artifact",
    "resulting_version": 19,
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 27,
   "event_id": "con902-verdict-con902-go-corrected",
   "kind": "workflow.verdict_recorded",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "contract_version": 1,
    "evaluation_evidence": [
     "evidence:con902-go-corrected-verify"
    ],
    "expected_version": 19,
    "incomparable_with_approved": false,
    "predicate_id": "predicate:primary",
    "resulting_version": 20,
    "verdict_actor_ref": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
    "verdict_kind": "ok",
    "verdict_model": "openai/gpt-5.6-luna",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 28,
   "event_id": "con902-premise-con902-go-corrected",
   "kind": "workflow.premise_confirmed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "confirming_actor_ref": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
    "contract_version": 1,
    "expected_version": 20,
    "resulting_version": 21,
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 29,
   "event_id": "con902-completion-con902-go-corrected",
   "kind": "workflow.completed",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 2,
   "payload": {
    "changed_refs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "evidence_count": 2,
    "expected_version": 21,
    "final_verdict_kind": "ok",
    "impact_verdict": "non-breaking",
    "premise_confirmed": true,
    "resulting_version": 22,
    "terminal_state": "completed",
    "verdict_actor_ref": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
    "work_id": "con902-go-corrected"
   }
  },
  {
   "seq": 30,
   "event_id": "con902-completion-con902-go-corrected:lifecycle",
   "kind": "work.transitioned",
   "subject_type": "work_item",
   "subject_id": "con902-go-corrected",
   "actor": "actor:b5f84a75a48a731a7295f174efe8ac512732b4b8caf5add75ae80033f5863541",
   "occurred_at": "2026-08-07T12:00:00Z",
   "payload_version": 1,
   "payload": {
    "evidence_refs": [],
    "expected_version": 22,
    "from": "in_progress",
    "reason": "workflow completed",
    "resulting_version": 23,
    "to": "completed"
   }
  }
 ],
 "provenance": {
  "as_of_samples": [
   {
    "kind": "reconstruct",
    "label": "contract-approved",
    "seq": 10,
    "state_at_sequence": "needed",
    "current_step": "define",
    "instance_state": "planned"
   },
   {
    "kind": "reconstruct",
    "label": "failure-recorded",
    "seq": 17,
    "state_at_sequence": "running",
    "current_step": "execute",
    "instance_state": "running"
   },
   {
    "kind": "reconstruct",
    "label": "corrected-attempt-accepted",
    "seq": 23,
    "state_at_sequence": "running",
    "current_step": "verify",
    "instance_state": "running"
   },
   {
    "kind": "prefix",
    "label": "final",
    "seq": 30,
    "state_at_sequence": "",
    "current_step": "complete",
    "instance_state": "completed"
   }
  ],
  "baseline_commit": "d95dd9d0293f01317c5744b555dd3c59be0acbd4",
  "capture": "Synthetic journal captured in a temporary store through the baseline engine's own initialization, admission, and fold paths at this commit. No live or installed store was read.",
  "definition_digest": "sha256:05e53c76e3f6c712cc62f7421c22a3df4719767aba91f7a07049ba973190d8d7",
  "definition_ref": "workflow.generic_one_off",
  "definition_version": 16,
  "replay_contract": "Restore journal events in seq order and receipt rows verbatim, rebuild from the log, and require the frozen projection hash, terminal state, and as-of states to reproduce.",
  "terminal_state": "completed",
  "workflow_projection_hash": "sha256:d547b51bcf833a75b956f058702d3847e3035790cf55b8864fdfe9bca9658515"
 },
 "receipts": [
  {
   "op_id": "con902-go-corrected-artifact",
   "attempt_epoch": 1,
   "work_id": "con902-go-corrected",
   "workflow_type_ref": "workflow.generic_one_off",
   "workflow_type_version": 16,
   "step_id": "complete",
   "step_kind": "internal_sqlite",
   "accepted_inputs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "accepted_scope_snapshot": "{}",
   "result_kind": "completed",
   "result_payload": "",
   "evidence_refs": "[\"artifact:con902-go-corrected\"]",
   "changed_refs": "[]",
   "principal_ref": "principal/artifact",
   "request_id": "request/artifact",
   "observed_at": "2026-08-09T00:00:00Z",
   "completed_at": "2026-08-09T00:00:01Z",
   "contract_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  },
  {
   "op_id": "con902-go-corrected-verification",
   "attempt_epoch": 1,
   "work_id": "con902-go-corrected",
   "workflow_type_ref": "workflow.generic_one_off",
   "workflow_type_version": 16,
   "step_id": "complete",
   "step_kind": "internal_sqlite",
   "accepted_inputs_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "accepted_scope_snapshot": "{}",
   "result_kind": "completed",
   "result_payload": "",
   "evidence_refs": "[\"evidence:con902-go-corrected-verify\"]",
   "changed_refs": "[]",
   "principal_ref": "principal/verify",
   "request_id": "request/verify",
   "observed_at": "2026-08-09T00:00:00Z",
   "completed_at": "2026-08-09T00:00:01Z",
   "contract_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  }
 ]
}`
