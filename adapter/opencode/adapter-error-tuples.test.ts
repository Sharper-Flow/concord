// Table tests: the closed adapter error tuple table and its
// factory and exit validator. No AST instrumentation
// and no per-site registry: the closed table plus the strict typecheck prove
// the emittable set, and these tests prove every member validates in every
// context it admits.
import { describe, expect, mock, test } from "bun:test"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { envelopeFailurePath, validateGeneratedEnvelope } from "./generated-contract-tests"
import { configureCoreBinary } from "./core-runtime"
import { fakeTool } from "./plugin-tool.fixture"

mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

configureCoreBinary("concord")

const adapter = await import("./concord")

const TUPLES = {
  missing_binary: { kind: "transport_failure", adapter_reason: "missing_binary", effect_state: "none", recovery: "contact_operator" },
  spawn_failure: { kind: "transport_failure", adapter_reason: "spawn_failure", effect_state: "none", recovery: "contact_operator" },
  io_failure_retry: { kind: "transport_failure", adapter_reason: "io_failure", effect_state: "none", recovery: "retry_same_request" },
  io_failure_operator: { kind: "transport_failure", adapter_reason: "io_failure", effect_state: "none", recovery: "contact_operator" },
  malformed_core_response_retry: { kind: "malformed_response", adapter_reason: "malformed_core_response", effect_state: "none", recovery: "retry_same_request" },
  malformed_core_response_operator: { kind: "malformed_response", adapter_reason: "malformed_core_response", effect_state: "none", recovery: "contact_operator" },
  timeout_no_effect: { kind: "timeout", adapter_reason: "timeout_no_effect", effect_state: "none", recovery: "retry_same_request" },
  cancelled_no_effect: { kind: "cancelled", adapter_reason: "cancelled_no_effect", effect_state: "none", recovery: "retry_same_request" },
  manifest_mismatch: { kind: "transport_failure", adapter_reason: "manifest_mismatch", effect_state: "none", recovery: "contact_operator" },
  context_resolution_failed: { kind: "transport_failure", adapter_reason: "context_resolution_failed", effect_state: "none", recovery: "contact_operator" },
  host_lease_missing: { kind: "transport_failure", adapter_reason: "host_lease_missing", effect_state: "none", recovery: "contact_operator" },
  unknown_effect: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "reconcile_operation" },
  session_directory_unreadable: { kind: "transport_failure", adapter_reason: "session_directory_unreadable", effect_state: "none", recovery: "retry_same_request" },
  invalid_cli_input: { kind: "invalid_input", adapter_reason: "invalid_cli_input", effect_state: "none", recovery: "correct_request" },
  invalid_request_wrapper: { kind: "invalid_input", adapter_reason: "invalid_request_wrapper", effect_state: "none", recovery: "correct_request" },
  invalid_tool_input: { kind: "invalid_input", adapter_reason: "invalid_tool_input", effect_state: "none", recovery: "correct_request" },
}
type TupleKey = keyof typeof TUPLES

// retry_safe is derived in one place: false for the input entries and for
// possible effects, true for the other no-effect entries.
const isInputEntry = (key: TupleKey): boolean => TUPLES[key].kind === "invalid_input"
const expectedRetrySafe = (key: TupleKey): boolean => !isInputEntry(key) && TUPLES[key].effect_state !== "possible"

const declaredPairs = (contractOperations as readonly any[]).map((item) => ({ tool: item.tool, operation: item.id.split(".")[1] }))
const requestID = "request-concord-913"

const expectEnvelopeMatchesTuple = (envelope: any, key: TupleKey, tool: string, operation: string) => {
  const tuple = TUPLES[key]
  expect(validateGeneratedEnvelope(envelope), `${key} under ${tool}.${operation}: ${envelopeFailurePath(envelope)}`).toBe(true)
  expect(envelope.origin).toBe("adapter")
  expect(envelope.outcome).toBe("error")
  expect(envelope.tool).toBe(tool)
  expect(envelope.operation).toBe(operation)
  expect(envelope.request_id).toBe(requestID)
  expect(envelope.error.kind).toBe(tuple.kind)
  expect(envelope.error.adapter_reason).toBe(tuple.adapter_reason)
  expect(envelope.error.effect_state).toBe(tuple.effect_state)
  expect(envelope.error.recovery_action.kind).toBe(tuple.recovery)
  expect(envelope.error.retry_safe).toBe(expectedRetrySafe(key))
}

