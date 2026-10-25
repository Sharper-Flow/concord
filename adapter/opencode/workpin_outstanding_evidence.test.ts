import { expect, test } from "bun:test"
import { validateGeneratedPayload } from "./generated-contract-tests"

test("work pin validators accept bounded kind debt and legacy absence", () => {
  const kinds = ["approval", "artifact", "commit", "durable_note", "native_run", "review", "verification"]
  const pin: Record<string, unknown> = {
    work_id: "work-debt", title: "Evidence debt", linear_issue_key: "",
    project_id: "project-test", project_display_name: "Fixture", version: 3,
    lifecycle: "in_progress", cancelled_instance_closes: 0,
    workflow_type: "workflow.implementation", step: "acceptance", attempt: null,
    pending_operator_decision: null, driving_sessions: [], watermark: "seq:3", next_valid_intents: [],
    workflow_definition_digest: "sha256:" + "a".repeat(64), obligations: [],
    outstanding_evidence_kinds: kinds,
  }
  expect(validateGeneratedPayload("work_pin", pin)).toBe(true)
  pin.outstanding_evidence_kinds = []
  expect(validateGeneratedPayload("work_pin", pin)).toBe(true)
  delete pin.outstanding_evidence_kinds
  expect(validateGeneratedPayload("work_pin", pin)).toBe(true)
  for (const invalid of [null, ["unknown"], ["commit", "commit"]]) {
    pin.outstanding_evidence_kinds = invalid
    expect(validateGeneratedPayload("work_pin", pin)).toBe(false)
  }
  for (const state of ["active", "completed"]) {
    const held: Record<string, unknown> = { ...pin, outside_repair_disposition: {
      work_id: "work-debt", reason: "repair", approval_ref: "approval-fixture", state,
      recorded_at: "2026-10-17T00:00:00Z",
    } }
    delete held.workflow_definition_digest
    delete held.obligations
    delete held.outstanding_evidence_kinds
    expect(validateGeneratedPayload("work_pin", held)).toBe(true)
    held.outstanding_evidence_kinds = []
    expect(validateGeneratedPayload("work_pin", held)).toBe(false)
  }
})
