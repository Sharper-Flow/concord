import { test, expect, mock } from "bun:test"
import { chmod, mkdir, mkdtemp, rm } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope, envelopeFailurePath } from "./generated-contract-tests"
import type { ChildRunnerOptions } from "./concord"
import { hostControlPlane, SESSION_LIST_ROUTE, SHOW_TOAST_ROUTE } from "./move-session"

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

test("unknown-effect mutation errors do not expose failed response data", async () => {
  const committed = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [], next_valid_intents: [], work_id: "work-1", unexpected_field: true,
  })
  const invalidResponse: any = await runTransition(runnerWithContext(committed))
  expect(invalidResponse.error.kind).toBe("operation_conflict")
  expect(invalidResponse.error.effect_state).toBe("possible")
  expect(invalidResponse.error.details.salvaged).toMatchObject({ work_id: "work-1", changed_refs: [] })

  const malformedResponse: any = await runTransition(runnerWithContext({ exitCode: 0, stdout: "not-json", stderr: "" }))
  expect(malformedResponse.error.kind).toBe("malformed_response")
  expect(malformedResponse.error.effect_state).toBe("possible")
  expect(malformedResponse.error.details.reconcile_attempted).toBe(true)
})

// The #694/#701 drift shape: a member the envelope law does not declare,
// nested inside a schema branch. The refusal names the member, so the operator
// reads which field failed instead of bisecting the envelope by hand.
test("a contract-failing mutation names the offending member", async () => {
  const drifted = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [], next_valid_intents: [],
    resolved_scope: { product_id: "product-1", project_ids: ["project-1"], scope_version: "v1", work_ids: [], product_ids: ["product-1"] },
  })
  expect(validateGeneratedEnvelope(drifted)).toBe(false)
  expect(envelopeFailurePath(drifted)).toBe("resolved_scope.product_ids")
  const result: any = await runTransition(runnerWithContext(drifted))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.adapter_reason).toBe("unknown_effect")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.message).toContain("member resolved_scope.product_ids failed the generated envelope contract")
})

test("strict response failures salvage bounded entity identifiers", async () => {
  const committed = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [{ entity_kind: "work", id: "work-1", version: "2" }],
    next_valid_intents: [], work_id: "work-1", worktree_path: "/worktrees/work-1", unexpected_field: true,
  })
  const result: any = await runTransition(runnerWithContext(committed))
  assertAdapterEnvelope(result)
  expect(result.error.details.salvaged).toEqual({
    work_id: "work-1",
    worktree_path: "/worktrees/work-1",
    changed_refs: [{ entity_kind: "work", id: "work-1", version: "2" }],
  })
  expect(result.outcome).toBe("error")
})

test("possible mutation failures reconcile by the request work ID", async () => {
  const committed = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [], next_valid_intents: [], work_id: "work-1", unexpected_field: true,
  })
  const readback = coreEnvelope("concord_work_browse", "list", "ok", {
    result: { items: [{ id: "work-1", kind: "task", title: "Task", lifecycle: "completed", version: 3 }] },
  })
  let calls = 0
  let reconciliationInput: any
  const result: any = await runTransition({ async run(_argv: string[], input: string) {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 4) reconciliationInput = JSON.parse(input).input
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? committed : readback), stderr: "" }
  } })
  expect(calls).toBe(4)
  expect(reconciliationInput.work_ids).toEqual(["work-1"])
  expect(result.error.details.reconciled).toEqual({ found: true, lifecycle: "completed", version: 3 })
})

test("post-approval possible failures use the same reconciliation wrapper", async () => {
  const invalid = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [], next_valid_intents: [], unexpected_field: true,
  })
  const readback = coreEnvelope("concord_work_browse", "list", "ok", {
    result: { items: [] },
  })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 4) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 3 ? invalid : readback), stderr: "" }
  } })
  expect(calls).toBe(5)
  expect(result.error.details.reconciled).toEqual({ found: false, lifecycle: null, version: null })
})

