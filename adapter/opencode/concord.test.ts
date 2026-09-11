import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { chmod, mkdir, mkdtemp, rm } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, hostToolSchemas, manifestDigest } from "./generated-contracts"
import { configureCoreBinary } from "./dispatch"
import { claimHostLease, configureHostLease } from "./host-lease"
import { validateGeneratedEnvelope, envelopeFailurePath } from "./generated-contract-tests"
import { hostControlPlane, SESSION_LIST_ROUTE, SESSION_ROUTE, SHOW_TOAST_ROUTE } from "./move-session"

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
const askingSource = await Bun.file(new URL("../../instructions/asking.md", import.meta.url)).text()
const continuationSource = await Bun.file(new URL("../../instructions/continuation.md", import.meta.url)).text()
// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

const adapter = await import("./concord")

// A work_start success renames the zellij pane frame when the host exports
// ZELLIJ_PANE_ID (issue #917). The suite stays hermetic against the operator's
// own zellij session: no test sees a pane id unless it sets one.
const outerPaneID = process.env.ZELLIJ_PANE_ID
beforeEach(() => {
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  delete process.env.ZELLIJ_PANE_ID
})
afterEach(() => {
  if (outerPaneID === undefined) delete process.env.ZELLIJ_PANE_ID
  else process.env.ZELLIJ_PANE_ID = outerPaneID
})
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
  const transition = adapter.publishedRequestSchema("concord_work_transition") as any
  const workflowAction = transition.oneOf.find((variant: any) => variant.properties.operation.const === "workflow_action")
  expect(workflowAction.properties.input.$ref).toContain("work_transition_action_public_input")
  // Every generated field reaches the host. The definition hook makes the
  // published fields optional; the adapter enforces the closed modes.
  expect(Object.keys((adapter.work_start as any).args).sort()).toEqual(["title", "value_statement", "kind", "task", "idempotency_key", "priority", "urgency", "tags", "workflow_type_ref", "external_ref", "governing_requirements", "ref", "work_id"].sort())
  for (const value of Object.values((adapter.work_start as any).args)) expect(value).toBeObject()
  expect((adapter.work_start as any).args.product_id).toBeUndefined()
  expect((adapter.work_start as any).args.project_id).toBeUndefined()
})

test("published tool schemas type every enum node", () => {
  // Moonshot's flavored tool-schema validator refuses enum nodes without an
  // explicit type, so the published surface must type each one.
  const untyped: string[] = []
  const walk = (value: unknown, path: string, seen: Set<unknown>) => {
    if (typeof value !== "object" || value === null || seen.has(value)) return
    seen.add(value)
    if (Array.isArray(value)) {
      value.forEach((item, index) => walk(item, `${path}[${index}]`, seen))
      return
    }
    for (const [key, item] of Object.entries(value)) {
      if (key === "enum" && !("type" in (value as Record<string, unknown>))) untyped.push(path)
      walk(item, `${path}.${key}`, seen)
    }
  }
  for (const toolName of ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_work_define", "concord_domain", "concord_work_initiative", "concord_work_transition", "concord_work_relate", "concord_work_compact"]) {
    walk(adapter.publishedRequestSchema(toolName), toolName, new Set())
  }
  const workStartDefinition = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  void adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
  walk(workStartDefinition.jsonSchema, "concord_work_start", new Set())
  expect(untyped).toEqual([])
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

test("request-wrapped tools accept the Code Mode double-wrapped argument shape", async () => {
  let invokeStdin = ""
  const core = coreEnvelope("concord_work_browse", "list", "error", {
    error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
  })
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    invokeStdin = input
    return core
  }) })
  // opencode 1.18.30's Code Mode bridge delivers the published arguments to
  // plugin tool execute wrapped one extra time under the schema's own
  // `request` property. Flat-schema work_start is unaffected.
  const hostResult: any = await adapter.work_browse.execute({ request: { request: { operation: "list", input: {} } } }, contextFor())
  const envelope = JSON.parse(hostResult.output)
  expect(envelope.origin).toBe("core")
  expect(envelope.outcome).toBe("error")
  const sent = JSON.parse(invokeStdin)
  expect(sent.operation).toBe("list")
  expect(sent.input).toEqual({})
  expect(sent.tool).toBe("concord_work_browse")
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
  expect(source).toContain("concordBinaryPath(), \"invoke\"")
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

