import { test, expect, mock } from "bun:test"
import { chmod, mkdir, mkdtemp, rm } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope } from "./generated-contract-tests"
import type { ChildRunnerOptions } from "./concord"

function schemaBuilder(kind: string, ...args: unknown[]) {
  return {
    kind, args, metadata: undefined as unknown,
    optional() { return this }, strict() { return this }, min() { return this }, max() { return this }, int() { return this }, regex() { return this },
    meta(metadata: unknown) { this.metadata = metadata; return this },
  }
}
const fakeTool = Object.assign((config: any) => config, {
  schema: {
    object: (...args: unknown[]) => schemaBuilder("object", ...args),
    record: (...args: unknown[]) => schemaBuilder("record", ...args),
    union: (...args: unknown[]) => schemaBuilder("union", ...args),
    literal: (...args: unknown[]) => schemaBuilder("literal", ...args),
    array: (...args: unknown[]) => schemaBuilder("array", ...args),
    string: (...args: unknown[]) => schemaBuilder("string", ...args),
    number: (...args: unknown[]) => schemaBuilder("number", ...args),
    unknown: (...args: unknown[]) => schemaBuilder("unknown", ...args),
    null: (...args: unknown[]) => schemaBuilder("null", ...args),
  },
})
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const source = await Bun.file(new URL("./concord.ts", import.meta.url)).text()
const credentialSource = await Bun.file(new URL("./credentials.ts", import.meta.url)).text()
const adapter = await import("./concord")
const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })

test("exports exactly the generated tool names", () => {
  const names = [...source.matchAll(/export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(/g)].map((match) => match[1]).filter((name) => name !== "work_start")
  expect(names).toEqual(["product_view", "work_browse", "work_trace", "knowledge", "work_define", "domain", "work_initiative", "work_transition", "work_relate", "work_compact"])
  expect(source).toContain("export const work_start = tool(")
  expect(new Set(contractOperations.map((operation: any) => operation.tool))).toEqual(new Set(names.map((name) => `concord_${name}`)))
})

test("published tool arguments expose one generated request union", () => {
  const tools = {
    concord_product_view: adapter.product_view,
    concord_work_browse: adapter.work_browse,
    concord_work_trace: adapter.work_trace,
    concord_knowledge: adapter.knowledge,
    concord_work_define: adapter.work_define,
    concord_domain: adapter.domain,
    concord_work_initiative: adapter.work_initiative,
    concord_work_transition: adapter.work_transition,
    concord_work_relate: adapter.work_relate,
    concord_work_compact: adapter.work_compact,
  } as const
  for (const [toolName, exportedTool] of Object.entries(tools)) {
    expect(Object.keys((exportedTool as any).args), toolName).toEqual(["request"])
    const published = adapter.publishedRequestSchema(toolName) as any
    const expected = contractOperations.filter((item: any) => item.tool === toolName).map((item: any) => item.id.split(".")[1])
    expect(published.oneOf.map((variant: any) => variant.properties.operation.const), toolName).toEqual(expected)
    expect(JSON.stringify(published), toolName).not.toContain("~standard")
    expect(JSON.stringify(published), toolName).not.toContain('"def"')
  }
  const published = adapter.publishedRequestSchema("concord_work_define") as any
  const capture = published.oneOf.find((variant: any) => variant.properties.operation.const === "capture")
  const inputRef = capture.properties.input.$ref.replace("#/properties/request/definitions/", "")
  const urgencyRef = published.definitions[inputRef].properties.urgency.$ref.replace("#/properties/request/definitions/", "")
  expect(published.definitions[urgencyRef].enum).toEqual(["standard", "expedite"])
  expect(Object.keys((adapter.work_start as any).args).sort()).toEqual(["title", "value_statement", "kind", "task", "idempotency_key", "priority", "urgency", "tags", "workflow_type_ref", "external_ref", "governing_requirements", "ref"].sort())
  expect((adapter.work_start as any).args.product_id).toBeUndefined()
  expect((adapter.work_start as any).args.project_id).toBeUndefined()
})

test("all exported tools return one serialized Concord envelope", async () => {
  const tools = [
    ["concord_product_view", adapter.product_view],
    ["concord_work_browse", adapter.work_browse],
    ["concord_work_trace", adapter.work_trace],
    ["concord_knowledge", adapter.knowledge],
    ["concord_work_define", adapter.work_define],
    ["concord_domain", adapter.domain],
    ["concord_work_initiative", adapter.work_initiative],
    ["concord_work_transition", adapter.work_transition],
    ["concord_work_relate", adapter.work_relate],
    ["concord_work_compact", adapter.work_compact],
  ] as const
  for (const [toolName, exportedTool] of tools) {
    const operation = contractOperations.find((candidate: any) => candidate.tool === toolName)!.id.split(".")[1]
    const core = coreEnvelope(toolName, operation, "error", {
      error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
    })
    expect(validateGeneratedEnvelope(core), `${toolName} core fixture`).toBe(true)
    adapter.configureConcordAdapter({ runner: runnerWithContext(core) })
    const hostResult: any = await exportedTool.execute(hostCall(operation, {}), contextFor())
    expect(hostResult).toMatchObject({ title: toolName, metadata: {} })
    expect(typeof hostResult.output).toBe("string")
    const envelope = JSON.parse(hostResult.output)
    expect(hostResult.output).toBe(JSON.stringify(envelope))
    expect(validateGeneratedEnvelope(envelope), `${toolName} returned invalid envelope: ${hostResult.output}`).toBe(true)
    expect(envelope.tool).toBe(toolName)
  }
})

test("oversize core results become bounded ToolResult error envelopes", async () => {
  const oversized = coreEnvelope("concord_product_view", "resolve", "error", {
    evidence_refs: Array.from({ length: 32 }, (_, index) => ({
      kind: "artifact", authority: "core", locator_kind: "id", locator: `${index}-${"x".repeat(2040)}`,
    })),
    error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
  })
  expect(validateGeneratedEnvelope(oversized)).toBe(true)
  expect(Buffer.byteLength(JSON.stringify(oversized))).toBeGreaterThan(51_200)
  adapter.configureConcordAdapter({ runner: runnerWithContext(oversized) })
  const hostResult = await adapter.product_view.execute(hostCall("resolve", {}), contextFor())
  expect(typeof hostResult).not.toBe("string")
  if (typeof hostResult === "string") throw new Error("adapter returned a string ToolResult")
  expect(Buffer.byteLength(hostResult.output)).toBeLessThanOrEqual(51_200)
  const envelope = JSON.parse(hostResult.output)
  expect(validateGeneratedEnvelope(envelope)).toBe(true)
  expect(envelope.error.kind).toBe("malformed_response")
  expect(envelope.error.adapter_reason).toBe("malformed_core_response")
})

test("work transition keeps lane dispatch behind the host result encoder", async () => {
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("generic transport must not run") } } })
  const envelope: any = await rawHostResult(adapter.work_transition.execute(hostCall(
    "workflow_action",
    { action_id: "dispatch_worker", fields: {} },
  ), contextFor()))
  expect(calls).toBe(0)
  expect(envelope.outcome).toBe("error")
  expect(envelope.error.message).toBe("dispatch_worker requires fields.lane_id naming the target lane")
})

