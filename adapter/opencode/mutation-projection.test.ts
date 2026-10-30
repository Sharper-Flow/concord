import { expect, test } from "bun:test"
import { contractOperations, manifestDigest, maxEnvelopeBytes } from "./generated-contracts"
import { validateGeneratedEnvelope, validateGeneratedPayload } from "./generated-contract-tests"
import { mutationResult, reconstructMutationResult } from "./mutation-result"
import { largeMutationPayload, mutationProjectionFixture } from "./mutation-projection.fixture"

test("projected mutation authority stays private and retains complete pins above the envelope cap", async () => {
  const fixture = mutationProjectionFixture()
  expect(Buffer.byteLength(fixture.document)).toBeGreaterThan(maxEnvelopeBytes)
  expect(validateGeneratedPayload("mutation_result", fixture.payload)).toBe(true)
  const wire = JSON.stringify(fixture.envelope)
  expect(mutationResult(fixture.envelope)).toBeUndefined()
  let reads = 0
  await reconstructMutationResult(fixture.envelope, fixture.source, async input => {
    expect(input).toEqual({ ...fixture.pointer, page: { cursor: reads === 0 ? null : String(reads), limit: 1 } })
    return fixture.pages[reads++]
  })
  expect(reads).toBe(fixture.pages.length)
  expect(mutationResult({ ...fixture.envelope })).toEqual(fixture.payload)
  expect(JSON.stringify(fixture.envelope)).toBe(wire)
  expect(Buffer.byteLength(wire)).toBeLessThan(maxEnvelopeBytes)
})

for (const [name, tamper] of [
  ["tampered bytes", (page: any) => { page.result.data = page.result.data.replace("work", "fake") }],
  ["digest", (page: any) => { page.result.digest = "sha256:" + "0".repeat(64) }],
  ["offset", (page: any) => { page.result.offset++ }],
  ["total", (page: any) => { page.result.total_bytes++ }],
  ["fragment byte count", (page: any) => { page.result.fragment_bytes++ }],
  ["early completion", (page: any) => { page.result.document_complete = true }],
  ["unpaired surrogate", (page: any) => { page.result.data = "\ud800" }],
  ["unknown page field", (page: any) => { page.result.unexpected = true }],
  ["missing cursor", (page: any) => { page.next_cursor = null }],
  ["oversized envelope", (page: any) => { page.result.data = "x".repeat(maxEnvelopeBytes) }],
] as const) {
  test(`mutation reconstruction refuses ${name} without granting authority`, async () => {
    const fixture = mutationProjectionFixture()
    tamper(fixture.pages[0])
    let reads = 0
    await expect(reconstructMutationResult(fixture.envelope, fixture.source, async () => fixture.pages[reads++])).rejects.toThrow("mutation_result projection")
    expect(mutationResult(fixture.envelope)).toBeUndefined()
  })
}

test("mutation reconstruction refuses drift on a later page and broken reads", async () => {
  for (const mode of ["digest", "offset", "total_bytes", "throws", "error", "cursor"] as const) {
    const fixture = mutationProjectionFixture()
    let reads = 0
    await expect(reconstructMutationResult(fixture.envelope, fixture.source, async () => {
      const page = fixture.pages[reads++]
      if (reads === 2) {
        if (mode === "throws") throw new Error("read failed")
        if (mode === "error") return { outcome: "error" }
        if (mode === "cursor") page.next_cursor = "1"
        else page.result[mode] = mode === "digest" ? "sha256:" + "0".repeat(64) : page.result[mode] + 1
      }
      return page
    })).rejects.toThrow()
    expect(mutationResult(fixture.envelope)).toBeUndefined()
  }
})