test("project resolution and envelopes use the live session directory", async () => {
  const requests: any[] = []
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: "/moved" }, response: new Response(null, { status: 200 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  adapter.configureConcordAdapter({ runner: {
    async run(_argv: string[], input: string) {
      requests.push(JSON.parse(input))
      if (requests.length === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } })), stderr: "" }
    },
  } })
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor(() => {}, undefined, "/stale", "/stale")))
  expect(result.outcome).toBe("ok")
  expect(requests[0]).toEqual({ directory: "/moved", worktree: "/moved" })
  expect(requests[1].call_envelope.directory).toBe("/moved")
  expect(requests[1].call_envelope.worktree).toBe("/moved")
  expect(JSON.stringify(requests[1])).not.toContain("/stale")
})

test("an unreadable session directory refuses before core transport", async () => {
  hostControlPlane().bind(undefined)
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("core transport must not run") } } })
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("transport_failure")
  expect(result.error.adapter_reason).toBe("session_directory_unreadable")
  expect(calls).toBe(0)
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
  // CD-0111 D4: the remedy names the operator and both digests, never a
  // session restart.
  expect(result.error.message).toContain("contact the operator with both digests")
  expect(result.error.message).toContain("sha256:" + "0".repeat(63) + "1")
  expect(result.error.message).toContain(manifestDigest)
  expect(result.error.message).not.toContain("restart")
})

test("a mutation answered by a newer core contract reports unknown effect and names the remedy", async () => {
  adapter.configureConcordAdapter({ runner: runnerWithContext(foreignDigest(coreEnvelope("concord_work_define", "capture", "ok", { changed_refs: [] }))) })
  const result: any = await rawHostResult(adapter.work_define.execute(hostCall("capture", { title: "t", value_statement: "v", kind: "task", project_ids: ["p"], idempotency_key: "k" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.adapter_reason).toBe("unknown_effect")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(result.error.message).toContain("contact the operator with both digests")
  expect(result.error.message).not.toContain("restart")
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

test("worktree audit reclaim preserves committed refs through the adapter boundary", async () => {
  const response = coreEnvelope("concord_work_transition", "worktree_audit_reclaim", "error", {
    changed_refs: [{ entity_kind: "work_item", id: "work-2", version: "5" }],
    error: { kind: "budget_refused", retry_safe: false, recovery_action: { kind: "adjust_budget" }, effect_state: "possible", supported_budget_seconds: 300 },
  })
  expect(validateGeneratedEnvelope(response)).toBe(true)
  adapter.configureConcordAdapter({ runner: runnerWithContext(response) })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worktree_audit_reclaim", {
    product_id: "product-1", default_ref: "main", idempotency_key: "audit-reclaim-adapter-1",
  }), contextFor()))
  expect(validateGeneratedEnvelope(result)).toBe(true)
  expect(result.outcome).toBe("error")
  expect(result.changed_refs).toEqual([{ entity_kind: "work_item", id: "work-2", version: "5" }])
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

// The default agent matches the fake ToolContext's agent ("agent-1"):
// session-prepare now receives the active session agent and its read-back
// must name the same agent.
const preparedContract = (agent = "agent-1", prompt = "Implement the task.", title = "Add atomic start") => ({
  schema_version: "1.0",
  directory: "/data/worktrees/project-1/work-1",
  product_id: "product-1",
  work_id: "work-1",
  agent,
  title,
  prompt,
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
      : { exitCode: 0, stdout: JSON.stringify({ ...preparedContract(), directory: "/other-worktree" }), stderr: "" }
  } } })
  const mismatch: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(mismatch.outcome).toBe("error")
  expect(mismatch.error.kind).toBe("malformed_response")
  expect(mismatch.error.effect_state).toBe("none")
  expect(mismatch.error.recovery_action.kind).toBe("retry_same_request")
  // The work item and worktree that exist ride along so the replay's target is visible.
  expect(mismatch.work_id).toBe("work-1")
  // No rollback ran: nothing recorded intent, so nothing needs compensating.
  expect(calls).toBe(3)
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

  const linkedCalls: RetargetCall[] = []
  bindRetargetRoute()
  adapter.configureConcordAdapter({ runner: retargetRunner(linkedCalls, {
    "project-resolve": () => ({ exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }),
  }) })
  const linked: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(linked.outcome).toBe("ok")
  expect(linkedCalls.map(({ argv }) => argv[1])).toContain("work-bootstrap")

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
  bindRetargetRoute()
  const calls2: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls2) })
  const result: any = await rawHostResult(adapter.work_start.execute(validTask as any, contextFor()))
  expect(result.outcome).toBe("ok")
})