test("transport and approval boundaries stay fail-closed", () => {
  expect(source).toContain('"concord", "invoke"')
  expect(source).toContain('always: []')
  expect(source).toContain("malformed_core_response")
  expect(source).toContain("operation_conflict")
  expect(credentialSource).toContain("secret-tool")
  expect(source).not.toContain("console.log")
})

test("project context is resolved before invoke", async () => {
  const previous = process.env.CONCORD_SELECTED_PRODUCT_ID
  process.env.CONCORD_SELECTED_PRODUCT_ID = "product-launcher-51"
  const requests: any[] = []
  try {
    await runProduct({ async run(_argv: string[], input: string) {
      requests.push(JSON.parse(input))
      return requests.length === 1
        ? { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
        : { exitCode: 1, stdout: "", stderr: "invoke not needed" }
    } })
  } finally {
    if (previous === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previous
  }
  expect(requests[0]).toEqual({ directory: "/worktree", worktree: "/worktree" })
  expect(requests[1].call_envelope.ambient_project_id).toBe("project-1")
  expect(requests[1].call_envelope.selected_product_id).toBe("product-launcher-51")
})

test("single core response rejects invalid trailing content", async () => {
  for (const suffix of [" garbage", "\n{}"] as const) {
    let calls = 0
    const result: any = await runProduct({ async run() {
      calls++
      if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return { exitCode: 0, stdout: `{"schema_version":"1.0"}${suffix}`, stderr: "" }
    } })
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.adapter_reason).toBe("malformed_core_response")
  }
})

// A core response whose manifest digest differs from the adapter's generated
// contract is version skew: the adapter files on disk were replaced by a newer
// release while this session still runs the old module. It is deterministic,
// so it must be typed and name the remedy, not fold into malformed_core_response.
const foreignDigest = (envelope: Record<string, unknown>) => ({ ...envelope, manifest_digest: "sha256:" + "0".repeat(63) + "1" })

test("a read answered by a newer core contract is typed as version skew", async () => {
  const result: any = await runProduct(runnerWithContext((argv: string[]) => foreignDigest(coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } }))))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("transport_failure")
  expect(result.error.adapter_reason).toBe("manifest_mismatch")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("contact_operator")
  expect(result.error.message).toContain("restart")
})

test("a mutation answered by a newer core contract reports unknown effect and names the remedy", async () => {
  adapter.configureConcordAdapter({ runner: runnerWithContext(foreignDigest(coreEnvelope("concord_work_define", "capture", "ok", { changed_refs: [] }))) })
  const result: any = await rawHostResult(adapter.work_define.execute(hostCall("capture", { title: "t", value_statement: "v", kind: "task", project_ids: ["p"], idempotency_key: "k" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.adapter_reason).toBe("unknown_effect")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(result.error.message).toContain("restart")
})

test("a same-generation malformed response is still malformed_core_response", async () => {
  const result: any = await runProduct(runnerWithContext(coreEnvelope("concord_product_view", "resolve", "ok", { result: { unexpected: true } })))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.adapter_reason).toBe("malformed_core_response")
  expect(result.error.effect_state).toBe("possible")
})

const contextResponse = (main_worktree = true, product_ids = ["product-1"]) => ({ project_id: "project-1", product_ids, scope_version: "1", main_worktree })
const contextFor = (ask: (...args: unknown[]) => unknown = () => {}, controller = new AbortController(), directory = "/worktree", worktree = directory): any => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree, directory, abort: controller.signal, ask })
const rawHostResult = async (result: Promise<string | { output: string }>) => {
  const value = await result
  if (typeof value === "string") throw new Error("adapter returned a string ToolResult")
  return JSON.parse(value.output)
}
const assertAdapterEnvelope = (value: any) => {
  expect(validateGeneratedEnvelope(value), JSON.stringify(value)).toBe(true)
  expect(value.origin).toBe("adapter")
  expect(value.outcome).toBe("error")
}
const runProduct = (runner: any, options: { ask?: () => Promise<void>; controller?: AbortController } = {}) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor(options.ask, options.controller)))
}
const runnerWithContext = (invoke: any) => {
  let calls = 0
  return { calls: () => calls, async run(argv: string[], input: string, signal: AbortSignal) { calls++; if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }; const response = typeof invoke === "function" ? invoke(argv, input, signal, calls) : invoke; return response && typeof response === "object" && "stdout" in response ? response : { exitCode: 0, stdout: JSON.stringify(response), stderr: "" } } }
}