test("transport failures reconcile the request work ID", async () => {
  const readback = coreEnvelope("concord_work_browse", "list", "ok", {
    result: { items: [{ id: "work-1", kind: "task", title: "Task", lifecycle: "in_progress", version: 2 }] },
  })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return calls === 2 ? { exitCode: 1, stdout: "", stderr: "broken pipe" } : { exitCode: 0, stdout: JSON.stringify(readback), stderr: "" }
  } })
  expect(calls).toBe(4)
  expect(result.error.details.reconciled).toEqual({ found: true, lifecycle: "in_progress", version: 2 })
  expect(result.error.details.salvaged).toBeUndefined()
})

test("post-approval runner failures reconcile the request work ID", async () => {
  const readback = coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 4) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }
    if (calls === 3) throw new Error("runner failed after approval")
    return { exitCode: 0, stdout: JSON.stringify(readback), stderr: "" }
  } })
  expect(calls).toBe(5)
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(result.error.details.reconciled).toEqual({ found: false, lifecycle: null, version: null })
})

test("oversized mutation envelopes reconcile the request work ID", async () => {
  const oversized = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
    evidence_refs: Array.from({ length: 32 }, (_, index) => ({
      kind: "artifact", authority: "core", locator_kind: "id", locator: `${index}-${"x".repeat(2040)}`,
    })),
  })
  expect(validateGeneratedEnvelope(oversized)).toBe(true)
  const readback = coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? oversized : readback), stderr: "" }
  } })
  expect(calls).toBe(4)
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.details.reconciled).toEqual({ found: false, lifecycle: null, version: null })
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

// mutations.go builds every workflow_action challenge with
// `"selected_choice": in.SelectedChoice`. That field is a Go string, so an
// action carrying no selection serializes it as "" rather than omitting it.
// The tool surface admits the field only on confirm_premise, so a correct
// caller sends nothing at all. A challenge check that compares the two values
// refuses every approval-gated action except confirm_premise, and
// approve_contract is the planning checkpoint in all seven workflows.
//
// These tests drive the whole challenge rather than asserting on the field
// list, because the field list passed while the comparison still refused.
const workflowActionChallenge = (actionID: string, selectedChoice = "") => ({
  ...approvalChallenge(), operation: "workflow_action",
  error: {
    kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
    details: {
      approval_ref: "challenge-1", operation_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      scope: ["product:product-1", "project:project-1", "work:work-1"], versions: ["work:2"],
      work_id: "work-1", action_id: actionID, contract_version: "3",
      selected_choice: selectedChoice, premise_summary: "approve the exact workflow action",
      decision_context_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    },
  },
})
const runWorkflowAction = (runner: any, input: Record<string, unknown>, ask?: () => Promise<void>) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", input), contextFor(ask)))
}
const challengeRunner = (challenge: unknown) => {
  let calls = 0
  return { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(challenge), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify({ ...approvalSuccess(), operation: "workflow_action" }), stderr: "" }
  } }
}

test("approve_contract clears the approval challenge the core actually builds", async () => {
  let approvals = 0
  const result: any = await runWorkflowAction(
    challengeRunner(workflowActionChallenge("approve_contract")),
    { work_id: "work-1", expected_version: 2, action_id: "approve_contract", idempotency_key: "idem-1" },
    async () => { approvals++ },
  )
  expect(result.outcome).toBe("ok")
  expect(approvals).toBe(1)
})