// CD-0098 D1/D2/D3/D4. Work start moves the calling session to the worktree it
// claimed. These tests pin the route's contract rather than a launch's: the
// claim exists before the move, an absent route refuses with no fallback, and
// success is refused unless the host reports the session in the claimed
// worktree. No step records intent ahead of its effect, so there is no
// partial outcome and no rollback: a replay under the same key converges.
const WORKTREE = "/data/worktrees/project-1/work-1"

type RetargetCall = { argv: string[]; input: string; options?: any }

const retargetRunner = (calls: RetargetCall[], overrides: Record<string, () => { exitCode: number; stdout: string; stderr: string }> = {}) => ({
  async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
    calls.push({ argv, input, options })
    if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
    const command = argv[1]
    if (overrides[command]) return overrides[command]()
    if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (command === "work-bootstrap") return { exitCode: 0, stdout: JSON.stringify(bootstrapSuccess()), stderr: "" }
    if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

// bindRetargetRoute stands in for the host's control plane. A test drives the
// route's answers directly, so the contract is exercised without a server.
const bindRetargetRoute = (options: { moveStatus?: number; moveBody?: string; landedDirectory?: string; unbound?: boolean } = {}) => {
  const moved: Array<Record<string, unknown>> = []
  let metadata: Record<string, unknown> = {}
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
      data: { id: "session-1", directory: options.landedDirectory ?? WORKTREE, metadata },
      response: new Response(null, { status: 200 }),
    }),
    patch: async ({ url, path, body }) => {
      expect(url).toBe("/session/{id}")
      expect(path).toEqual({ id: "session-1" })
      metadata = (body as { metadata: Record<string, unknown> }).metadata
      return { response: new Response(null, { status: 200 }) }
    },
  })
  return moved
}

test("work start moves the calling session into the claimed worktree", async () => {
  const moved = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1", agent: "agent-1" })
  expect(await hostControlPlane().taskScope("session-1")).toBe("managed")
  // The claim exists before the session moves, so a failed move leaves a
  // resumable claim rather than a moved session with none.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare", "project-resolve", "invoke"])
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  // session-prepare verifies the ACTIVE host agent and derives; it carries
  // no process identity and records nothing.
  expect(JSON.parse(calls[2].input)).toEqual({ product_id: "product-1", work_id: "work-1", task: bootstrapArgs.task, agent: "agent-1" })
  // No launch: the adapter never spawns a host session for the work.
  expect(calls.some(({ argv }) => argv[1] === "session-exec" || argv[0] === "opencode")).toBe(false)
})

// A core that answers session-prepare with any agent other than the one this
// session runs as fails the strict read-back: the move must not happen on an
// agent identity the session does not hold.
test("work start refuses a session-prepare read-back that names another agent", async () => {
  const moved = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
      "session-prepare": () => ({ exitCode: 0, stdout: JSON.stringify(preparedContract("concord-orchestrator")), stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.message).toContain("session-prepare response failed the strict prepare contract")
  expect(moved).toEqual([])
})

// Issue #917: a successful work_start names the zellij pane frame after the
// work title. The fork sits after every refusal point, so a success without
// ZELLIJ_PANE_ID stays fork-free and a failed fork stays a warning.
test("work start renames the zellij pane frame to the work title on success", async () => {
  process.env.ZELLIJ_PANE_ID = "402"
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("ok")
  // One rename fork, after session-prepare supplied the title and before the
  // gate brief's reads: exactly one per success.
  const renames = calls.filter(({ argv }) => argv[0] === "zellij")
  expect(renames).toHaveLength(1)
  expect(renames[0].argv).toEqual(["zellij", "action", "rename-pane", "-p", "402", "Add atomic start"])

  const resumeCalls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(resumeCalls) })
  const resumed: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(resumed.outcome).toBe("ok")
  const resumeRenames = resumeCalls.filter(({ argv }) => argv[0] === "zellij")
  expect(resumeRenames).toHaveLength(1)
  expect(resumeRenames[0].argv).toEqual(["zellij", "action", "rename-pane", "-p", "402", "Add atomic start"])
})