test("all context and transport failures produce valid adapter envelopes", async () => {
  const cases: Array<[string, any, any, string, string]> = [
    ["context resolution failure", undefined, { exitCode: 1, stdout: "", stderr: "context resolution failed" }, "transport_failure", "io_failure"],
    ["missing binary", undefined, { code: "ENOENT" }, "transport_failure", "missing_binary"],
    ["spawn failure", undefined, new Error("spawn failed"), "transport_failure", "spawn_failure"],
  ]
  for (const [, , contextFailure, kind, reason] of cases) {
    const runner = { async run() { if (contextFailure instanceof Error) throw contextFailure; if (contextFailure?.code) throw contextFailure; return contextFailure } }
    const result: any = await runProduct(runner)
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe(kind)
    expect(result.error.adapter_reason).toBe(reason)
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("contact_operator")
  }
})

test("I/O, malformed, timeout, and cancellation outcomes remain schema-valid", async () => {
  const io: any = await runProduct(runnerWithContext({ exitCode: 1, stdout: "", stderr: "broken pipe" }))
  assertAdapterEnvelope(io)
  expect(io.error.kind).toBe("operation_conflict")
  expect(io.error.effect_state).toBe("possible")

  for (const stdout of ["not-json", "{}\n{}", "{} {}"] as const) {
    const malformed: any = await runProduct(runnerWithContext({ exitCode: 0, stdout, stderr: "" }))
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
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool: "concord_work_transition", operation: "lifecycle", outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
  error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none", details: { approval_ref: "challenge-1", operation_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", scope: ["product:product-1", "project:project-1", "work:work-1"], versions: ["work:2"] } },
})
const approvalSuccess = () => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool: "concord_work_transition", operation: "lifecycle", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
  result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
})
const runTransition = (runner: any, ask?: () => Promise<void>) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-1" }), contextFor(ask)))
}

const coreEnvelope = (tool: string, operation: string, outcome: string, fields: Record<string, unknown> = {}) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool, operation, ...((contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any)?.query_id ? { query_id: (contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any).query_id } : {}), outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

test("every declared query id passes the generated envelope contract", () => {
  // The generated validator runs on every core response. A query_id pattern
  // narrower than the manifest makes the adapter answer malformed_core_response
  // for a well-formed result, so each declared id is checked against it here.
  // An error outcome carries no result, so the only operation-specific field
  // under test is the query id itself.
  const error = { kind: "timeout", retry_safe: true, recovery_action: { kind: "retry_same_request" }, effect_state: "none" }
  const declared = contractOperations.filter((candidate: any) => candidate.query_id) as any[]
  expect(declared.length).toBeGreaterThan(0)
  for (const operation of declared) {
    const envelope: any = coreEnvelope(operation.tool, operation.id.split(".").slice(1).join("."), "error", { error })
    expect(envelope.query_id, `${operation.id} lost its query id`).toBe(operation.query_id)
    expect(
      validateGeneratedEnvelope(envelope),
      `${operation.id} carries query id ${operation.query_id}, which the envelope contract rejects`,
    ).toBe(true)
  }
})

test("overlap operation and refusal pass the generated adapter boundary", async () => {
  const success = coreEnvelope("concord_work_relate", "resolve_overlap", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [],
    next_valid_intents: [],
  })
  expect(validateGeneratedEnvelope(success)).toBe(true)
  adapter.configureConcordAdapter({ runner: runnerWithContext(success) })
  const resolved: any = await rawHostResult(adapter.work_relate.execute(hostCall("resolve_overlap", {
    from_work_id: "work-1", to_work_id: "work-2",
    from_expected_version: 2, to_expected_version: 2,
    from_contract_version: 1, to_contract_version: 1,
    resolution_kind: "compatible_with", reason: "The approved contracts can proceed together.",
    idempotency_key: "resolve-overlap-1",
  }), contextFor()))
  expect(resolved.outcome).toBe("ok")

  const refusal = coreEnvelope("concord_work_transition", "lifecycle", "error", {
    error: {
      kind: "domain_overlap", retry_safe: false,
      recovery_action: { kind: "request_approval" }, effect_state: "none",
      domain_overlap: {
        overlaps: [{
          product_id: "product-1", from_work_id: "work-1", to_work_id: "work-2",
          from_contract_version: 1, to_contract_version: 1,
          shared_affected_domain_ids: ["domain-1"], shared_law_ids: [],
          shared_domain_modifications: [], shared_relation_tuples: [],
          overlap_classes: ["architecture"], resolution_state: "unresolved",
          recovery_actions: ["resolve_overlap"], shared_affected_domain_count: 1,
          shared_law_count: 0, shared_domain_modification_count: 0,
          shared_relation_tuple_count: 0, detail_truncated: false,
        }],
        total_overlaps: 1, returned_overlaps: 1, truncated: false,
      },
    },
  })
  expect(validateGeneratedEnvelope(refusal)).toBe(true)
  const blocked: any = await runTransition(runnerWithContext(refusal))
  expect(blocked.error.kind).toBe("domain_overlap")

  const staleLaw = coreEnvelope("concord_work_transition", "lifecycle", "error", {
    error: {
      kind: "stale_law_revision", retry_safe: false,
      recovery_action: { kind: "request_approval" }, effect_state: "none",
      stale_law_revision: {
        old_law_id: "law-old", old_content_hash: `sha256:${"a".repeat(64)}`,
        accepted_successor_law_id: "law-current", accepted_successor_content_hash: `sha256:${"b".repeat(64)}`,
        recovery_actions: ["revise_contract"],
      },
    },
  })
  expect(validateGeneratedEnvelope(staleLaw)).toBe(true)
  const stale: any = await runTransition(runnerWithContext(staleLaw))
  expect(stale.error.kind).toBe("stale_law_revision")
})

