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