test("the pane name strips control characters and is cut at 64 code points", async () => {
  process.env.ZELLIJ_PANE_ID = "7"
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
      "session-prepare": () => ({ exitCode: 0, stdout: JSON.stringify(preparedContract("agent-1", "Implement the task.", `ab\u0001cd\u009Fef${"g".repeat(70)}`)), stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("ok")
  const renames = calls.filter(({ argv }) => argv[0] === "zellij")
  expect(renames).toHaveLength(1)
  const name = renames[0].argv[5]
  expect(name).toBe(`abcdefg${"g".repeat(57)}`)
  expect([...name]).toHaveLength(64)
})

test("work start without ZELLIJ_PANE_ID renames nothing", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("ok")
  expect(calls.some(({ argv }) => argv[0] === "zellij")).toBe(false)
})

test("a failed pane rename is a warning and never fails work_start", async () => {
  process.env.ZELLIJ_PANE_ID = "9"
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  // The exitCode-1 fork and the throwing fork are both warnings: the envelope
  // reports the completed start unchanged in either mode.
  const failing = { async run(argv: string[], input: string, signal: AbortSignal, options?: any) {
    if (argv[0] === "zellij") return { exitCode: 1, stdout: "", stderr: "no such pane" }
    return retargetRunner(calls).run(argv, input, signal, options)
  } }
  adapter.configureConcordAdapter({ runner: failing })
  const refused: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(refused.outcome).toBe("ok")
  expect(refused.work_id).toBe("work-1")

  const throwing = { async run(argv: string[], input: string, signal: AbortSignal, options?: any) {
    if (argv[0] === "zellij") throw new Error("zellij is absent")
    return retargetRunner(calls).run(argv, input, signal, options)
  } }
  adapter.configureConcordAdapter({ runner: throwing })
  const thrown: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(thrown.outcome).toBe("ok")
  expect(thrown.work_id).toBe("work-1")
})

test("work start forwards a core terminal-origin refusal", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
    "project-resolve": () => ({ exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }),
    "work-bootstrap": () => ({ exitCode: 1, stdout: "", stderr: "concord work-bootstrap: invalid_operation: cannot chain work_start from live work item work-origin" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("bootstrap_failure")
  expect(result.error.message).toContain("live work item work-origin")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap"])
})

// A host that serves no control plane cannot retarget, and CD-0098 D2 leaves
// no other route into the worktree. The refusal therefore belongs in front of
// the capture: an operator who cannot start work should not be left holding a
// work item and a claim to reconcile as well.
const resumeSuccess = () => ({
  schema_version: "1.0",
  product_id: "product-1",
  project_id: "project-1",
  work_id: "work-1",
  worktree: { set_id: "worktree-set-1", path: WORKTREE, branch: "work/work-1", base_sha: "a".repeat(40), state: "active" },
})

const resumeRunner = (calls: RetargetCall[], overrides: Record<string, () => { exitCode: number; stdout: string; stderr: string }> = {}) => ({
  async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
    calls.push({ argv, input, options })
    if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
    const command = argv[1]
    if (overrides[command]) return overrides[command]()
    if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (command === "work-resume") return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
    if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

test("work start resume derives the entry by work_id and moves the session", async () => {
  const moved = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(await hostControlPlane().taskScope("session-1")).toBe("managed")
  expect(result).toMatchObject({ outcome: "ok", product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: WORKTREE, agent: "agent-1", session_id: "session-1" })
  // A resume never captures: work-resume replaces work-bootstrap and records
  // nothing, so the child sequence has no journal step.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume", "session-prepare", "project-resolve", "invoke"])
  expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", work_id: "work-1" })
  // A resume carries no task; session-prepare still verifies the active
  // agent and the worktree.
  expect(JSON.parse(calls[2].input)).toEqual({ product_id: "product-1", work_id: "work-1", task: "", agent: "agent-1" })
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
})

test("work start resume forwards the typed core refusal and reads the landing back", async () => {
  const moved = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 1, stdout: "", stderr: "concord work-resume: invalid_operation: cannot resume terminal work item work-1 (completed)" }),
  }) })
  const refused: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(refused.outcome).toBe("error")
  expect(refused.error.kind).toBe("resume_failure")
  expect(refused.error.message).toContain("terminal work item work-1")
  expect(refused.error.effect_state).toBe("none")
  expect(refused.error.recovery_action.kind).toBe("retry_same_request")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
  expect(moved).toEqual([])

  const offTarget = bindRetargetRoute({ landedDirectory: "/somewhere-else" })
  adapter.configureConcordAdapter({ runner: resumeRunner(calls) })
  const mismatch: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(mismatch.outcome).toBe("error")
  expect(mismatch.error.kind).toBe("session_directory_mismatch")
  expect(mismatch.work_id).toBe("work-1")
  expect(mismatch.worktree_path).toBe(WORKTREE)
})