test("overlap approval asks with exact direction and resolution consequence", async () => {
  const digest = `sha256:${"c".repeat(64)}`
  const challenge = coreEnvelope("concord_work_relate", "resolve_overlap", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: {
        tool: "concord_work_relate", operation: "resolve_overlap", consequence: "relation",
        operation_digest: digest, scope: ["product_id:product-1", "work_ids:work-1", "work_ids:work-2"],
        versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"], expires_at: "2026-08-20T00:00:00Z",
      },
      details: {
      approval_ref: "overlap-challenge-1", operation_digest: digest,
      summary: "Approve the exact requested mutation, scope, and expected versions.",
      scope: ["product:product-1", "work:work-1", "work:work-2"],
      versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"],
      resolution_kind: "depends_on", from_work_id: "work-1", to_work_id: "work-2",
    } },
  })
  const success = coreEnvelope("concord_work_relate", "resolve_overlap", "ok", {
    result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
  })
  let calls = 0
  const runner = { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? challenge : success), stderr: "" }
  } }
  let askMetadata: any
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_relate.execute(hostCall("resolve_overlap", {
    from_work_id: "work-1", to_work_id: "work-2",
    from_expected_version: 2, to_expected_version: 3,
    from_contract_version: 1, to_contract_version: 1,
    resolution_kind: "depends_on", reason: "Work 2 must establish the shared law first.",
    idempotency_key: "resolve-overlap-approval-1",
  }), contextFor(async (request: any) => { askMetadata = request.metadata })))
  expect(result.outcome).toBe("ok")
  expect(askMetadata).toEqual({
    approval_ref: "overlap-challenge-1", operation_digest: digest,
    summary: "Approve the exact requested mutation, scope, and expected versions.",
    scope: ["product:product-1", "work:work-1", "work:work-2"],
    versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"],
    resolution_kind: "depends_on", from_work_id: "work-1", to_work_id: "work-2",
    consequence_summary: {
      tool: "concord_work_relate", operation: "resolve_overlap", consequence: "relation",
      operation_digest: digest, scope: ["product_id:product-1", "work_ids:work-1", "work_ids:work-2"],
      versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"], expires_at: "2026-08-20T00:00:00Z",
    },
  })
})

test("generated and adapter validators reject unknown top-level fields for every outcome", async () => {
  const variants: Array<[string, any, any, any]> = [
    ["ok", coreEnvelope("concord_product_view", "resolve", "ok", { items: [{}] }), adapter.product_view, hostCall("resolve", {})],
    ["pending", coreEnvelope("concord_work_compact", "publish", "pending", { operation_ref: { id: "op-1", kind: "publish", version: "1", state: "pending", current_step: "git", updated_at: "2026-08-08T12:00:00Z" }, next_action: { kind: "reconcile_operation" } }), adapter.work_compact, hostCall("publish", {})],
    ["partial", coreEnvelope("concord_work_compact", "publish", "partial", { operation_ref: { id: "op-1", kind: "publish", version: "1", state: "partial", current_step: "sqlite", updated_at: "2026-08-08T12:00:00Z" }, completed_steps: ["git"], error: { kind: "operation_conflict", retry_safe: true, recovery_action: { kind: "reconcile_operation" }, effect_state: "partial" } }), adapter.work_compact, hostCall("publish", {})],
    ["error", coreEnvelope("concord_work_transition", "lifecycle", "error", { error: { kind: "invalid_input", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none" } }), adapter.work_transition, hostCall("lifecycle", {})],
  ]
  for (const [name, original, tool, args] of variants) {
    const unknown = { ...original, unknown_top_level: true }
    expect(validateGeneratedEnvelope(original), `${name} baseline`).toBe(true)
    expect(validateGeneratedEnvelope(unknown), `${name} unknown`).toBe(false)
    adapter.configureConcordAdapter({ runner: runnerWithContext(unknown) })
    const result: any = await rawHostResult(tool.execute(args, contextFor()))
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.adapter_reason).toBe("malformed_core_response")
  }
})

test("approval rejection and possible-effect conflict are valid adapter envelopes", async () => {
  const rejected: any = await runTransition(runnerWithContext({ exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }), async () => { throw new Error("rejected") })
  assertAdapterEnvelope(rejected)
  expect(rejected.error.kind).toBe("cancelled")
  expect(rejected.error.effect_state).toBe("none")

  let calls = 0
  const conflicted: any = await runTransition({ async run(argv: string[]) { calls++; if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }; if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }; return { exitCode: 0, stdout: "not-json", stderr: "" } } })
  assertAdapterEnvelope(conflicted)
  expect(conflicted.error.kind).toBe("operation_conflict")
  expect(conflicted.error.adapter_reason).toBe("unknown_effect")
  expect(conflicted.error.effect_state).toBe("possible")
})

test("approval challenge is resubmitted once with the same idempotency key and unsigned binding", async () => {
  const requests: any[] = []
  let calls = 0
  const runner = { async run(_argv: string[], input: string) {
    calls++
    if (calls > 1) requests.push(JSON.parse(input))
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
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
  expect(requests[1].call_envelope.host_approval_assertion.signature).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.nonce).toBeUndefined()
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
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? challenge : success), stderr: "" }
  } }
  let askMetadata: any
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", { work_id: "work-1", expected_version: 7, action_id: "confirm_premise", selected_choice: "confirm", decision_context_digest: "sha256:" + "b".repeat(64), idempotency_key: "confirm-1" }), contextFor(async (request: any) => { askMetadata = request.metadata })))
  expect(result.outcome).toBe("ok")
  expect(askMetadata).toEqual({ approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64), work_id: "work-1", action_id: "confirm_premise", contract_version: "1", selected_choice: "confirm", decision_context_digest: "sha256:" + "b".repeat(64), premise_summary: "Ship the approved workflow premise." })
  expect(requests[1].call_envelope.host_approval_assertion.operator_principal_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_agent_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_session_ref).toBeUndefined()
})

test("fake seams exercise context resolution and malformed-response handling", async () => {
  const calls: string[][] = []
  adapter.configureConcordAdapter({
    runner: { async run(argv: string[], _input: string, _signal: AbortSignal) { calls.push(argv); return calls.length === 1 ? { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" } : { exitCode: 0, stdout: "not-json", stderr: "diagnostic" } } },
  })
  const context: any = { sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: "/worktree", directory: "/worktree", abort: new AbortController().signal, ask: async () => {} }
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), context))
  expect(calls).toEqual([["concord", "project-resolve"], ["concord", "invoke"]])
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.adapter_reason).toBe("malformed_core_response")
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
})

