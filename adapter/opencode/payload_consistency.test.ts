import { expect, test } from "bun:test"
import { payloadSchemas } from "./generated-contracts"
import { validateGeneratedPayload } from "./generated-contract-tests"
import { validateAgainstSchema } from "./core-runtime"

const timestampSchema = { type: "string", format: "date-time", minLength: 20, maxLength: 64 }
const textSchema = { type: "string", minLength: 2, maxLength: 128 }

test("native timestamps enforce calendar validity and explicit timezone", () => {
  // The host validator ignores the date-time format keyword, so the
  // calendar and timezone verdicts run through the generated validator.
  Object.assign(payloadSchemas, { timestamp_probe: timestampSchema })
  try {
    for (const value of ["2026-02-30T00:00:00Z", "2026-09-14", "2026-09-14 00:00:00Z", "2026-09-14T00:00:00+24:00"]) {
      expect(validateGeneratedPayload("timestamp_probe", value)).toBe(false)
    }
    for (const value of ["2024-02-29T00:00:00Z", "2026-09-14T12:00:00.123456789+02:30"]) {
      expect(validateGeneratedPayload("timestamp_probe", value)).toBe(true)
    }
  } finally {
    Reflect.deleteProperty(payloadSchemas, "timestamp_probe")
  }
  // The host validator enforces shape bounds: a bare
  // date is below the 20-code-point floor, and a full timestamp passes the
  // shape even though only the format keyword decides its calendar.
  expect(validateAgainstSchema(timestampSchema, "2026-09-14")).toBe(false)
  expect(validateAgainstSchema(timestampSchema, "2024-02-29T00:00:00Z")).toBe(true)
})

test("string bounds count Unicode characters, not UTF-16 code units", () => {
  expect(validateAgainstSchema(textSchema, "😀".repeat(128))).toBe(true)
  expect(validateAgainstSchema(textSchema, "😀".repeat(129))).toBe(false)
  expect(validateAgainstSchema(textSchema, "😀")).toBe(false)
})

test("retained decision payload is closed and preserves nullable links", () => {
  const record = {
    work_id: "work-1", question: "Which option?", options_considered: ["one", "two"],
    decision: "insufficient_evidence", rationale: "Need evidence", consequences: ["delay"],
    inputs: ["input"], poc_findings: "No finding", supersedes: "earlier", superseded_by: null,
    recorded_at: "2026-08-20T12:00:00Z",
  }
  expect(validateGeneratedPayload("retained_decision_record", record)).toBe(true)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, decision: "accepted_decision", supersedes: null, superseded_by: "later" })).toBe(true)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, inputs: ["x".repeat(200)] })).toBe(true)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, decision: "unknown" })).toBe(false)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, extra: true })).toBe(false)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, inputs: [] })).toBe(false)
  expect(validateGeneratedPayload("retained_decision_record", { ...record, options_considered: Array(17).fill("one") })).toBe(false)
  for (const key of Object.keys(record)) {
    const incomplete: Record<string, unknown> = { ...record }
    delete incomplete[key]
    expect(validateGeneratedPayload("retained_decision_record", incomplete), key).toBe(false)
  }
})

test("the retired payload schemas carry no public alias", () => {
  for (const name of ["decision_record_text", "native_report_timestamp"]) {
    expect(Object.hasOwn(payloadSchemas as Record<string, unknown>, name), `${name} must stay out of the live generated corpus`).toBe(false)
    // No alias: the live validator cannot resolve the retired name, so it
    // refuses every payload.
    expect(validateGeneratedPayload(name, "2024-02-29T00:00:00Z")).toBe(false)
    expect(validateGeneratedPayload(name, "😀".repeat(128))).toBe(false)
  }
})