test("work start resume rejects mixed and malformed argument shapes", async () => {
  bindRetargetRoute({ unbound: true })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls) })
  for (const args of [
    {},
    { work_id: "work-1", title: "Both shapes at once" },
    { work_id: "" },
    { work_id: "work-1", idempotency_key: "capture-field-in-resume" },
    { title: "Capture fields without the required set" },
    { work_id: "work-1", product_id: "product-1" },
  ]) {
    const result: any = await rawHostResult(adapter.work_start.execute(args, contextFor()))
    expect(result.outcome, JSON.stringify(args)).toBe("error")
    expect(result.error.kind, JSON.stringify(args)).toBe("invalid_input")
    expect(result.error.message, JSON.stringify(args)).toContain("host-tool contract")
  }
  // Nothing ran: the shape refusal is in front of every effect, host probe
  // included, so a malformed call costs no child and no move.
  expect(calls).toEqual([])
})

// Missing capture fields must name the caller's correction without host effects.
test("work start names the missing capture fields and admits a corrected request", async () => {
  bindRetargetRoute({ unbound: true })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) { calls.push({ argv, input: "", options: undefined }); throw new Error("argument refusal must precede every effect") } } })
  const incomplete = { title: "Correct confirmed usage-reporting defects", kind: "bug", task: "Validate the reported defects and shape a bounded repair contract." }
  const result: any = await rawHostResult(adapter.work_start.execute(incomplete, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("invalid_input")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("host-tool contract")
  expect(result.error.message).toContain("value_statement")
  expect(result.error.message).toContain("idempotency_key")
  // An identical resubmission refuses again, but the owner is the caller with
  // a corrected request, not the operator with an unspecified repair.
  expect(result.error.retry_safe).toBe(false)
  expect(result.error.recovery_action.kind).toBe("correct_request")
  expect(calls).toEqual([])
  const repeated = await rawHostResult(adapter.work_start.execute(incomplete, contextFor()))
  expect(repeated.error).toEqual(result.error)
  expect(calls).toEqual([])
  bindRetargetRoute()
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const corrected = await rawHostResult(adapter.work_start.execute({ ...incomplete, value_statement: "Start valid work without operator repair.", idempotency_key: "corrected-start-1" }, contextFor()))
  expect(corrected.outcome).toBe("ok")
  expect(calls.filter(({ argv }) => argv[1] === "work-bootstrap")).toHaveLength(1)
})

test("capture and resume refuse before core effects when managed participation cannot be persisted", async () => {
  for (const args of [bootstrapArgs, { work_id: "work-1" }]) {
    for (const supportsPatch of [false, true]) {
      let updates = 0
      const client = {
        get: async () => ({ data: { id: "session-1", directory: WORKTREE, metadata: {} }, response: new Response(null, { status: 200 }) }),
        post: async () => { throw new Error("a refused enrollment cannot move the session") },
        ...(supportsPatch ? { patch: async () => { updates++; return { response: new Response(null, { status: 200 }) } } } : {}),
      }
      hostControlPlane().bind(client)
      const calls: RetargetCall[] = []
      adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
      const result: any = await rawHostResult(adapter.work_start.execute(args, contextFor()))
      expect(result.outcome).toBe("error")
      expect(result.error.kind).toBe("unreachable")
      expect(result.error.message).toContain("managed Task scope")
      expect(result.work_id).toBeUndefined()
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
      expect(updates).toBe(supportsPatch ? 1 : 0)
    }
  }
})

test("invalid work start input cannot enroll a host session", async () => {
  let hostCalls = 0
  hostControlPlane().bind({
    get: async () => { hostCalls++; throw new Error("invalid input must not reach the host") },
    post: async () => { hostCalls++; throw new Error("invalid input must not reach the host") },
    patch: async () => { hostCalls++; throw new Error("invalid input must not reach the host") },
  })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const { idempotency_key: _key, ...invalid } = bootstrapArgs
  const result: any = await rawHostResult(adapter.work_start.execute(invalid, contextFor()))
  expect(result.error.kind).toBe("invalid_input")
  expect(hostCalls).toBe(0)
  expect(calls).toEqual([])
})