const bootstrapArgs = {
  title: "Add atomic start",
  value_statement: "Start work in its linked worktree.",
  kind: "task" as const,
  task: "Implement the bounded adapter task.",
  idempotency_key: "start-1",
  priority: 10,
  urgency: "standard" as const,
  tags: ["adapter"],
  workflow_type_ref: "workflow-1",
  external_ref: "issue-611",
  governing_requirements: ["CD-0010"],
  ref: "HEAD",
}

const bootstrapSuccess = (path = "/data/worktrees/project-1/work-1") => ({
  schema_version: "1.0",
  operation_id: "bootstrap-1",
  replayed: false,
  product_id: "product-1",
  project_id: "project-1",
  work_id: "work-1",
  work_version: 2,
  worktree: { set_id: "worktree-set-1", path, branch: "work/work-1", base_sha: "a".repeat(40), state: "active" },
})

const launchContract = (agent = "concord-implement", prompt = "Implement the task.", sessionID: string | null = null) => ({
  schema_version: "1.0",
  operation_id: "bootstrap-1",
  attempt_id: "bootstrap-1:launch",
  launch_state: "prepared",
  session_id: sessionID,
  spawn_permitted: true,
  rollback_permitted: false,
  recovery_lookup_permitted: false,
  title: "concord-work-start-bootstrap-1",
  directory: "/data/worktrees/project-1/work-1",
  product_id: "product-1",
  work_id: "work-1",
  agent,
  prompt,
})

const runOutput = (sessionID = "session-run-1", text = "done") => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID }),
  JSON.stringify({ type: "text", timestamp: 2, sessionID, part: { type: "text", text } }),
  JSON.stringify({ type: "step_finish", timestamp: 3, sessionID, part: { type: "step-finish", reason: "stop" } }),
].join("\n")

const exportOutput = (sessionID = "session-run-1", agent = "concord-implement") => JSON.stringify({
  info: { id: sessionID },
  messages: [{ info: { id: "message-1", sessionID, role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }],
})

test("work_start orders bootstrap, session preparation, launch, and export with bound cwd and identity", async () => {
  const calls: Array<{ argv: string[]; input: string; options?: any }> = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
    calls.push({ argv, input, options })
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session-exec") return { exitCode: 0, stdout: runOutput(), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportOutput(), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(calls.map(({ argv }) => argv)).toEqual([
    ["concord", "project-resolve"],
    ["concord", "work-bootstrap"],
    ["concord", "session-prepare"],
    ["concord", "session-exec"],
    ["concord", "session-record"],
    ["opencode", "export", "session-run-1", "--sanitize"],
    ["concord", "session-record"],
  ])
  expect(JSON.parse(calls[0].input)).toEqual({ directory: "/worktree", worktree: "/worktree" })
  expect(calls[1].options.cwd).toBe("/worktree")
  expect(calls[2].options.cwd).toBe("/data/worktrees/project-1/work-1")
  expect(calls[3].options).toMatchObject({ cwd: "/data/worktrees/project-1/work-1", env: { CONCORD_SELECTED_PRODUCT_ID: "product-1", CONCORD_SELECTED_WORK_ID: "work-1" } })
  expect(JSON.parse(calls[3].input)).toMatchObject({ operation_id: "bootstrap-1", attempt_id: "bootstrap-1:launch", session_id: null, title: "concord-work-start-bootstrap-1" })
  expect(JSON.parse(calls[4].input)).toMatchObject({ operation_id: "bootstrap-1", attempt_id: "bootstrap-1:launch", session_id: "session-run-1", state: "running" })
  expect(JSON.parse(calls[6].input)).toMatchObject({ operation_id: "bootstrap-1", attempt_id: "bootstrap-1:launch", session_id: "session-run-1", model: "openai/gpt-5.6-luna", state: "completed" })
  expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", ...bootstrapArgs })
  expect(JSON.parse(calls[2].input)).toMatchObject({ product_id: "product-1", work_id: "work-1", task: bootstrapArgs.task, owner_pid: process.pid })
  expect(JSON.parse(calls[2].input).owner_start).toMatch(/^\d+$/)
  expect(result).toMatchObject({ outcome: "ok", product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: "/data/worktrees/project-1/work-1", session_id: "session-run-1", agent: "concord-implement", readback_agent: "concord-implement", readback_model: "openai/gpt-5.6-luna", output: "done" })
})

test("work_start rejects malformed core contracts and path mismatches before launch", async () => {
  for (const response of [{ work_id: "work-1" }, { ...bootstrapSuccess(), worktree: { ...bootstrapSuccess().worktree, path: "relative" } }]) {
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run() { calls++; return { exitCode: 0, stdout: JSON.stringify(calls === 1 ? contextResponse() : response), stderr: "" } } } })
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("malformed_response")
    expect(calls).toBe(2)
  }
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return calls === 2
      ? { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
      : { exitCode: 0, stdout: JSON.stringify({ ...launchContract(), directory: "/other-worktree" }), stderr: "" }
  } } })
  const mismatch: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(mismatch.outcome).toBe("partial")
  expect(mismatch.error.kind).toBe("malformed_response")
  expect(mismatch.error.recovery_action.kind).toBe("contact_operator")
   expect(calls).toBe(4)
})

