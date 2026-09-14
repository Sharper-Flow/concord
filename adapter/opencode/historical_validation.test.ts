import { afterAll, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { payloadSchemas } from "./generated-contracts"
import { validateGeneratedPayload } from "./generated-contract-tests"

afterAll(() => configureHostLease({ reset: true }))

function repro(name: string, message: string): never {
  console.error(JSON.stringify({ historical_repro: name, message }))
  throw new Error("REPRO: " + message)
}

test("historical Unicode character bounds", () => {
  if (!validateGeneratedPayload("proposal_text", "plain")) throw new Error("fixture schema unavailable")
  if (!validateGeneratedPayload("proposal_text", "😀".repeat(512))) repro("historical Unicode character bounds", "UTF-16 units replaced Unicode character bounds")
})

test("historical date-time calendar validation", () => {
  const value = { work_version: 1, approach: "shared validation", decisions: [{ id: "choice-one", question: "question", choice: "choice", rationale: "rationale", rejected: [] }], touched_refs: ["ref:one"], recorded_at: "2026-02-28T00:00:00Z" }
  if (!validateGeneratedPayload("workflow_design_record", value)) throw new Error("fixture schema unavailable")
  for (const invalid of ["2026-02-30T00:00:00Z", "2026-02-28"]) {
    value.recorded_at = invalid
    if (validateGeneratedPayload("workflow_design_record", value)) repro("historical date-time calendar validation", "invalid date-time was accepted")
  }
})

test("historical reference sibling validation", () => {
  Object.assign(payloadSchemas, { historical_ref_probe: { $ref: "#/$defs/proposal_text", maxLength: 2 } })
  try {
    if (!validateGeneratedPayload("historical_ref_probe", "ab")) throw new Error("fixture schema unavailable")
    if (validateGeneratedPayload("historical_ref_probe", "abc")) repro("historical reference sibling validation", "reference discarded its sibling constraint")
  } finally {
    Reflect.deleteProperty(payloadSchemas, "historical_ref_probe")
  }
})

// The real cancelled-task incident supplies the baseline execution evidence.
// This checks the missing host entry point; lane_completion tests verify its
// signed terminal writes and refusal behavior, not merely its presence.
test("historical host terminal-error entry point", async () => {
  const hooks = await ConcordAdapterPlugin()
  if (!("event" in hooks) || typeof hooks.event !== "function") repro("historical host terminal-error entry point", "no host terminal-error event handler")
})