test("confirm_premise still binds the selection it carries", async () => {
  // The one action whose schema admits a selection must still agree with the
  // core, or an operator could approve a choice they did not make.
  const mismatched: any = await runWorkflowAction(
    challengeRunner(workflowActionChallenge("confirm_premise", "revise")),
    { work_id: "work-1", expected_version: 2, action_id: "confirm_premise", selected_choice: "confirm", decision_context_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", idempotency_key: "idem-2" },
    async () => {},
  )
  expect(mismatched.outcome).not.toBe("ok")
  expect(mismatched.error.adapter_reason).toBe("malformed_core_response")

  let approvals = 0
  const agreed: any = await runWorkflowAction(
    challengeRunner(workflowActionChallenge("confirm_premise", "confirm")),
    { work_id: "work-1", expected_version: 2, action_id: "confirm_premise", selected_choice: "confirm", decision_context_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", idempotency_key: "idem-3" },
    async () => { approvals++ },
  )
  expect(agreed.outcome).toBe("ok")
  expect(approvals).toBe(1)
})

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
    expect(result.error.kind).toBe(name === "ok" ? "malformed_response" : "operation_conflict")
    expect(result.error.adapter_reason).toBe(name === "ok" ? "malformed_core_response" : "unknown_effect")
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

const emitRunOutput = async (options: ChildRunnerOptions | undefined, output: string) => {
  for (const line of output.split("\n")) await options?.onStdoutLine?.(line)
}

const exportOutput = (sessionID = "session-run-1", agent = "concord-implement") => JSON.stringify({
  info: { id: sessionID },
  messages: [{ info: { id: "message-1", sessionID, role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }],
})

test("work_start rejects malformed core contracts and path mismatches before launch", async () => {
  // The subject is the core contract, so the host answers reachable: work start
  // probes the control plane before it captures anything, and an unreachable
  // host would refuse ahead of the contract this test is about.
  bindRetargetRoute()
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

test("child stdout keeps a bounded tail while retaining run decisions", async () => {
  const source = [
    JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-tail" }),
    ...Array.from({ length: 20_000 }, () => "host filler"),
    JSON.stringify({ type: "text", timestamp: 2, sessionID: "session-tail", part: { type: "text", text: "tail" } }),
    JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-tail", part: { type: "step-finish", reason: "stop" } }),
  ].join("\n")
  expect(Buffer.byteLength(source)).toBeGreaterThan(65_536)
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (let offset = 0; offset < source.length; offset += 1_024) controller.enqueue(encoder.encode(source.slice(offset, offset + 1_024)))
      controller.close()
    },
  })
  const captured = await adapter.readChildStdout(stream)
  expect(Buffer.byteLength(captured.stdout)).toBeLessThanOrEqual(65_536)
  expect(captured.runSessionObservation.sessions).toEqual(new Set(["session-tail"]))
  expect(captured.runSessionObservation.officialEvents).toBe(3)
  expect(captured.runSessionObservation.completed).toBe(true)
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
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], _input: string, _signal: AbortSignal, options?: ChildRunnerOptions) {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (calls === 3) return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (argv[1] === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (argv[1] === "session-exec") { await emitRunOutput(options, runOutput()); return { exitCode: 0, stdout: runOutput(), stderr: "" } }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportOutput(), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  bindRetargetRoute()
  const calls2: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls2) })
  const result: any = await rawHostResult(adapter.work_start.execute(validTask as any, contextFor()))
  expect(result.outcome).toBe("ok")
})

// CD-0098 D1/D2/D3/D4. Work start moves the calling session to the worktree it
// claimed. These tests pin the route's contract rather than a launch's: the
// claim is recorded before the move, an absent route refuses with no fallback,
// and success is refused unless the host reports the session in the claimed
// worktree.
const WORKTREE = "/data/worktrees/project-1/work-1"

type RetargetCall = { argv: string[]; input: string; options?: any }

