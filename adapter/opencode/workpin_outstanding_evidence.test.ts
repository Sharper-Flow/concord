import { expect, test } from "bun:test"
import { validateGeneratedPayload } from "./generated-contract-tests"

test("work pin validators enforce the intent count bound", () => {
  const pin: Record<string, unknown> = {
    work_id: "work-test", title: "Work fixture", linear_issue_key: "",
    project_id: "project-test", project_display_name: "Fixture", version: 3,
    lifecycle: "in_progress", cancelled_instance_closes: 0,
    workflow_type: "change", step: "review",
    driving_sessions: [], watermark: "seq:3", next_valid_intents: [],
  }
  expect(validateGeneratedPayload("work_pin", pin)).toBe(true)
  const intent = { tool: "concord_work_trace", operation: "history", reason_code: "audit" }
  pin.next_valid_intents = Array.from({ length: 83 }, () => intent)
  expect(validateGeneratedPayload("work_pin", pin)).toBe(true)
  pin.next_valid_intents = Array.from({ length: 84 }, () => intent)
  expect(validateGeneratedPayload("work_pin", pin)).toBe(false)
})
