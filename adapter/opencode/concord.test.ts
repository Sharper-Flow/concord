import { test, expect, mock } from "bun:test"
import { canonicalAssertion, contractOperations, manifestDigest, manifestVersion } from "./generated-contracts"
import { validateGeneratedEnvelope } from "./generated-contract-tests"
import approvalVector from "./approval-vector.json"
import grantAssertionVector from "./grant-assertion-vector.json"

function schemaBuilder() {
  return {
    optional() { return this }, strict() { return this }, min() { return this }, max() { return this }, int() { return this }, regex() { return this },
  }
}
const fakeTool = Object.assign((config: any) => config, {
  schema: {
    object: schemaBuilder, array: schemaBuilder, union: schemaBuilder, literal: schemaBuilder,
    string: schemaBuilder, number: schemaBuilder, unknown: schemaBuilder, null: schemaBuilder,
  },
})
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const source = await Bun.file(new URL("./concord.ts", import.meta.url)).text()
const adapter = await import("./concord")

test("exports exactly the generated tool names", () => {
  const names = [...source.matchAll(/export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(/g)].map((match) => match[1])
  expect(names).toEqual(["product_view", "work_browse", "work_trace", "knowledge", "work_define", "work_epic", "work_transition", "work_relate", "work_compact"])
  expect(new Set(contractOperations.map((operation: any) => operation.tool))).toEqual(new Set(names.map((name) => `concord_${name}`)))
})

test("transport and approval boundaries stay fail-closed", () => {
  expect(source).toContain('"concord", "invoke"')
  expect(source).toContain('always: []')
  expect(source).toContain("malformed_core_response")
  expect(source).toContain("operation_conflict")
  expect(source).toContain("secret-tool")
  expect(source).not.toContain("console.log")
  expect(source).not.toContain("grant_token:")
})

test("grant bootstrap sends typed assertion arrays and preserves the canonical vector", async () => {
  let grantRequest: any
  let calls = 0
  const result: any = await runProduct({ async run(_argv: string[], input: string) {
    calls++
    if (calls === 1) {
      grantRequest = JSON.parse(input)
      return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    }
    return { exitCode: 1, stdout: "", stderr: "invoke not needed" }
  } })
  expect(result.outcome).toBe("error")
  expect(Array.isArray(grantRequest.assertion.requested_project_ids)).toBe(true)
  expect(grantRequest.assertion.requested_project_ids).toEqual([])
  expect(Array.isArray(grantRequest.assertion.requested_capabilities)).toBe(true)
  expect(grantRequest.assertion.requested_capabilities).toEqual(["product_read"])
  const canonicalFields = { ...grantAssertionVector, canonical_base64: undefined }
  expect(Buffer.from(canonicalAssertion(canonicalFields as any)).toString("base64")).toBe(grantAssertionVector.canonical_base64)
})

test("launcher Product identity is validated and bound during grant bootstrap", async () => {
  const previous = process.env.CONCORD_SELECTED_PRODUCT_ID
  process.env.CONCORD_SELECTED_PRODUCT_ID = "product-launcher-51"
  let request: any
  try {
    await runProduct({ async run(_argv: string[], input: string) {
      if (!request) request = JSON.parse(input)
      return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    } })
  } finally {
    if (previous === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previous
  }
  expect(request.assertion.requested_product_id).toBe("product-launcher-51")
})

test("invalid launcher Product identity is not granted", async () => {
  const previous = process.env.CONCORD_SELECTED_PRODUCT_ID
  process.env.CONCORD_SELECTED_PRODUCT_ID = "../other-product"
  let request: any
  try {
    await runProduct({ async run(_argv: string[], input: string) {
      if (!request) request = JSON.parse(input)
      return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    } })
  } finally {
    if (previous === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previous
  }
  expect(request.assertion.requested_product_id).toBe("")
})

test("single core response rejects invalid trailing content", async () => {
  for (const suffix of [" garbage", "\n{}"] as const) {
    let calls = 0
    const result: any = await runProduct({ async run() {
      calls++
      if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
      return { exitCode: 0, stdout: `{"schema_version":"1.0"}${suffix}`, stderr: "" }
    } })
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.adapter_reason).toBe("malformed_core_response")
  }
})

const grantResponse = () => ({ manifest_digest: manifestDigest, surface_version: manifestVersion, grant_token: "secret", grant_ref: "grant-1", client_ref: "opencode", principal_ref: "principal-1", session_ref: "session-1", agent_ref: "agent-1", envelope_version: "1.0", scope_version: "1" })
const contextFor = (ask: () => Promise<void> = async () => {}, controller = new AbortController()): any => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: "/worktree", directory: "/worktree", abort: controller.signal, ask })
const assertAdapterEnvelope = (value: any) => {
  expect(validateGeneratedEnvelope(value), JSON.stringify(value)).toBe(true)
  expect(value.origin).toBe("adapter")
  expect(value.outcome).toBe("error")
}
const runProduct = (runner: any, options: { credentials?: any; ask?: () => Promise<void>; controller?: AbortController } = {}) => {
  adapter.configureConcordAdapter({ credentials: options.credentials ?? { async getPrivateKey() { return new Uint8Array(32) } }, runner })
  return adapter.product_view.execute({ operation: "resolve", input: { product_id: "product-1" } }, contextFor(options.ask, options.controller))
}
const runnerWithGrant = (invoke: any) => {
  let calls = 0
  return { calls: () => calls, async run(argv: string[], input: string, signal: AbortSignal) { calls++; if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }; const response = typeof invoke === "function" ? invoke(argv, input, signal, calls) : invoke; return response && typeof response === "object" && "stdout" in response ? response : { exitCode: 0, stdout: JSON.stringify(response), stderr: "" } } }
}