const retargetRunner = (calls: RetargetCall[], overrides: Record<string, () => { exitCode: number; stdout: string; stderr: string }> = {}) => ({
  async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
    calls.push({ argv, input, options })
    const command = argv[1]
    if (overrides[command]) return overrides[command]()
    if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (command === "work-bootstrap") return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(launchContract()), stderr: "" }
    if (command === "session-record") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    if (command === "work-bootstrap-rollback") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0" }), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

// bindRetargetRoute stands in for the host's control plane. A test drives the
// route's answers directly, so the contract is exercised without a server.
const bindRetargetRoute = (options: { moveStatus?: number; moveBody?: string; landedDirectory?: string; unbound?: boolean } = {}) => {
  const moved: Array<Record<string, unknown>> = []
  if (options.unbound) {
    hostControlPlane().bind(undefined)
    return moved
  }
  hostControlPlane().bind({
    post: async ({ body }) => {
      moved.push(body as Record<string, unknown>)
      const status = options.moveStatus ?? 204
      return { response: new Response(status === 204 ? null : (options.moveBody ?? ""), { status }) }
    },
    get: async () => ({
      data: { id: "session-1", directory: options.landedDirectory ?? WORKTREE },
      response: new Response(null, { status: 200 }),
    }),
  })
  return moved
}

test("work start retargets the calling session and records the claim before the move", async () => {
  const moved = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1", agent: "concord-implement" })
  // The claim is recorded before the session moves, so a failed move leaves a
  // resumable claim rather than a moved session with none.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare", "session-record"])
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  expect(JSON.parse(calls[3].input)).toMatchObject({ session_id: "session-1", state: "completed" })
  // No launch: the adapter never spawns a host session for the work.
  expect(calls.some(({ argv }) => argv[1] === "session-exec" || argv[0] === "opencode")).toBe(false)
})

// A host that serves no control plane cannot retarget, and CD-0098 D2 leaves
// no other route into the worktree. The refusal therefore belongs in front of
// the capture: an operator who cannot start work should not be left holding a
// work item and a claim to reconcile as well.
test("work start refuses before any effect when the host handed the plugin no client", async () => {
  bindRetargetRoute({ unbound: true })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).not.toBe("ok")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("this host handed the plugin no client")
  expect(result.error.message).toContain("host version")
  // The remedy travels with the refusal, because the condition is one the
  // operator repairs by starting the session differently. It must not name a
  // separate server: CD-0098 D2 takes the transport from the client the plugin
  // factory hands the adapter, so `opencode serve` repairs nothing here.
  expect(result.error.message).toContain("restart this session on an OpenCode build")
  expect(result.error.message).not.toContain("opencode serve")
  expect(result.error.message).not.toContain("opencode attach")
  // Nothing was captured, so there is nothing to roll back and nothing to
  // resume: the probe ran before work-bootstrap.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
  expect(result.work_id).toBeUndefined()
})

test("work start refuses before any effect when the host control plane cannot be reached", async () => {
  const unreachable = async () => {
    throw new Error("Unable to connect. Is the computer able to access the url?")
  }
  hostControlPlane().bind({ get: unreachable, post: unreachable })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).not.toBe("ok")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("the host control plane is unreachable")
  expect(result.error.message).toContain("restart this session on an OpenCode build")
  expect(result.error.message).not.toContain("opencode serve")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
})

test("work start refuses a move the host rejects", async () => {
  bindRetargetRoute({ moveStatus: 400, moveBody: JSON.stringify({ name: "MoveSessionError", data: { message: "destination project mismatch" } }) })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).not.toBe("ok")
  expect(result.error.message).toContain("destination project mismatch")
})

test("work start verifies the session directory after the move", async () => {
  bindRetargetRoute({ landedDirectory: "/somewhere/else" })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).not.toBe("ok")
  expect(result.error.message).toContain("/somewhere/else")
  // The refused retarget is recorded as a failure rather than left silent.
  expect(JSON.parse(calls[3].input)).toMatchObject({ state: "failed" })
})

// Issue #741. A record failure leaves the capture and the claim behind, and
// reconcile reads only terminal work, so naming it stranded the operator with
// a partial no typed operation could clear. The replay resumes the same
// operation under the same key.
test("work start sends no model and offers exact replay when the record fails", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(calls, {
      "session-record": () => ({ exitCode: 1, stdout: "", stderr: "concord session-record: store is not open" }),
    }),
  })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("partial")
  expect(result.error.recovery_action.kind).toBe("exact_replay")
  expect(result.error.message).toContain("idempotency_key")
  // CD-0098 retargets this session, so no child reports a model and none is
  // claimed. A model the adapter cannot observe is not sent.
  expect(JSON.parse(calls[3].input)).not.toHaveProperty("model")
})

// Issue #722: a worktree removal is safe only when no live session runs in the
// directory it deletes. The store owns the worktree path and refuses on it;
// the adapter owns the only truthful answer to which sessions are live and
// where, because no event records a session leaving a directory.
const bindSessionRoutes = (options: { sessions?: unknown; listStatus?: number; unbound?: boolean; toastStatus?: number } = {}) => {
  const toasts: Array<Record<string, unknown>> = []
  if (options.unbound) {
    hostControlPlane().bind(undefined)
    return toasts
  }
  hostControlPlane().bind({
    post: async ({ url, body }) => {
      if (url === SHOW_TOAST_ROUTE) {
        toasts.push(body as Record<string, unknown>)
        return { response: new Response("true", { status: options.toastStatus ?? 200 }) }
      }
      return { response: new Response(null, { status: 404 }) }
    },
    get: async ({ url }) => {
      if (url !== SESSION_LIST_ROUTE) return { response: new Response(null, { status: 404 }) }
      const status = options.listStatus ?? 200
      if (status !== 200) return { response: new Response("host is unwell", { status }) }
      return { data: options.sessions ?? [], response: new Response(null, { status }) }
    },
  })
  return toasts
}

