import { expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import { payloadSchemas } from "./generated-contracts"
import { validateGeneratedPayload } from "./generated-contract-tests"
import { validateAgainstSchema } from "./dispatch"

// decision_record_text and native_report_timestamp retired from the live
// generated corpus: the live validator refuses their names for any payload.
// The frozen historical decoder contract owns their semantics, so the
// bounds cases below validate the frozen documents, not the retired names.
const historicalDecoder = JSON.parse(
  readFileSync(new URL("../../contracts/workflow-historical-decoder.schema.json", import.meta.url), "utf8"),
) as { $defs: Record<string, unknown> }
const historicalDecisionText = historicalDecoder.$defs.decision_record_text
const historicalNativeTimestamp = historicalDecoder.$defs.native_report_timestamp

test("native timestamps enforce calendar validity and explicit timezone", () => {
  // The host validator ignores the date-time format keyword, so the
  // calendar and timezone verdicts run through the generated validator
  // against the frozen historical document, registered under a probe name
  // the live corpus does not carry.
  Object.assign(payloadSchemas, { historical_native_report_timestamp: historicalNativeTimestamp })
  try {
    for (const value of ["2026-02-30T00:00:00Z", "2026-09-14", "2026-09-14 00:00:00Z", "2026-09-14T00:00:00+24:00"]) {
      expect(validateGeneratedPayload("historical_native_report_timestamp", value)).toBe(false)
    }
    for (const value of ["2024-02-29T00:00:00Z", "2026-09-14T12:00:00.123456789+02:30"]) {
      expect(validateGeneratedPayload("historical_native_report_timestamp", value)).toBe(true)
    }
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_native_report_timestamp")
  }
  // The host validator enforces the frozen document's shape bounds: a bare
  // date is below the 20-code-point floor, and a full timestamp passes the
  // shape even though only the format keyword decides its calendar.
  expect(validateAgainstSchema(historicalNativeTimestamp, "2026-09-14")).toBe(false)
  expect(validateAgainstSchema(historicalNativeTimestamp, "2024-02-29T00:00:00Z")).toBe(true)
})

test("decision bounds count Unicode characters, not UTF-16 code units", () => {
  expect(validateAgainstSchema(historicalDecisionText, "😀".repeat(128))).toBe(true)
  expect(validateAgainstSchema(historicalDecisionText, "😀".repeat(129))).toBe(false)
  expect(validateAgainstSchema(historicalDecisionText, "😀")).toBe(false)
})

test("the retired payload schemas carry no public alias", () => {
  for (const name of ["decision_record_text", "native_report_timestamp"]) {
    expect(Object.hasOwn(payloadSchemas as Record<string, unknown>, name), `${name} must stay out of the live generated corpus`).toBe(false)
    // No alias: the live validator cannot resolve the retired name, so it
    // refuses every payload, including values the frozen historical
    // document admits.
    expect(validateGeneratedPayload(name, "2024-02-29T00:00:00Z")).toBe(false)
    expect(validateGeneratedPayload(name, "😀".repeat(128))).toBe(false)
  }
})

test("research results preserve bare packs and require explicit versioned wrappers", () => {
  const descriptor = {
    pack_id: "pack-1", owner_work_id: "work-1", current_revision: 1,
    freshness: "current", expected_version: 1,
    created_at: "2026-10-10T00:00:00Z", updated_at: "2026-10-10T00:00:00Z",
  }
  for (const result of [descriptor, { result_version: 2, pack: descriptor },
    { result_version: 2, packs: [] }, { result_version: 2, packs: [descriptor] }]) {
    expect(validateGeneratedPayload("research_read_result", result)).toBe(true)
  }
  for (const result of [{}, { result_version: 2 }, { result_version: 1, pack: descriptor },
    { result_version: 2, pack: descriptor, packs: [] },
    { result_version: 2, packs: [{ ...descriptor, revisions: [] }] }]) {
    expect(validateGeneratedPayload("research_read_result", result)).toBe(false)
  }
})

test("research selection schema refuses duplicate and unbounded findings", () => {
  const input = { product_id: "product-1", pack_id: "pack-1", result_version: 2, revision: 1 }
  expect(validateGeneratedPayload("work_trace_research_input", { ...input, finding_ids: ["f-1"] })).toBe(true)
  for (const finding_ids of [[], ["f-1", "f-1"], Array.from({ length: 33 }, (_, i) => `f-${i}`)]) {
    expect(validateGeneratedPayload("work_trace_research_input", { ...input, finding_ids })).toBe(false)
  }
})