test("all grant and transport failures produce valid adapter envelopes", async () => {
  const cases: Array<[string, any, any, string, string]> = [
    ["credential failure", { async getPrivateKey() { throw new Error("secret-tool unavailable") } }, undefined, "transport_failure", "grant_bootstrap_failed"],
    ["grant bootstrap failure", undefined, { exitCode: 1, stdout: "", stderr: "grant failed" }, "transport_failure", "grant_bootstrap_failed"],
    ["missing binary", undefined, { code: "ENOENT" }, "transport_failure", "missing_binary"],
    ["spawn failure", undefined, new Error("spawn failed"), "transport_failure", "spawn_failure"],
  ]
  for (const [, credentials, grantFailure, kind, reason] of cases) {
    const runner = { async run() { if (grantFailure instanceof Error) throw grantFailure; if (grantFailure?.code) throw grantFailure; return grantFailure } }
    const result: any = await runProduct(runner, { credentials })
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe(kind)
    expect(result.error.adapter_reason).toBe(reason)
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("contact_operator")
  }
  for (const [reason, response] of [["manifest_mismatch", { ...grantResponse(), manifest_digest: "sha256:wrong" }], ["incompatible_contract", { ...grantResponse(), surface_version: "9.0.0" }]] as const) {
    const result: any = await runProduct({ async run() { return { exitCode: 0, stdout: JSON.stringify(response), stderr: "" } } })
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("transport_failure")
    expect(result.error.adapter_reason).toBe(reason)
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("contact_operator")
  }
})

test("I/O, malformed, timeout, and cancellation outcomes remain schema-valid", async () => {
  const io: any = await runProduct(runnerWithGrant({ exitCode: 1, stdout: "", stderr: "broken pipe" }))
  assertAdapterEnvelope(io)
  expect(io.error.kind).toBe("operation_conflict")
  expect(io.error.effect_state).toBe("possible")

  for (const stdout of ["not-json", "{}\n{}", "{} {}"] as const) {
    const malformed: any = await runProduct(runnerWithGrant({ exitCode: 0, stdout, stderr: "" }))
    assertAdapterEnvelope(malformed)
    expect(malformed.error.kind).toBe("malformed_response")
    expect(malformed.error.effect_state).toBe("possible")
  }

  const timeout: any = await runProduct({ async run() { throw Object.assign(new Error("timed out"), { name: "TimeoutError" }) } })
  assertAdapterEnvelope(timeout)
  expect(timeout.error.kind).toBe("timeout")
  expect(timeout.error.effect_state).toBe("none")

  const controller = new AbortController()
  controller.abort()
  const cancelled: any = await runProduct({ async run() { throw Object.assign(new Error("aborted"), { name: "AbortError" }) } }, { controller })
  assertAdapterEnvelope(cancelled)
  expect(cancelled.error.kind).toBe("cancelled")
  expect(cancelled.error.effect_state).toBe("none")
})

