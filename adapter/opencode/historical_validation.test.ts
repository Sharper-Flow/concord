// Imports must exist in the baseline pinned by scripts/test-workflow-history.py.
import { test } from "bun:test"
import { payloadSchemas } from "./generated-contracts"
import { validateGeneratedPayload } from "./generated-contract-tests"

function repro(name: string, message: string): never {
  console.error(JSON.stringify({ historical_repro: name, message }))
  throw new Error("REPRO: " + message)
}

test("historical Unicode character bounds", () => {
  Object.assign(payloadSchemas, { historical_length_probe: { type: "string", minLength: 1, maxLength: 4096 } })
  try {
    if (!validateGeneratedPayload("historical_length_probe", "plain")) throw new Error("fixture schema unavailable")
    if (!validateGeneratedPayload("historical_length_probe", "😀".repeat(4096))) repro("historical Unicode character bounds", "UTF-16 units replaced Unicode character bounds")
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_length_probe")
  }
})

test("historical date-time calendar validation", () => {
  Object.assign(payloadSchemas, { historical_date_time_probe: { type: "string", format: "date-time" } })
  try {
    if (!validateGeneratedPayload("historical_date_time_probe", "2026-02-28T00:00:00Z")) throw new Error("fixture schema unavailable")
    for (const invalid of ["2026-02-30T00:00:00Z", "2026-02-28"]) {
      if (validateGeneratedPayload("historical_date_time_probe", invalid)) repro("historical date-time calendar validation", "invalid date-time was accepted")
    }
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_date_time_probe")
  }
})

test("historical reference sibling validation", () => {
  Object.assign(payloadSchemas, { historical_ref_probe: { $ref: "#/$defs/reference", maxLength: 2 } })
  try {
    if (!validateGeneratedPayload("historical_ref_probe", "ab")) throw new Error("fixture schema unavailable")
    if (validateGeneratedPayload("historical_ref_probe", "abc")) repro("historical reference sibling validation", "reference discarded its sibling constraint")
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_ref_probe")
  }
})