const workStartDiagnosticCases: Array<{ name: string; args: unknown; fragments: string[] }> = [
  { name: "null arguments", args: null, fragments: ["object"] },
  { name: "array arguments", args: [], fragments: ["object"] },
  { name: "missing capture fields", args: {}, fragments: ["missing", "title", "value_statement", "kind", "task", "idempotency_key"] },
  { name: "unknown capture field", args: { ...bootstrapArgs, project_id: "sensitive-input-value" }, fragments: ["undeclared", "project_id"] },
  { name: "unknown resume field", args: { work_id: "work-1", extra: "sensitive-input-value" }, fragments: ["undeclared", "extra"] },
  { name: "invalid kind", args: { ...bootstrapArgs, kind: "sensitive-input-value" }, fragments: ["kind", "enum"] },
  { name: "invalid urgency", args: { ...bootstrapArgs, urgency: "sensitive-input-value" }, fragments: ["urgency", "enum"] },
  { name: "string priority", args: { ...bootstrapArgs, priority: "sensitive-input-value" }, fragments: ["priority", "integer"] },
  { name: "fractional priority", args: { ...bootstrapArgs, priority: 0.5 }, fragments: ["priority", "integer"] },
  { name: "priority below minimum", args: { ...bootstrapArgs, priority: -101 }, fragments: ["priority", "minimum", "-100"] },
  { name: "priority above maximum", args: { ...bootstrapArgs, priority: 101 }, fragments: ["priority", "maximum", "100"] },
  { name: "bad idempotency key", args: { ...bootstrapArgs, idempotency_key: "sensitive-input-value bad" }, fragments: ["idempotency_key", "match"] },
  { name: "bad workflow reference", args: { ...bootstrapArgs, workflow_type_ref: "sensitive-input-value bad" }, fragments: ["workflow_type_ref", "match"] },
  { name: "empty title", args: { ...bootstrapArgs, title: "" }, fragments: ["title", "shorter", "1"] },
  { name: "title character limit", args: { ...bootstrapArgs, title: "x".repeat(257) }, fragments: ["title", "256"] },
  { name: "title byte limit", args: { ...bootstrapArgs, title: "é".repeat(129) }, fragments: ["title", "256", "UTF-8 bytes"] },
  { name: "value statement byte limit", args: { ...bootstrapArgs, value_statement: "é".repeat(129) }, fragments: ["value_statement", "256", "UTF-8 bytes"] },
  { name: "external reference byte limit", args: { ...bootstrapArgs, external_ref: "é".repeat(129) }, fragments: ["external_ref", "256", "UTF-8 bytes"] },
  { name: "task byte limit", args: { ...bootstrapArgs, task: "🙂".repeat(2049) }, fragments: ["task", "8192", "UTF-8 bytes"] },
  { name: "empty resume identity", args: { work_id: "" }, fragments: ["work_id", "shorter", "1"] },
  { name: "invalid resume identity", args: { work_id: "sensitive-input-value bad" }, fragments: ["work_id", "match"] },
  { name: "oversize resume identity", args: { work_id: "w".repeat(129) }, fragments: ["work_id", "128"] },
  { name: "null resume identity", args: { work_id: null }, fragments: ["work_id", "string"] },
  { name: "duplicate tags", args: { ...bootstrapArgs, tags: ["sensitive-input-value", "sensitive-input-value"] }, fragments: ["tags", "duplicate"] },
  { name: "invalid tag item", args: { ...bootstrapArgs, tags: ["valid", "sensitive-input-value bad"] }, fragments: ["tags[1]", "match"] },
  { name: "too many tags", args: { ...bootstrapArgs, tags: Array.from({ length: 33 }, (_, index) => `tag-${index}`) }, fragments: ["tags", "32"] },
  { name: "invalid requirement item", args: { ...bootstrapArgs, governing_requirements: ["valid", 1] }, fragments: ["governing_requirements[1]", "string"] },
]
for (const field of hostToolSchemas.concord_work_start.oneOf[0].required) {
  const args: Record<string, unknown> = { ...bootstrapArgs }
  delete args[field]
  workStartDiagnosticCases.push({ name: `missing ${field}`, args, fragments: ["missing", field] })
}
for (const field of Object.keys(hostToolSchemas.concord_work_start.oneOf[0].properties)) {
  workStartDiagnosticCases.push({ name: `resume mixed with ${field}`, args: { work_id: "work-1", [field]: "sensitive-input-value" }, fragments: ["undeclared", field] })
}
for (const field of ["constructor", "toString", "__proto__"]) {
  for (const mode of ["capture", "resume"]) {
    const base = mode === "capture" ? bootstrapArgs : { work_id: "work-1" }
    workStartDiagnosticCases.push({ name: `${mode} with ${field}`, args: { ...base, [field]: "sensitive-input-value" }, fragments: ["undeclared", field] })
  }
}
for (const { name, args, fragments } of workStartDiagnosticCases) {
  test(`work start diagnostic: ${name}`, async () => {
    let hostCalls = 0
    hostControlPlane().bind({
      get: async () => { hostCalls++; throw new Error("invalid input reached the host") },
      post: async () => { hostCalls++; throw new Error("invalid input reached the host") },
      patch: async () => { hostCalls++; throw new Error("invalid input reached the host") },
    })
    let coreCalls = 0
    adapter.configureConcordAdapter({ runner: { async run() { coreCalls++; throw new Error("invalid input reached the core") } } })
    const result = await rawHostResult(adapter.work_start.execute(args, contextFor()))
    expect(result).toMatchObject({ outcome: "error", error: { kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "correct_request" } } })
    for (const fragment of fragments) expect(result.error.message).toContain(fragment)
    expect(result.error.message).toContain("Capture requires")
    expect(result.error.message).toContain("Resume requires only work_id")
    expect(result.error.message).not.toContain("sensitive-input-value")
    expect(result.work_id).toBeUndefined()
    expect(hostCalls).toBe(0)
    expect(coreCalls).toBe(0)
  })
}