test("work_start does not duplicate a launch owned by another invocation", async () => {
  const calls: string[][] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls.push(argv)
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify({ ...launchContract(), spawn_permitted: false }), stderr: "" }
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(calls).toHaveLength(3)
  expect(calls.some((argv) => argv[1] === "run" || argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start rolls back a stale owner that recorded no session", async () => {
  const calls: string[][] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls.push(argv)
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify({ ...launchContract(), spawn_permitted: false, rollback_permitted: true }), stderr: "" }
    if (argv[1] === "work-bootstrap-rollback") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0", state: "rolled_back" }), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.error.kind).toBe("cancelled")
  expect(calls.map((argv) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare", "work-bootstrap-rollback"])
})

test("work_start recovers a titled session after the fenced process ends", async () => {
  const calls: Array<{ argv: string[]; input: string }> = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string) {
    calls.push({ argv, input })
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify({ ...launchContract(), launch_state: "running", spawn_permitted: false, recovery_lookup_permitted: true }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([
      ...Array.from({ length: 300 }, (_, index) => ({ id: `session-${index}`, title: `other-${index}`, directory: "/data/worktrees/project-1/work-1" })),
      { id: "session-recovered", title: "concord-work-start-bootstrap-1", directory: "/data/worktrees/project-1/work-1" },
    ]), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: "{}", stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  const record = calls.find((call) => call.argv[1] === "session-record")
  expect(JSON.parse(record!.input)).toMatchObject({ session_id: "session-recovered", state: "running" })
  expect(calls.find((call) => call.argv[1] === "session")!.argv).toEqual(["opencode", "session", "list", "--format", "json"])
  expect(calls.some((call) => call.argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start rejects a partly malformed recovery list without rollback", async () => {
  const calls: string[][] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls.push(argv)
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify({ ...launchContract(), launch_state: "running", spawn_permitted: false, recovery_lookup_permitted: true }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "valid", title: "other", directory: "/tmp" }, { id: "missing-fields" }]), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(calls.some((argv) => argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start reports a partial effect for child failure and stops on abort", async () => {
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "work-bootstrap-rollback") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0", state: "rolled_back" }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: "[]", stderr: "" }
    expect(argv).toEqual(["concord", "session-exec"])
    return { exitCode: 7, stdout: "", stderr: "child failed" }
  } } })
  const partial: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(partial.outcome).toBe("partial")
  expect(partial.error.effect_state).toBe("partial")
  expect(partial.error.recovery_action.kind).toBe("contact_operator")

  const controller = new AbortController()
  controller.abort()
  adapter.configureConcordAdapter({ runner: { async run() { throw Object.assign(new Error("aborted"), { name: "AbortError" }) } } })
  const aborted: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor(() => {}, controller)))
  expect(aborted.outcome).toBe("error")
  expect(aborted.error.kind).toBe("cancelled")

  const activeController = new AbortController()
  const activeCalls: Array<{ argv: string[]; input: string }> = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], _input: string, signal: AbortSignal) {
    activeCalls.push({ argv, input: _input })
    if (activeCalls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (activeCalls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (activeCalls.length === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "session-after-abort", title: "concord-work-start-bootstrap-1", directory: "/data/worktrees/project-1/work-1" }]), stderr: "" }
    expect(argv).toEqual(["concord", "session-exec"])
    activeController.abort()
    expect(signal.aborted).toBe(true)
    throw Object.assign(new Error("active child stopped"), { name: "AbortError" })
  } } })
  const activeAbort: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor(() => {}, activeController)))
  expect(activeAbort.outcome).toBe("partial")
  expect(activeAbort.error.kind).toBe("cancelled")
  expect(activeAbort.error.recovery_action.kind).toBe("reconcile_operation")
  expect(activeCalls.filter((call) => call.argv[1] === "session-record").map((call) => JSON.parse(call.input).session_id)).toEqual(["session-after-abort"])
  expect(activeCalls.some((call) => call.argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start records a streamed session identity before the child returns", async () => {
  const calls: Array<{ argv: string[]; input: string }> = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string, _signal: AbortSignal, options?: ChildRunnerOptions) {
    calls.push({ argv, input })
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session-exec") {
      await options?.onStdoutLine?.(JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-streamed" }))
      throw new Error("child transport ended after session creation")
    }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("partial")
  const records = calls.filter((call) => call.argv[1] === "session-record").map((call) => JSON.parse(call.input))
  expect(records).toEqual([expect.objectContaining({ session_id: "session-streamed", state: "running" })])
  expect(calls.some((call) => call.argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start preserves streamed identity when its durable record fails", async () => {
  const calls: Array<{ argv: string[]; input: string }> = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string, _signal: AbortSignal, options?: ChildRunnerOptions) {
    calls.push({ argv, input })
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls.length === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls.length === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 1, stdout: "", stderr: "record unavailable" }
    if (argv[1] === "session-exec") {
      await options?.onStdoutLine?.(JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-record-failed" }))
      throw new Error("run should stop after the record failure")
    }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("partial")
  expect(result.error.kind).toBe("session_record_failure")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  const records = calls.filter((call) => call.argv[1] === "session-record").map((call) => JSON.parse(call.input))
  expect(records.map((record) => record.session_id)).toEqual(["session-record-failed"])
  expect(calls.some((call) => call.argv[1] === "work-bootstrap-rollback")).toBe(false)
})

test("work_start fails closed on session identity and bounded output violations", async () => {
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-exec") return { exitCode: 0, stdout: runOutput("session-run-1", "x".repeat(70_000)), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "session-run-1", title: "concord-work-start-bootstrap-1", directory: "/data/worktrees/project-1/work-1" }]), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: "{}", stderr: "" }
    return { exitCode: 0, stdout: exportOutput(), stderr: "" }
  } } })
  const oversized: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(oversized.error.kind).toBe("malformed_response")

  calls = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-exec") return { exitCode: 0, stdout: runOutput(), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: "{}", stderr: "" }
    return { exitCode: 0, stdout: exportOutput("session-run-1", "concord-research"), stderr: "" }
  } } })
  const mismatch: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(mismatch.error.kind).toBe("agent_identity_mismatch")
})