const approvalChallenge = () => ({
  schema_version: "1.0", contract_version: "2.0.0", request_id: "session-1-message-1", origin: "core", tool: "concord_work_transition", operation: "lifecycle", outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
  error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none", details: { approval_ref: "challenge-1", operation_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", scope: ["product:product-1", "project:project-1", "work:work-1"], versions: ["work:2"] } },
})
const approvalSuccess = () => ({
  schema_version: "1.0", contract_version: "2.0.0", request_id: "session-1-message-1", origin: "core", tool: "concord_work_transition", operation: "lifecycle", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
  result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
})
const runTransition = (runner: any, ask?: () => Promise<void>) => {
  adapter.configureConcordAdapter({ credentials: { async getPrivateKey() { return new Uint8Array(32) } }, runner })
  return adapter.work_transition.execute({ operation: "lifecycle", input: { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-1" } }, contextFor(ask))
}

const coreEnvelope = (tool: string, operation: string, outcome: string, fields: Record<string, unknown> = {}) => ({
  schema_version: "1.0", contract_version: "2.0.0", request_id: "session-1-message-1", origin: "core", tool, operation, ...(tool === "concord_product_view" ? { query_id: "PM1.Q1" } : {}), outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

test("generated and adapter validators reject unknown top-level fields for every outcome", async () => {
  const variants: Array<[string, any, any, any]> = [
    ["ok", coreEnvelope("concord_product_view", "resolve", "ok", { items: [{}] }), adapter.product_view, { operation: "resolve", input: {} }],
    ["pending", coreEnvelope("concord_work_compact", "publish", "pending", { operation_ref: { id: "op-1", kind: "publish", version: "1", state: "pending", current_step: "git", updated_at: "2026-08-08T12:00:00Z" }, next_action: { kind: "reconcile_operation" } }), adapter.work_compact, { operation: "publish", input: {} }],
    ["partial", coreEnvelope("concord_work_compact", "publish", "partial", { operation_ref: { id: "op-1", kind: "publish", version: "1", state: "partial", current_step: "sqlite", updated_at: "2026-08-08T12:00:00Z" }, completed_steps: ["git"], error: { kind: "operation_conflict", retry_safe: true, recovery_action: { kind: "reconcile_operation" }, effect_state: "partial" } }), adapter.work_compact, { operation: "publish", input: {} }],
    ["error", coreEnvelope("concord_work_transition", "lifecycle", "error", { error: { kind: "invalid_input", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none" } }), adapter.work_transition, { operation: "lifecycle", input: {} }],
  ]
  for (const [name, original, tool, args] of variants) {
    const unknown = { ...original, unknown_top_level: true }
    expect(validateGeneratedEnvelope(original), `${name} baseline`).toBe(true)
    expect(validateGeneratedEnvelope(unknown), `${name} unknown`).toBe(false)
    adapter.configureConcordAdapter({ credentials: { async getPrivateKey() { return new Uint8Array(32) } }, runner: runnerWithGrant(unknown) })
    const result: any = await tool.execute(args, contextFor())
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.adapter_reason).toBe("malformed_core_response")
  }
})

test("approval rejection and possible-effect conflict are valid adapter envelopes", async () => {
  const rejected: any = await runTransition(runnerWithGrant({ exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }), async () => { throw new Error("rejected") })
  assertAdapterEnvelope(rejected)
  expect(rejected.error.kind).toBe("cancelled")
  expect(rejected.error.effect_state).toBe("none")

  let calls = 0
  const conflicted: any = await runTransition({ async run(argv: string[]) { calls++; if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }; if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }; return { exitCode: 0, stdout: "not-json", stderr: "" } } })
  assertAdapterEnvelope(conflicted)
  expect(conflicted.error.kind).toBe("operation_conflict")
  expect(conflicted.error.adapter_reason).toBe("unknown_effect")
  expect(conflicted.error.effect_state).toBe("possible")
})

test("approval challenge is resubmitted once with the same idempotency key and signed binding", async () => {
  const requests: any[] = []
  let calls = 0
  const runner = { async run(_argv: string[], input: string) {
    calls++
    if (calls > 1) requests.push(JSON.parse(input))
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(approvalSuccess()), stderr: "" }
  } }
  let approvals = 0
  const result: any = await runTransition(runner, async () => { approvals++ })
  expect(result.outcome).toBe("ok")
  expect(validateGeneratedEnvelope(result)).toBe(true)
  expect(calls).toBe(3)
  expect(approvals).toBe(1)
  expect(requests[1].tool).toBe(requests[0].tool)
  expect(requests[1].operation).toBe(requests[0].operation)
  expect(requests[1].input.idempotency_key).toBe(requests[0].input.idempotency_key)
  expect(requests[1].input.approval.approval_ref).toBe("challenge-1")
  expect(requests[1].call_envelope.host_approval_assertion.scope).toEqual(["product:product-1", "project:project-1", "work:work-1"])
  expect(requests[1].call_envelope.host_approval_assertion.versions).toEqual(["work:2"])
  expect(typeof requests[1].call_envelope.host_approval_assertion.signature).toBe("string")
})

test("workflow premise approval asks with exact checkpoint metadata and no human identity", async () => {
  const requests: any[] = []
  const challenge = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none", details: {
      approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64), scope: ["product:product-1", "project:project-1", "work:work-1"], versions: ["work:7", "contract:1"],
      work_id: "work-1", action_id: "confirm_premise", contract_version: "1", selected_choice: "confirm", premise_summary: "Ship the approved workflow premise.", decision_context_digest: "sha256:" + "b".repeat(64),
    } },
  })
  const success = coreEnvelope("concord_work_transition", "workflow_action", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })
  let calls = 0
  const runner = { async run(_argv: string[], input: string) {
    calls++
    if (calls > 1) requests.push(JSON.parse(input))
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? challenge : success), stderr: "" }
  } }
  let askMetadata: any
  adapter.configureConcordAdapter({ credentials: { async getPrivateKey() { return new Uint8Array(32) } }, runner })
  const result: any = await adapter.work_transition.execute({ operation: "workflow_action", input: { work_id: "work-1", expected_version: 7, action_id: "confirm_premise", selected_choice: "confirm", decision_context_digest: "sha256:" + "b".repeat(64), idempotency_key: "confirm-1" } }, contextFor(async (request: any) => { askMetadata = request.metadata }))
  expect(result.outcome).toBe("ok")
  expect(askMetadata).toEqual({ approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64), work_id: "work-1", action_id: "confirm_premise", contract_version: "1", selected_choice: "confirm", decision_context_digest: "sha256:" + "b".repeat(64), premise_summary: "Ship the approved workflow premise." })
  expect(requests[1].call_envelope.host_approval_assertion.operator_principal_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_agent_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_session_ref).toBeUndefined()
})