const removalRequest = (operation: string) => hostCall(operation, {
  work_id: "work-1", project_id: "project-1", expected_version: 2, idempotency_key: "remove-1",
})

// A removal is a mutation, so an ok core answer carries the result, the
// changed refs, and the next intents the generated envelope contract requires.
const removalOk = (operation: string) => coreEnvelope("concord_work_transition", operation, "ok", {
  // The payload contract counts a version; the envelope carries it as a string.
  result: { changed_refs: [{ entity_kind: "work_item", id: "work-1", version: 3 }], next_valid_intents: [] },
  changed_refs: [{ entity_kind: "work_item", id: "work-1", version: "3" }],
  next_valid_intents: [],
})

test("a worktree removal carries the host's live session directories to the core", async () => {
  for (const operation of ["worktree_reclaim", "worktree_destroy"]) {
    bindSessionRoutes({ sessions: [
      { id: "ses_alpha", directory: "/worktrees/work-1" },
      { id: "ses_beta", directory: "/elsewhere" },
    ] })
    const seen: string[] = []
    adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
      seen.push(input)
      return removalOk(operation)
    }) })
    const envelope: any = await rawHostResult(adapter.work_transition.execute(removalRequest(operation), contextFor()))
    expect(envelope.outcome, operation).toBe("ok")
    expect(JSON.parse(seen[0]).input.observed_session_directories, operation).toEqual([
      { session_ref: "ses_alpha", directory: "/worktrees/work-1" },
      { session_ref: "ses_beta", directory: "/elsewhere" },
    ])
  }
})

test("a worktree removal refuses when the host session list cannot be read", async () => {
  // "No session occupies this worktree" and "I could not look" are different
  // answers. Only one of them makes a removal safe, so an unreadable host
  // refuses rather than reporting an empty list.
  for (const options of [{ unbound: true }, { listStatus: 500 }, { sessions: { not: "an array" } }, { sessions: [{ id: "ses_alpha" }] }]) {
    bindSessionRoutes(options)
    let coreCalls = 0
    adapter.configureConcordAdapter({ runner: runnerWithContext(() => {
      coreCalls++
      return removalOk("worktree_reclaim")
    }) })
    const envelope: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
    assertAdapterEnvelope(envelope)
    expect(envelope.error.adapter_reason, JSON.stringify(options)).toBe("session_occupancy_unreadable")
    expect(envelope.error.effect_state).toBe("none")
    expect(envelope.error.message).toContain("Nothing was removed")
    expect(coreCalls, JSON.stringify(options)).toBe(0)
  }
})

test("a completed worktree removal reports itself to the operator", async () => {
  const toasts = bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/elsewhere" }] })
  adapter.configureConcordAdapter({ runner: runnerWithContext(removalOk("worktree_reclaim")) })
  const envelope: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(envelope.outcome).toBe("ok")
  expect(toasts).toHaveLength(1)
  expect(toasts[0]).toMatchObject({ variant: "info" })
  expect(String(toasts[0].message)).toContain("work-1")
})

test("a refused worktree removal reports nothing, and a failed toast does not fail the removal", async () => {
  // Your rule: an unsafe removal does not happen, and needs no notice because
  // nothing was lost. A notice for a removal that did not happen would be a
  // lie, and a host with no attached TUI must not turn a completed removal
  // into a failure.
  const refused = bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/elsewhere" }] })
  adapter.configureConcordAdapter({ runner: runnerWithContext(coreEnvelope(
    "concord_work_transition", "worktree_reclaim", "error",
    { error: { kind: "worktree_ownership_conflict", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" } },
  )) })
  const refusal: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(refusal.outcome).toBe("error")
  expect(refused).toHaveLength(0)

  bindSessionRoutes({ sessions: [], toastStatus: 500 })
  adapter.configureConcordAdapter({ runner: runnerWithContext(removalOk("worktree_reclaim")) })
  const delivered: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(delivered.outcome).toBe("ok")
})