test("verified bytes still require the original inline schema and changed-version pins", async () => {
  for (const mode of ["schema", "changed_refs", "pin", "reference", "pointer", "intents"] as const) {
    const payload: Record<string, any> = largeMutationPayload()
    if (mode === "schema") payload.unexpected = true
    if (mode === "pin") payload.work_pins[0].version++
    const fixture = mutationProjectionFixture(payload)
    if (mode === "changed_refs") fixture.envelope.changed_refs = []
    if (mode === "reference") fixture.envelope.result.projection_reference.work_refs[0].version++
    if (mode === "pointer") fixture.envelope.result.projection_reference.read_pointer.input.idempotency_key = "other-key"
    if (mode === "intents") (fixture.envelope.next_valid_intents as unknown[]).push({ tool: "concord_work_transition", operation: "workflow_action", reason_code: "unverified" })
    let reads = 0
    await expect(reconstructMutationResult(fixture.envelope, fixture.source, async () => fixture.pages[reads++])).rejects.toThrow()
    expect(mutationResult(fixture.envelope)).toBeUndefined()
  }
})

test("projection contracts restrict source pairs and retain closed inline result validation", () => {
  const fixture = mutationProjectionFixture()
  const operations = contractOperations.filter(op => op.result_schema === "#/schemas/projectable_mutation_result")
  expect(operations.map(op => op.id).sort()).toEqual(["concord_work_relate.message_send", "concord_work_transition.workflow_action"])
  expect(validateGeneratedPayload("projectable_mutation_result", fixture.envelope.result)).toBe(true)
  expect(validateGeneratedPayload("projectable_mutation_result", fixture.payload)).toBe(true)
  expect(validateGeneratedPayload("projectable_mutation_result", { ...fixture.payload, unexpected: true })).toBe(false)
  expect(validateGeneratedPayload("mutation_result", fixture.envelope.result)).toBe(false)
  expect(validateGeneratedPayload("work_trace_mutation_result_input", fixture.pointer)).toBe(true)
  expect(validateGeneratedPayload("work_trace_mutation_result_input", { ...fixture.pointer, source_tool: "concord_work_transition" })).toBe(false)
  expect(validateGeneratedPayload("work_trace_mutation_result_input", { ...fixture.pointer, source_tool: "concord_work_transition", source_operation: "workflow_action" })).toBe(true)
  for (const idempotency_key of ["", "x".repeat(129)]) expect(validateGeneratedPayload("work_trace_mutation_result_input", { ...fixture.pointer, idempotency_key })).toBe(false)
  expect(validateGeneratedPayload("work_trace_mutation_result_input", { ...fixture.pointer, idempotency_key: "key with spaces" })).toBe(true)
  for (const digest of ["deadbeef", "sha256:deadbeef", "sha256:" + "A".repeat(64)]) expect(validateGeneratedPayload("work_trace_mutation_result_input", { ...fixture.pointer, digest })).toBe(false)
  const read = contractOperations.find(op => op.id === "concord_work_trace.mutation_result")!
  expect([read.kind, read.capability, read.consequence]).toEqual(["read", "product_read", "read"])
  expect(validateGeneratedPayload("work_browse_list_result", { items: [] })).toBe(true)
  for (const next_kind of ["projection", "items", null]) expect(validateGeneratedPayload("work_browse_list_result", { ...fixture.pages[0].result, next_kind })).toBe(true)
  expect(validateGeneratedPayload("work_browse_list_result", { ...fixture.pages[0].result, next_kind: "boundaries" })).toBe(false)
})

test("a projected mutation envelope preserves effects but grants no inline intents", () => {
  const fixture = mutationProjectionFixture()
  const envelope = {
    schema_version: "1.0", manifest_digest: manifestDigest, request_id: "projection-request", origin: "core",
    resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [],
    next_cursor: null, omissions: [], evidence_refs: [], replayed: false, ...fixture.envelope,
  }
  expect(validateGeneratedEnvelope(envelope)).toBe(true)
  expect(validateGeneratedEnvelope({ ...envelope, warnings: [] })).toBe(false)
  expect(validateGeneratedEnvelope({ ...envelope, next_valid_intents: [{ tool: "concord_work_transition", operation: "workflow_action", reason_code: "unverified" }] })).toBe(false)
})