test("work start description explains the generated capture and resume requirements", async () => {
  const definition = { description: adapter.work_start.description, parameters: {}, jsonSchema: {} }
  await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, definition)
  const [capture, resume] = hostToolSchemas.concord_work_start.oneOf
  expect(definition.description).toContain(`Capture requires ${capture.required.join(", ")}`)
  expect(definition.description).toContain(`Resume requires only ${resume.required.join(", ")}`)
  expect(definition.description).toContain("Do not combine capture and resume fields")
  expect(definition.description.match(/Capture requires/g)).toHaveLength(1)
})

test("work start accepts minimal capture and exact declared bounds in a resolved project", async () => {
  const minimal = Object.fromEntries(hostToolSchemas.concord_work_start.oneOf[0].required.map((field) => [field, bootstrapArgs[field]]))
  const bounded = {
    ...bootstrapArgs,
    title: "é".repeat(128), value_statement: "é".repeat(128), external_ref: "é".repeat(128), task: "🙂".repeat(2048),
    idempotency_key: "i".repeat(128), workflow_type_ref: "w".repeat(128), ref: "r".repeat(128),
    tags: Array.from({ length: 32 }, (_, index) => `tag-${index}`),
    governing_requirements: Array.from({ length: 32 }, (_, index) => `law-${index}`),
    urgency: "expedite",
  }
  for (const args of [minimal, { ...bounded, priority: -100 }, { ...bounded, priority: 100 }]) {
    const moved = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result = await rawHostResult(adapter.work_start.execute(args, contextFor()))
    expect(result).toMatchObject({ outcome: "ok", product_id: "product-1", project_id: "project-1", work_id: "work-1" })
    expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", ...args })
    expect(calls.filter(({ argv }) => argv[1] === "work-bootstrap")).toHaveLength(1)
    expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  }
})

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
  expect(result.outcome).toBe("error")
  expect(result.error.message).toContain("/somewhere/else")
  // Nothing is recorded: the host's answer is the fact, and a replay asks again.
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
})