test("work_start derives one Project and Product from project-resolve before mutation", async () => {
  const previous = process.env.CONCORD_SELECTED_PRODUCT_ID
  const calls: string[][] = []
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  try {
    adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string) {
      calls.push(argv)
      expect(JSON.parse(input)).toEqual({ directory: "/worktree", worktree: "/worktree" })
      return { exitCode: 0, stdout: JSON.stringify(contextResponse(true, ["product-1", "product-2"])), stderr: "" }
    } } })
    const ambiguous: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(ambiguous.error.kind).toBe("invalid_input")
    expect(calls).toEqual([["concord", "project-resolve"]])
  } finally {
    if (previous === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previous
  }

  const linkedCalls: string[][] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    linkedCalls.push(argv)
    return { exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }
  } } })
  const linked: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(linked.error.kind).toBe("invalid_input")
  expect(linked.error.message).toContain("default checkout")
  expect(linkedCalls).toEqual([["concord", "project-resolve"]])

  const explicitTarget = { ...bootstrapArgs, project_id: "other-project" }
  const targetCalls: string[][] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) { targetCalls.push(argv); throw new Error("mutation must not run") } } })
  const rejectedTarget: any = await rawHostResult(adapter.work_start.execute(explicitTarget as any, contextFor()))
  expect(rejectedTarget.error.kind).toBe("invalid_input")
  expect(targetCalls).toEqual([])
})

test("work_start enforces UTF-8 byte limits for short fields and task input", async () => {
  for (const invalid of [{ ...bootstrapArgs, title: "é".repeat(129) }, { ...bootstrapArgs, task: "🙂".repeat(2049) }]) {
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("core must not run") } } })
    const result: any = await rawHostResult(adapter.work_start.execute(invalid as any, contextFor()))
    expect(result.error.kind).toBe("invalid_input")
    expect(calls).toBe(0)
  }
  const validTask = { ...bootstrapArgs, task: "🙂".repeat(2048) }
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], _input: string) {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session-exec") return { exitCode: 0, stdout: runOutput(), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportOutput(), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute(validTask as any, contextFor()))
  expect(result.outcome).toBe("ok")
})

test("work_start exact replay resumes the durable session", async () => {
  const calls: Array<{ argv: string[]; input: string }> = []
  let prepares = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string) {
    calls.push({ argv, input })
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "work-bootstrap") return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (argv[1] === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(launchContract("concord-implement", "Implement the task.", prepares++ > 0 ? "session-run-1" : null)), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session-exec") return { exitCode: 0, stdout: runOutput("session-run-1"), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportOutput("session-run-1"), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  const first: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  const second: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(first.work_id, JSON.stringify({ first, second, calls })).toBe(second.work_id)
  expect(calls.filter((call) => call.argv.join(" ") === "concord work-bootstrap")).toHaveLength(2)
  expect(calls.filter((call) => call.argv.join(" ") === "concord session-prepare")).toHaveLength(2)
  expect(calls.filter((call) => call.argv.join(" ") === "concord session-record")).toHaveLength(3)
  expect(JSON.parse(calls.filter((call) => call.argv[1] === "session-exec")[1].input).session_id).toBe("session-run-1")
})

