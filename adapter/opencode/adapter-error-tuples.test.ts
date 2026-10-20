// Table tests: the closed adapter error tuple table and its
// factory, vacate constructor, and exit validator. No AST instrumentation
// and no per-site registry: the closed table plus the strict typecheck prove
// the emittable set, and these tests prove every member validates in every
// context it admits.
import { describe, expect, mock, test } from "bun:test"
import { contractOperations, maxEnvelopeBytes, manifestDigest } from "./generated-contracts"
import { envelopeFailurePath, validateGeneratedEnvelope } from "./generated-contract-tests"
import { configureCoreBinary } from "./dispatch"
import { fakeTool } from "./plugin-tool.fixture"

mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

configureCoreBinary("concord")

const adapter = await import("./concord")

const VACATE_TOOL = "concord_work_transition"
const VACATE_OPERATION = "session_vacate"
const VACATE_KEYS = ["vacate_replay", "vacate_unavailable"] as const

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
  worker_abandon_refused_none: { kind: "operation_conflict", adapter_reason: "worker_abandon_refused", effect_state: "none", recovery: "reconcile_operation" },
  worker_abandon_refused_possible: { kind: "operation_conflict", adapter_reason: "worker_abandon_refused", effect_state: "possible", recovery: "reconcile_operation" },
  worker_abandon_receipt_failed: { kind: "operation_conflict", adapter_reason: "worker_abandon_receipt_failed", effect_state: "possible", recovery: "reconcile_operation" },
  worker_reconcile_failed_recorded: { kind: "operation_conflict", adapter_reason: "worker_reconcile_failed_recorded", effect_state: "possible", recovery: "reconcile_operation" },
  lane_tool_refusal: { kind: "unauthorized", adapter_reason: "lane_tool_refusal", effect_state: "none", recovery: "contact_operator" },
  invalid_cli_input: { kind: "invalid_input", adapter_reason: "invalid_cli_input", effect_state: "none", recovery: "correct_request" },
  invalid_request_wrapper: { kind: "invalid_input", adapter_reason: "invalid_request_wrapper", effect_state: "none", recovery: "correct_request" },
  invalid_tool_input: { kind: "invalid_input", adapter_reason: "invalid_tool_input", effect_state: "none", recovery: "correct_request" },
  vacate_replay: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "retry_same_request" },
  vacate_unavailable: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", recovery: "contact_operator" },
}
type TupleKey = keyof typeof TUPLES
type VacateKey = (typeof VACATE_KEYS)[number]
type GenericKey = Exclude<TupleKey, VacateKey>

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
    for (const key of Object.keys(TUPLES).filter((key): key is GenericKey => !(VACATE_KEYS as readonly string[]).includes(key))) {
      for (const pair of declaredPairs) {
        // The vacate-scoped exception is exclusive: under the vacate pair an
        // adapter operation_conflict/unknown_effect/possible error must take
        // the replay or contact recovery, never the generic reconcile tuple.
        if (key === "unknown_effect" && pair.tool === VACATE_TOOL && pair.operation === VACATE_OPERATION) continue
        const envelope = adapter.adapterError(pair.tool, pair.operation, requestID, key, "bounded diagnostic")
        expectEnvelopeMatchesTuple(envelope, key, pair.tool, pair.operation)
      }
    }
  })

  test("CON-913 the generic unknown_effect tuple is refused under the vacate pair", () => {
    expect(() => adapter.adapterError(VACATE_TOOL, VACATE_OPERATION, requestID, "unknown_effect", "bounded diagnostic")).toThrow(adapter.AdapterContractDefect)
  })

  test("CON-913 vacate tuples validate only through the vacate constructor and pair", () => {
    for (const key of VACATE_KEYS) {
      const envelope = adapter.vacateError(requestID, key, "the relocation request stands")
      expectEnvelopeMatchesTuple(envelope, key, VACATE_TOOL, VACATE_OPERATION)
      // The vacate entries are reachable only through this constructor: the
      // generic factory refuses them under every declared pair, including
      // the vacate pair itself.
      for (const pair of declaredPairs) {
        expect(() => adapter.adapterError(pair.tool, pair.operation, requestID, key as never, "forged vacate key"), `${key} via adapterError under ${pair.tool}.${pair.operation}`).toThrow(adapter.AdapterContractDefect)
      }
      // The vacate constructor refuses generic keys in turn.
      expect(() => adapter.vacateError(requestID, "unknown_effect" as never, "forged generic key")).toThrow(adapter.AdapterContractDefect)
      expect(() => adapter.vacateError(requestID, "missing_binary" as never, "forged generic key")).toThrow(adapter.AdapterContractDefect)
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

  test("CON-913 envelope-level couplings: default reconcile, vacate exception scope, core negatives, partial outcomes", () => {
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
    const coreVacateRetry = envelope({ operation: VACATE_OPERATION, ...queryID("concord_work_transition", VACATE_OPERATION), error: errorMember({ recovery_action: { kind: "retry_same_request" } }) })
    expect(validateGeneratedEnvelope(coreVacateRetry), "core vacate retry must stay refused").toBe(false)
    const adapterVacateReplay = { ...coreVacateRetry, origin: "adapter", authority: "unreachable", error: errorMember({ adapter_reason: "unknown_effect", effect_state: "possible", recovery_action: { kind: "retry_same_request" } }) }
    expect(validateGeneratedEnvelope(adapterVacateReplay), "adapter vacate replay must pass").toBe(true)
    const adapterVacateContact = { ...adapterVacateReplay, error: { ...adapterVacateReplay.error, recovery_action: { kind: "contact_operator" } } }
    expect(validateGeneratedEnvelope(adapterVacateContact), "adapter vacate contact must pass").toBe(true)
    for (const mutation of [
      (candidate: any) => ({ ...candidate, origin: "core", authority: "authoritative", error: { ...candidate.error, adapter_reason: undefined } }),
      (candidate: any) => ({ ...candidate, tool: "concord_work_browse", operation: "list", ...queryID("concord_work_browse", "list") }),
      (candidate: any) => ({ ...candidate, operation: "lifecycle", ...queryID("concord_work_transition", "lifecycle") }),
      (candidate: any) => ({ ...candidate, error: { ...candidate.error, adapter_reason: "worker_abandon_refused" } }),
      (candidate: any) => ({ ...candidate, error: { ...candidate.error, effect_state: "none" } }),
    ]) {
      expect(validateGeneratedEnvelope(mutation(adapterVacateReplay)), "exception must not stretch beyond its scope").toBe(false)
    }
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
      { salvaged: { work_id: "work-1", worktree_path: "/worktrees/work-1", changed_refs: [{ entity_kind: "work", id: "work-1", version: "2" }] } },
      { reconciled: { found: true, lifecycle: "in_progress", version: 3 } },
      { abandon_error_message: "store: worker_fail: refused", recovery_stopped_at: "worker_abandon_retry", recovery_error_message: "vacate refused" },
      { recovery_steps: ["session_vacate: moved", "worker_abandon: closed"] },
      { work_id: "work-1", attempt_id: "attempt-913" },
      { core_digest: "sha256:" + "a".repeat(64), adapter_digest: manifestDigest },
    ]
    for (const details of shapes) {
      const envelope = adapter.adapterError(tool, "worktree_claim", requestID, "unknown_effect", "bounded diagnostic", details)
      expect(validateGeneratedEnvelope(envelope), JSON.stringify(details)).toBe(true)
      expect(envelope.error.details).toEqual(details)
    }
    expect(() => adapter.adapterError(tool, "worktree_claim", requestID, "unknown_effect", "bounded diagnostic", { note: { nested: "objects are not admitted" } })).toThrow(adapter.AdapterContractDefect)
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

  test("CON-913 staleness notices ride final bytes without degrading an adapter error envelope", () => {
    const staleness = { pinnedReleaseRoot: "/d/v11.40.3", installedReleaseRoot: "/d/v11.40.6", pinnedRelease: "v11.40.3", installedRelease: "v11.40.6" }
    const tool = "concord_work_browse"
    const roomy = adapter.adapterError(tool, "list", requestID, "spawn_failure", "bounded diagnostic")
    const carried = adapter.withReleaseStaleness(roomy, staleness)
    expect(carried.extraWarnings).toEqual([])
    expect(validateGeneratedEnvelope(carried.envelope)).toBe(true)
    expect(Buffer.byteLength(JSON.stringify(carried.envelope))).toBeLessThanOrEqual(maxEnvelopeBytes)
    expect((carried.envelope as any).warnings.at(-1)?.kind).toBe("release_stale")
    const heavy = adapter.adapterError(tool, "list", requestID, "spawn_failure", "bounded diagnostic", { filler: "x".repeat(60_000) })
    expect(validateGeneratedEnvelope(heavy)).toBe(true)
    expect(Buffer.byteLength(JSON.stringify(heavy))).toBeGreaterThan(maxEnvelopeBytes)
    const fallen = adapter.withReleaseStaleness(heavy, staleness)
    expect(fallen.envelope).toBe(heavy)
    expect(fallen.extraWarnings).toHaveLength(1)
    expect(fallen.extraWarnings[0]).toContain("restart this session")
    expect(validateGeneratedEnvelope(fallen.envelope)).toBe(true)
  })
})