// Issues #742 and #749. A failure after the work item and worktree exist
// used to leave a partial state no typed operation could clear. There is no
// such state now: every step is idempotent on the derived key, so a replay
// under the same idempotency_key adopts what exists and runs the rest.
test("work start replays to convergence after an interrupted step", async () => {
  // First attempt: session-prepare fails after bootstrap created the work item.
  bindRetargetRoute()
  const first: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(first, {
      "session-prepare": () => ({ exitCode: 1, stdout: "", stderr: "concord session-prepare: lane identity refused" }),
    }),
  })
  const interrupted: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(interrupted.outcome).toBe("error")
  expect(interrupted.error.effect_state).toBe("none")
  expect(interrupted.error.retry_safe).toBe(true)
  expect(interrupted.error.recovery_action.kind).toBe("retry_same_request")
  expect(interrupted.work_id).toBe("work-1")
  expect(interrupted.worktree_path).toBe(WORKTREE)
  expect(await hostControlPlane().taskScope("session-1")).toBe("managed")
  // The failed prepare retains the claim and participation without compensation.
  expect(first.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])

  // Second attempt, same key: bootstrap replays (the core reports replayed:
  // true), prepare succeeds, the move lands, and the answer is ok.
  const moved = bindRetargetRoute()
  const second: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(second, {
      "work-bootstrap": () => ({ exitCode: 0, stdout: JSON.stringify({ ...bootstrapSuccess(), replayed: true }), stderr: "" }),
    }),
  })
  const converged: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(converged).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1" })
  expect(second.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare", "project-resolve", "invoke"])
  expect(JSON.parse(second[1].input).idempotency_key).toBe(bootstrapArgs.idempotency_key)
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
})

// A move the host refuses leaves the claim where it was: the next replay
// asks the host again, and no compensation runs in between.
test("work start leaves a resumable claim when the move is refused", async () => {
  bindRetargetRoute({ moveStatus: 400, moveBody: JSON.stringify({ name: "MoveSessionError", data: { message: "destination project mismatch" } }) })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
  expect(result.work_id).toBe("work-1")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
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
      if (url === SESSION_ROUTE) return { data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }
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

// CD-0111 D1: the core path is the stamped release constant, never a PATH
// lookup. The transport must contain no ambient `concord` resolution, and an
// unstamped adapter copy must refuse with missing_binary instead of spawning.
test("core_binary_is_the_stamped_release_constant", async () => {
  expect(source).toContain('runner.run([concordBinaryPath(), "invoke"]')
  expect(source).toContain('runner.run([concordBinaryPath(), "project-resolve"]')
  expect(source).toContain("[concordBinaryPath(), \"work-bootstrap\"]")
  expect(source).toContain("[concordBinaryPath(), \"session-prepare\"]")
  expect(source).not.toContain('CONCORD_BIN ?? "concord"')
  expect(source).not.toContain('"concord", "invoke"')

  configureCoreBinary(null)
  const envelope: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor()))
  expect(envelope.error.kind).toBe("transport_failure")
  expect(envelope.error.adapter_reason).toBe("missing_binary")
  expect(envelope.error.message).toContain("not bound to a release")
  configureCoreBinary("concord")
})

// CD-0111 D2: a session that could not claim its host lease keeps the tools
// closed, so the installer can never mistake it for an ended session.
test("a session without a host lease refuses every core operation", async () => {
  configureHostLease({ reset: true })
  // The repository placeholder carries no release, so the claim fails the way
  // any failed claim does: the fault is recorded and the tools stay closed.
  await claimHostLease(process.pid)
  adapter.configureConcordAdapter({ runner: runnerWithContext(coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } })) })
  const envelope: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor()))
  expect(envelope.outcome).toBe("error")
  expect(envelope.error.kind).toBe("transport_failure")
  expect(envelope.error.adapter_reason).toBe("host_lease_missing")
  expect(envelope.error.recovery_action.kind).toBe("contact_operator")
  configureHostLease({ reset: true })
})

test("the host-owned tool description publishes the native dispatch route", () => {
  const description = (adapter.work_transition as any).description
  expect(description).toContain("operation workflow_action")
  expect(description).toContain("action_id dispatch_worker")
  expect(description).toContain("fields.lane_id")
  expect(description).toContain("Route discovery does not prove admission at the current workflow step")
  expect(contractOperations.some((operation: any) => operation.id === "concord_work_transition.workflow_action")).toBe(true)
})

test("portable continuation posture leaves host protocol names to the host surface", () => {
  for (const hostTerm of [
    "Concord",
    "concord_work_transition",
    "workflow_action",
    "action_id",
    "fields.lane_id",
    "contact_operator",
    "host",
    "native",
  ]) {
    expect(continuationSource).not.toContain(hostTerm)
  }
  expect(askingSource).toContain("Do not ask for permission to continue work already agreed")
  expect(continuationSource).not.toContain("Do not ask for general permission to continue")
  expect(continuationSource.match(/When you stop,/g)?.length).toBe(1)
})