describe("CON-913 closed adapter error tuples", () => {
  test("CON-913 the runtime table admits exactly the decided closed key set", () => {
    expect(Object.keys(adapter.adapterErrorTuples).sort()).toEqual(Object.keys(TUPLES).sort())
    for (const entry of Object.values(adapter.adapterErrorTuples as Record<string, unknown>)) {
      expect(entry).toBeObject()
    }
  })

  test("CON-913 every generic key emits a schema-valid envelope under every declared pair", () => {
    for (const key of Object.keys(TUPLES) as TupleKey[]) {
      for (const pair of declaredPairs) {
        const envelope = adapter.adapterError(pair.tool, pair.operation, requestID, key, "bounded diagnostic")
        expectEnvelopeMatchesTuple(envelope, key, pair.tool, pair.operation)
      }
    }
  })

  test("CON-913 the factory refuses an undeclared tool or operation pair", () => {
    for (const key of ["missing_binary", "unknown_effect", "invalid_cli_input"] as const) {
      expect(() => adapter.adapterError("concord_work_browse", "no_such_operation", requestID, key as never, "undeclared operation"), `${key} undeclared operation`).toThrow(adapter.AdapterContractDefect)
      expect(() => adapter.adapterError("concord_not_a_tool", "list", requestID, key as never, "undeclared tool"), `${key} undeclared tool`).toThrow(adapter.AdapterContractDefect)
      expect(() => adapter.adapterError("concord_work_browse", "", requestID, key as never, "empty operation on a non-wrapper key"), `${key} empty operation`).toThrow(adapter.AdapterContractDefect)
    }
  })

  test("CON-913 a forged unknown key throws the contract defect with identity and is never serialized", () => {
    let caught: unknown = null
    try {
      adapter.adapterError("concord_work_browse", "list", requestID, "no_such_key" as never, "forged through a cast")
    } catch (error) {
      caught = error
    }
    expect(caught).toBeInstanceOf(adapter.AdapterContractDefect)
    expect(caught).toBeInstanceOf(Error)
    const message = (caught as Error).message
    expect(message).toContain("concord_work_browse")
    expect(message).toContain("list")
    expect(message).toContain(requestID)
    // No envelope exists for a forged key: the defect is the typed failure
    // itself, never a serialized refusal a catch could relabel.
    expect(validateGeneratedEnvelope(caught)).toBe(false)
  })

  test("CON-913 the factory truncates over-long UTF-8 messages to the schema maximum and never rejects", () => {
    const tool = "concord_work_browse"
    const operation = "list"
    const exact = adapter.adapterError(tool, operation, requestID, "spawn_failure", "y".repeat(1000))
    expect(validateGeneratedEnvelope(exact)).toBe(true)
    expect(exact.error.message).toBe("y".repeat(1000))
    const ascii = adapter.adapterError(tool, operation, requestID, "spawn_failure", "y".repeat(4000))
    expect(validateGeneratedEnvelope(ascii)).toBe(true)
    expect(Array.from(ascii.error.message as string).length).toBeLessThanOrEqual(1000)
    expect(ascii.error.message.startsWith("yyyy")).toBe(true)
    expect((ascii.error.message as string).length).toBeLessThan(4000)
    const emoji = adapter.adapterError(tool, operation, requestID, "spawn_failure", "🙂".repeat(1200))
    expect(validateGeneratedEnvelope(emoji)).toBe(true)
    expect(Array.from(emoji.error.message as string).length).toBeLessThanOrEqual(1000)
    expect(emoji.error.message.startsWith("🙂🙂")).toBe(true)
  })

  test("CON-913 envelope-level couplings: default reconcile, core negatives, partial outcomes", () => {
    const queryID = (tool: string, operation: string) => {
      const found = (contractOperations as readonly any[]).find((item) => item.tool === tool && item.id === `${tool}.${operation}`)
      return found?.query_id ? { query_id: found.query_id } : {}
    }
    const envelope = (fields: Record<string, unknown>) => ({
      schema_version: "1.0", manifest_digest: manifestDigest, request_id: requestID,
      origin: "core", tool: "concord_work_transition", operation: "lifecycle", ...queryID("concord_work_transition", "lifecycle"),
      outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
      ...fields,
    })
    const errorMember = (overrides: Record<string, unknown>) => ({
      kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "possible", message: "bounded diagnostic",
      ...overrides,
    })
    expect(validateGeneratedEnvelope(envelope({ error: errorMember({}) }))).toBe(true)
    expect(validateGeneratedEnvelope(envelope({ error: errorMember({ recovery_action: { kind: "retry_same_request" } }) }))).toBe(false)
    expect(validateGeneratedEnvelope(envelope({ error: errorMember({ adapter_reason: "unknown_effect" }) }))).toBe(false)
    const partial = (recovery: string) => ({
      schema_version: "1.0", manifest_digest: manifestDigest, request_id: requestID,
      origin: "core", tool: "concord_work_transition", operation: "lifecycle",
      outcome: "partial", resolved_scope: null,
      operation_ref: { id: "op-913", kind: "workflow", version: "1", state: "partial", current_step: "approve", updated_at: "2026-10-10T00:00:00Z" },
      completed_steps: ["step-one"],
      authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
      error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: recovery }, effect_state: "partial" },
    })
    expect(validateGeneratedEnvelope(partial("retry_same_request")), "partial operation_conflict retry must be refused").toBe(false)
    expect(validateGeneratedEnvelope(partial("reconcile_operation"))).toBe(true)
    const adapterBase = { ...envelope({}), origin: "adapter", authority: "unreachable" }
    const transport = (kind: string, effect: string) => ({ ...adapterBase, tool: "concord_work_browse", operation: "list", ...queryID("concord_work_browse", "list"), error: errorMember({ kind, adapter_reason: "malformed_core_response", effect_state: effect }) })
    expect(validateGeneratedEnvelope(transport("transport_failure", "possible")), "adapter transport + possible must be refused").toBe(false)
    expect(validateGeneratedEnvelope(transport("malformed_response", "possible")), "adapter malformed + possible must be refused").toBe(false)
    expect(validateGeneratedEnvelope(transport("transport_failure", "none")), "adapter transport + none stays admitted").toBe(true)
    const inputEnvelope = (reason: string, operation: string, effect: string) => ({
      ...adapterBase, tool: "concord_work_browse", ...(operation ? queryID("concord_work_browse", operation) : {}), operation,
      error: errorMember({ kind: "invalid_input", adapter_reason: reason, effect_state: effect, retry_safe: effect === "none" ? false : true, recovery_action: { kind: "correct_request" } }),
    })
    for (const reason of ["invalid_request_wrapper", "invalid_cli_input", "invalid_tool_input"]) {
      expect(validateGeneratedEnvelope(inputEnvelope(reason, "list", "none")), `${reason} declared pair`).toBe(true)
      expect(validateGeneratedEnvelope(inputEnvelope(reason, "list", "possible")), `${reason} possible must be refused`).toBe(false)
    }
    expect(validateGeneratedEnvelope(inputEnvelope("invalid_request_wrapper", "", "none")), "the wrapper reason keeps the empty-operation path").toBe(true)
    for (const reason of ["invalid_cli_input", "invalid_tool_input"]) {
      expect(validateGeneratedEnvelope(inputEnvelope(reason, "", "none")), `${reason} must not take the empty-operation path`).toBe(false)
    }
  })

  test("CON-913 producer detail shapes validate at the factory and an invalid shape refuses", () => {
    const tool = "concord_work_transition"
    const shapes: Array<Record<string, unknown>> = [
      { salvaged: { work_id: "work-1", changed_refs: [{ entity_kind: "work", id: "work-1", version: "2" }] } },
      { reconciled: { found: true, lifecycle: "in_progress", version: 3 } },
      { core_digest: "sha256:" + "a".repeat(64), adapter_digest: manifestDigest },
    ]
    for (const details of shapes) {
      const envelope = adapter.adapterError(tool, "project_handoff_record", requestID, "unknown_effect", "bounded diagnostic", details)
      expect(validateGeneratedEnvelope(envelope), JSON.stringify(details)).toBe(true)
      expect(envelope.error.details).toEqual(details)
    }
    expect(() => adapter.adapterError(tool, "project_handoff_record", requestID, "unknown_effect", "bounded diagnostic", { note: { nested: "objects are not admitted" } })).toThrow(adapter.AdapterContractDefect)
    expect(() => adapter.adapterError(tool, "project_handoff_record", requestID, "unknown_effect", "bounded diagnostic", { salvaged: { worktree_path: "/worktrees/work-1" } })).toThrow(adapter.AdapterContractDefect)
  })

  test("CON-913 validateAdapterEnvelope returns valid envelopes and fails closed only for adapter origin", () => {
    const valid = adapter.adapterError("concord_work_browse", "list", requestID, "unknown_effect", "bounded diagnostic")
    expect(adapter.validateAdapterEnvelope(valid)).toBe(valid)
    const core = {
      schema_version: "1.0", manifest_digest: manifestDigest, request_id: requestID, origin: "core", tool: "concord_work_browse", operation: "list",
      outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
      error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "possible", adapter_reason: "unknown_effect", message: "a core envelope never carries an adapter reason" },
    }
    expect(validateGeneratedEnvelope(core)).toBe(false)
    expect(adapter.validateAdapterEnvelope(core)).toBe(core)
    const mutation = adapter.adapterError("concord_work_transition", "lifecycle", requestID, "unknown_effect", "bounded diagnostic")
    const poisoned = { ...mutation, error: { ...mutation.error, details: { note: { nested: "shape" } } } }
    let caught: unknown = null
    try {
      adapter.validateAdapterEnvelope(poisoned)
    } catch (error) {
      caught = error
    }
    expect(caught).toBeInstanceOf(adapter.AdapterContractDefect)
    const defectMessage = (caught as Error).message
    expect(defectMessage).toContain("concord_work_transition")
    expect(defectMessage).toContain("lifecycle")
    expect(defectMessage).toContain(requestID)
    expect(defectMessage).toContain("details")
    expect(defectMessage).toContain("reconcile")
    expect(defectMessage).toContain("unknown")
    const read = adapter.adapterError("concord_work_browse", "list", requestID, "spawn_failure", "bounded diagnostic")
    const poisonedRead = { ...read, error: { ...read.error, effect_state: "possible" } }
    expect(() => adapter.validateAdapterEnvelope(poisonedRead)).toThrow(adapter.AdapterContractDefect)
  })

})