test("work_start integrates with a real Concord binary and a fake OpenCode child", async () => {
  const root = fileURLToPath(new URL("../../", import.meta.url))
  const fixture = await mkdtemp(join(tmpdir(), "concord-work-start-"))
  const project = join(fixture, "project")
  const home = join(fixture, "home")
  const hostAgents = join(home, ".config", "opencode", "agents")
  const bin = join(fixture, "bin")
  const concordBinary = join(bin, "concord")
  const adapterConcord = join(bin, "concord-adapter")
  const fakeOpenCode = join(bin, "opencode")
  const database = join(fixture, "authority.db")
  const observed = join(fixture, "child-observed.json")
  const pdeathMarker = join(fixture, "pdeath-child.pid")
  const pdeathProbe = join(fixture, "pdeath-probe.ts")
  const previous = {
    concord: process.env.CONCORD_BIN,
    opencode: process.env.OPENCODE_BIN,
    database: process.env.CONCORD_DB_PATH,
    home: process.env.HOME,
    path: process.env.PATH,
    selected: process.env.CONCORD_SELECTED_PRODUCT_ID,
  }
  const restore = (name: string, value: string | undefined) => value === undefined ? delete process.env[name] : (process.env[name] = value)
  const run = async (argv: string[], input = "", env: Record<string, string> = {}, cwd?: string) => {
    const child = Bun.spawn(argv, { stdin: "pipe", stdout: "pipe", stderr: "pipe", env: { ...process.env, ...env } as Record<string, string>, ...(cwd ? { cwd } : {}) })
    await child.stdin.write(input)
    await child.stdin.end()
    const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    if (exitCode !== 0) throw new Error(`${argv.join(" ")} failed: ${stderr || stdout}`)
    return { stdout, stderr }
  }
  try {
    await mkdir(project, { recursive: true })
    await mkdir(hostAgents, { recursive: true })
    await mkdir(bin, { recursive: true })
    await run(["git", "init", "--quiet", "-b", "main", project])
    await run(["git", "-C", project, "config", "user.email", "test@example.invalid"])
    await run(["git", "-C", project, "config", "user.name", "Concord Test"])
    await Bun.write(join(project, "README.md"), "fixture\n")
    await run(["git", "-C", project, "add", "README.md"])
    await run(["git", "-C", project, "commit", "--quiet", "-m", "fixture"])
    await run(["git", "-C", project, "update-ref", "refs/remotes/origin/main", "HEAD"])
    await run(["git", "-C", project, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"])
    await Bun.write(join(hostAgents, "concord-orchestrator.md"), "---\nmode: all\n---\n")
    for (const lane of ["research", "implement", "design", "review", "verify"]) await Bun.write(join(hostAgents, `concord-${lane}.md`), "---\nmode: all\n---\n")
    await Bun.write(fakeOpenCode, `#!/usr/bin/env bun
const args = Bun.argv.slice(2)
if (args[0] === "debug" && args[1] === "config") {
  console.log(JSON.stringify({ agent: { "concord-orchestrator": { mode: "all" } } }))
} else if (args[0] === "run") {
  if (process.env.CONCORD_PDEATH_MARKER) {
    await Bun.write(process.env.CONCORD_PDEATH_MARKER, String(process.pid))
    await Bun.sleep(60_000)
    process.exit(9)
  }
  await Bun.write(${JSON.stringify(observed)}, JSON.stringify({ cwd: process.cwd(), args, product_id: process.env.CONCORD_SELECTED_PRODUCT_ID, work_id: process.env.CONCORD_SELECTED_WORK_ID }))
  const sessionID = "session-integration"
  console.log(JSON.stringify({ type: "step_start", timestamp: 1, sessionID }))
  console.log(JSON.stringify({ type: "text", timestamp: 2, sessionID, part: { type: "text", text: "integration complete" } }))
  console.log(JSON.stringify({ type: "step_finish", timestamp: 3, sessionID, part: { type: "step-finish", reason: "stop" } }))
} else if (args[0] === "export") {
  console.log(JSON.stringify({ info: { id: args[1] }, messages: [{ info: { id: "message-integration", sessionID: args[1], role: "assistant", agent: "concord-orchestrator", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }] }))
} else if (args[0] === "session" && args[1] === "list") {
  console.log("[]")
}
    `)
    await chmod(fakeOpenCode, 0o755)
    await run(["go", "build", "-o", concordBinary, "./cmd/concord"], "", {}, root)
    await Bun.write(adapterConcord, `#!/bin/sh
exec "${concordBinary}" "$@"
`)
    await chmod(adapterConcord, 0o755)
    const cliEnv = { CONCORD_DB_PATH: database, HOME: home, PATH: `${bin}:${process.env.PATH ?? ""}` }
    await run([concordBinary, "product-create"], JSON.stringify({ product_id: "product-integration", display_name: "Integration product", stage_maturity: "prototype", stage_audience_commitment: "operator_only", project_id: "project-integration", project_display_name: "Integration project", role: "primary" }), cliEnv)
    await run([concordBinary, "project-locator-add"], JSON.stringify({ project_id: "project-integration", locator_id: "locator-integration", kind: "canonical_path", value: project, expected_version: 1 }), cliEnv)
    await run([concordBinary, "project-resolve"], JSON.stringify({ directory: project, worktree: project }), cliEnv)
    process.env.CONCORD_BIN = adapterConcord
    process.env.OPENCODE_BIN = fakeOpenCode
    process.env.CONCORD_DB_PATH = database
    process.env.HOME = home
    process.env.PATH = `${bin}:${previous.path ?? ""}`
    delete process.env.CONCORD_SELECTED_PRODUCT_ID
    adapter.configureConcordAdapter({ reset: true })
    const integrationArgs: any = { ...bootstrapArgs, tags: [], governing_requirements: [], workflow_type_ref: "workflow.implementation" }
    const result: any = await rawHostResult(adapter.work_start.execute(integrationArgs, contextFor(() => {}, new AbortController(), project)))
    expect(result.outcome, JSON.stringify(result)).toBe("ok")
    expect(await Bun.file(observed).exists()).toBe(true)
    const child = JSON.parse(await Bun.file(observed).text())
    expect(child.cwd).toBe(result.worktree_path)
    expect(child.args).toContain("--dir")
    expect(child.args[child.args.indexOf("--dir") + 1]).toBe(result.worktree_path)
    expect(child.product_id).toBe("product-integration")
    expect(child.work_id).toBe(result.work_id)
    expect((await run(["git", "-C", project, "branch", "--show-current"])).stdout.trim()).toBe("main")
    expect((await run(["git", "-C", project, "remote"])).stdout.trim()).toBe("")
    const worktrees = (await run(["git", "-C", project, "worktree", "list", "--porcelain"])).stdout
    expect(worktrees.split("\n").filter((line: string) => line.startsWith("worktree "))).toHaveLength(2)
    expect(worktrees).toContain(`worktree ${result.worktree_path}`)

    const pdeathArgs = { ...integrationArgs, idempotency_key: "start-parent-death", external_ref: "issue-parent-death" }
    await Bun.write(pdeathProbe, `
import * as adapter from ${JSON.stringify(join(root, "adapter/opencode/concord.ts"))}
const controller = new AbortController()
await adapter.work_start.execute(${JSON.stringify(pdeathArgs)}, {
  sessionID: "session-parent-death",
  messageID: "message-parent-death",
  agent: "concord-orchestrator",
  worktree: ${JSON.stringify(project)},
  directory: ${JSON.stringify(project)},
  abort: controller.signal,
  metadata() {},
  async ask() {},
})
`)
    const probe = Bun.spawn(["bun", pdeathProbe], { cwd: project, stdout: "pipe", stderr: "pipe", env: { ...process.env, CONCORD_PDEATH_MARKER: pdeathMarker } as Record<string, string> })
    for (let index = 0; index < 100 && !(await Bun.file(pdeathMarker).exists()); index++) await Bun.sleep(20)
    expect(await Bun.file(pdeathMarker).exists()).toBe(true)
    const launchedPID = Number(await Bun.file(pdeathMarker).text())
    expect(Number.isInteger(launchedPID)).toBe(true)
    probe.kill("SIGKILL")
    await probe.exited
    let childAlive = true
    for (let index = 0; index < 100 && childAlive; index++) {
      try { process.kill(launchedPID, 0) } catch { childAlive = false }
      if (childAlive) await Bun.sleep(20)
    }
    expect(childAlive).toBe(false)
    delete process.env.CONCORD_PDEATH_MARKER
    const recovered: any = await rawHostResult(adapter.work_start.execute(pdeathArgs, contextFor(() => {}, new AbortController(), project)))
    expect(recovered.error.kind).toBe("cancelled")
    expect(recovered.error.message).toContain("rolled back")
  } finally {
    restore("CONCORD_BIN", previous.concord)
    restore("OPENCODE_BIN", previous.opencode)
    restore("CONCORD_DB_PATH", previous.database)
    restore("HOME", previous.home)
    restore("PATH", previous.path)
    restore("CONCORD_SELECTED_PRODUCT_ID", previous.selected)
    adapter.configureConcordAdapter({ reset: true })
    await rm(fixture, { recursive: true, force: true })
  }
}, 120_000)
