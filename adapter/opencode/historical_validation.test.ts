// Imports must exist in the baseline pinned by scripts/test-workflow-history.py.
// The Unicode-bounds and date-time calendar repros are covered on the same
// generated validator by adapter-error-tuples.test.ts (CON-913 emoji
// truncation against error.message maxLength) and payload_consistency.test.ts
// (native timestamp calendar and timezone refusal); this file keeps the one
// repro with no covering check, the $ref sibling merge.
import { test } from "bun:test"
import { payloadSchemas } from "./generated-contracts"
import { validateGeneratedPayload } from "./generated-contract-tests"

function repro(name: string, message: string): never {
  console.error(JSON.stringify({ historical_repro: name, message }))
  throw new Error("REPRO: " + message)
}

test("historical reference sibling validation", () => {
  Object.assign(payloadSchemas, { historical_ref_probe: { $ref: "#/$defs/proposal_text", maxLength: 2 } })
  try {
    if (!validateGeneratedPayload("historical_ref_probe", "ab")) throw new Error("fixture schema unavailable")
    if (validateGeneratedPayload("historical_ref_probe", "abc")) repro("historical reference sibling validation", "reference discarded its sibling constraint")
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_ref_probe")
  }
})