test("fake seams exercise grant bootstrap and malformed-response handling", async () => {
  const calls: string[][] = []
  const grant = { manifest_digest: manifestDigest, surface_version: manifestVersion, grant_token: "secret", grant_ref: "grant-1", client_ref: "opencode", principal_ref: "principal-1", session_ref: "session-1", agent_ref: "agent-1", envelope_version: "1.0", scope_version: "1" }
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32) } },
    runner: { async run(argv: string[], _input: string, _signal: AbortSignal) { calls.push(argv); return calls.length === 1 ? { exitCode: 0, stdout: JSON.stringify(grant), stderr: "" } : { exitCode: 0, stdout: "not-json", stderr: "diagnostic" } } },
  })
  const context: any = { sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: "/worktree", directory: "/worktree", abort: new AbortController().signal, ask: async () => {} }
  const result: any = await adapter.product_view.execute({ operation: "resolve", input: {} }, context)
  expect(calls).toEqual([["concord", "grant"], ["concord", "invoke"]])
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.adapter_reason).toBe("malformed_core_response")
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
})

test("grant cache remains bound to the requested capability", async () => {
  let calls = 0
  const requestedCapabilities: string[] = []
  const runner = { async run(_argv: string[], input: string) {
    calls++
    const request = JSON.parse(input)
    if (calls === 1 || calls === 3) {
      requestedCapabilities.push(request.assertion.requested_capabilities[0])
      return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    }
    const response = calls === 2
      ? coreEnvelope("concord_product_view", "resolve", "error", { error: { kind: "invalid_input", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none" } })
      : coreEnvelope("concord_work_transition", "lifecycle", "error", { error: { kind: "invalid_input", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none" } })
    return { exitCode: 0, stdout: JSON.stringify(response), stderr: "" }
  } }
  adapter.configureConcordAdapter({ credentials: { async getPrivateKey() { return new Uint8Array(32) } }, runner })
  await adapter.product_view.execute({ operation: "resolve", input: { product_id: "product-1" } }, contextFor())
  await adapter.work_transition.execute({ operation: "lifecycle", input: { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-1" } }, contextFor())
  expect(requestedCapabilities).toEqual(["product_read", "work_transition"])
  expect(calls).toBe(4)
})

test("canonical host approval vector matches core encoding", () => {
  const { canonical_base64: expected, ...assertion } = approvalVector
  expect(Buffer.from(adapter.canonicalHostApproval(assertion)).toString("base64")).toBe(expected)
})

test("grant bootstrap advertises the generated surface identity", async () => {
  let assertion: any
  let calls = 0
  const runner = { async run(_argv: string[], input: string) {
    calls++
    if (calls === 1) {
      assertion = JSON.parse(input).assertion
      return { exitCode: 0, stdout: JSON.stringify(grantResponse()), stderr: "" }
    }
    return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", stage: "prototype", projects: [], candidates: [] } })), stderr: "" }
  } }
  await runProduct(runner)
  expect(assertion.client_version).toBe(manifestVersion)
  expect(assertion.surface_range).toBe(`${manifestVersion}-${manifestVersion}`)
  expect(assertion.manifest_digest).toBe(manifestDigest)
})
