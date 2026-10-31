import { describe, expect, test } from "bun:test"

import { validateGeneratedPayload } from "./generated-contract-tests"

type unavailableCase = { name: string; view?: unknown; marker?: unknown; valid: boolean }

const unavailableCases = (): unavailableCase[] => {
  const view = () => ({ source_event_frontier: 0, required_reading: [], findings: [], domain_groups: [] })
  const marker = () => ({ reason: "limit_exceeded" })
  return [
    { name: "absent context stays valid", valid: true },
    { name: "available view stays valid", view: view(), valid: true },
    { name: "closed unavailable marker is accepted", marker: marker(), valid: true },
    { name: "wrong reason is refused", marker: { reason: "other" }, valid: false },
    { name: "unknown marker member is refused", marker: { ...marker(), detail: "x" }, valid: false },
    { name: "null marker is refused", marker: null, valid: false },
    { name: "marker excludes the available view", view: view(), marker: marker(), valid: false },
    { name: "marker excludes a null view", view: null, marker: marker(), valid: false },
    { name: "marker inside the view is refused", view: { work_context_unavailable: marker() }, valid: false },
  ]
}

const withMembers = (base: Record<string, unknown>, view: unknown, marker: unknown): Record<string, unknown> => {
  const payload = { ...base }
  if (view !== undefined) payload.work_context = view
  if (marker !== undefined) payload.work_context_unavailable = marker
  return payload
}

const basePin = (): Record<string, unknown> => ({
  work_id: "work-test",
  title: "fixture",
  linear_issue_key: "CON-0000",
  project_id: "project-test",
  project_display_name: "fixture",
  version: 1,
  lifecycle: "in_progress",
  cancelled_instance_closes: 0,
  workflow_type: "workflow.break_fix",
  step: "planning",
  driving_sessions: [],
  watermark: "seq:1",
  next_valid_intents: [],
  workflow_definition_digest: "sha256:" + "a".repeat(64),
  obligations: [],
})

const continuityWith = (view: unknown, marker: unknown): Record<string, unknown> => ({
  work_id: "work-test",
  pinned: withMembers(
    {
      product_identity: ["product"],
      workflow_step: "planning",
      step_actions: [],
      contract: null,
      spec_mandate: [],
      latest_checkpoint: null,
      design_record: null,
    },
    view,
    marker,
  ),
  boundaries: { count: 0, items: [], next_cursor: null, watermark: "seq:1" },
  typed_availability: { restart: "unavailable", reason: "unavailable" },
  pending_messages: 0,
  observations: [],
  observations_total: 0,
  observations_read: { tool: "concord_work_trace", operation: "observations", input: { work_id: "work-test" } },
})

describe("work context unavailable schema", () => {
  const locations: [string, (view: unknown, marker: unknown) => Record<string, unknown>][] = [
    ["continuity_snapshot", continuityWith],
    ["work_pin", (view, marker) => withMembers(basePin(), view, marker)],
  ]
  for (const [schema, build] of locations) {
    for (const testCase of unavailableCases()) {
      test(`${schema} ${testCase.name}`, () => {
        expect(validateGeneratedPayload(schema, build(testCase.view, testCase.marker))).toBe(testCase.valid)
      })
    }
  }
})
