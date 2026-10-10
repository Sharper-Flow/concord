import { expect, test } from "bun:test"
import { validateGeneratedPayload } from "./generated-contract-tests"

test("native timestamps enforce calendar validity and explicit timezone", () => {
  for (const value of ["2026-02-30T00:00:00Z", "2026-09-14", "2026-09-14 00:00:00Z", "2026-09-14T00:00:00+24:00"]) {
    expect(validateGeneratedPayload("native_report_timestamp", value)).toBe(false)
  }
  for (const value of ["2024-02-29T00:00:00Z", "2026-09-14T12:00:00.123456789+02:30"]) {
    expect(validateGeneratedPayload("native_report_timestamp", value)).toBe(true)
  }
})

test("decision bounds count Unicode characters, not UTF-16 code units", () => {
  expect(validateGeneratedPayload("decision_record_text", "😀".repeat(128))).toBe(true)
  expect(validateGeneratedPayload("decision_record_text", "😀".repeat(129))).toBe(false)
  expect(validateGeneratedPayload("decision_record_text", "😀")).toBe(false)
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
