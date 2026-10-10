import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { chmod, mkdir, mkdtemp, rm, symlink } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, hostToolSchemas, manifestDigest, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { configureCoreBinary } from "./dispatch"
import { dispatchWindows, staleReleaseDispatchRefusal, TASK_TOOL_ID } from "./dispatch-window"
import { claimHostLease, configureHostLease, hostLeaseFault, releaseDisplayName, releaseStaleness, resolveInstalledReleaseRoot } from "./host-lease"
import { armedClaimedWorktree, clearClaimedWorktree, resetClaimedWorktrees, unlandedClaimedWorktree } from "./claimed-worktree"
import { validateGeneratedEnvelope, envelopeFailurePath, expandedPublishedRequestSchema } from "./generated-contract-tests"
import { hostControlPlane, SESSION_LIST_ROUTE, SESSION_MESSAGES_ROUTE, SESSION_ROUTE, type RouteResult } from "./move-session"
import { adoptManifestDigest, resetManifestPinForTesting } from "./manifest-pin"
import { resetMoveNotices, takeMoveNotice } from "./move-notice"
import { armTurnMoveBoundary, clearTurnMoveBoundary, dispatchRequiresNextTurn } from "./turn-move-boundary"
import { syntheticHostVersionFixture } from "./host-version.test-support"

syntheticHostVersionFixture()

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
const askingSource = await Bun.file(new URL("../../.concord/instructions/asking.md", import.meta.url)).text()
const continuationSource = await Bun.file(new URL("../../.concord/instructions/continuation.md", import.meta.url)).text()
// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

const adapter = await import("./concord")

// A work_start success renames the zellij pane frame when the host exports
// ZELLIJ_PANE_ID (issue #917). The suite stays hermetic against the operator's
// own zellij session: no test sees a pane id unless it sets one.
const outerPaneID = process.env.ZELLIJ_PANE_ID
const outerSelectedProductID = process.env.CONCORD_SELECTED_PRODUCT_ID
beforeEach(() => {
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  resetClaimedWorktrees()
  // The text-part queue is adapter module state; drain it so one test's
  // notices never leak into the next test's assertions.
  adapter.takeWorkNotices("session-1")
  resetMoveNotices()
  delete process.env.ZELLIJ_PANE_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
})
afterEach(() => {
  resetClaimedWorktrees()
  if (outerPaneID === undefined) delete process.env.ZELLIJ_PANE_ID
  else process.env.ZELLIJ_PANE_ID = outerPaneID
  if (outerSelectedProductID === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
  else process.env.CONCORD_SELECTED_PRODUCT_ID = outerSelectedProductID
})
const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })

test("exports exactly the generated tool names", () => {
  const names = [...source.matchAll(/export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(/g)].map((match) => match[1]).filter((name) => name !== "work_start")
  expect(names).toEqual(["product_view", "work_browse", "work_trace", "knowledge", "work_define", "domain", "work_transition", "work_relate", "work_compact"])
  expect(source).toContain("export const work_start = tool(")
  expect(new Set(contractOperations.map((operation: any) => operation.tool))).toEqual(new Set(names.map((name) => `concord_${name}`)))
})

test("published tool arguments expose a host-safe request shape", async () => {
  const tools = {
    concord_product_view: adapter.product_view,
    concord_work_browse: adapter.work_browse,
    concord_work_trace: adapter.work_trace,
    concord_knowledge: adapter.knowledge,
    concord_work_define: adapter.work_define,
    concord_domain: adapter.domain,
    concord_work_transition: adapter.work_transition,
    concord_work_relate: adapter.work_relate,
    concord_work_compact: adapter.work_compact,
  } as const
  const actionInput = (schema: any, actionId: string) => {
    const expanded: any = expandedPublishedRequestSchema(schema)
    const branch = expanded.oneOf.find((candidate: any) => candidate.properties.operation.const === "workflow_action" && candidate.properties.input.properties.action_id.const === actionId)
    if (branch === undefined) throw new Error(`published schema carries no closed ${actionId} variant branch`)
    return branch.properties.input
  }
  for (const [toolName, exportedTool] of Object.entries(tools)) {
    expect(Object.keys((exportedTool as any).args), toolName).toEqual(["request"])
    const published = adapter.publishedRequestSchema(toolName) as any
    const expected = contractOperations.filter((item: any) => item.tool === toolName).map((item: any) => item.id.split(".")[1])
    expect(published.properties.operation.enum, toolName).toEqual(expected)
    // The published input is compact (CON-812): the request parent keeps its
    // type, required set, and closure, and the document carries its own
    // local reference table that the expansion resolves.
    expect(published.properties.input.type, toolName).toBe("object")
    expect(published.$defs, toolName).toBeObject()
    expect(() => expandedPublishedRequestSchema(published), toolName).not.toThrow()
    // One compact branch per operation; the expanded view restores one
    // closed branch per operation and per action variant, in contract order
    // then registry action order. No branch carries a sibling operation's
    // or action's fields.
    expect(published.oneOf, toolName).toHaveLength(expected.length)
    const branches = (expandedPublishedRequestSchema(published) as any).oneOf
    const variantActionIds = workflowActionPublicVariants.map((variant) => variant.action_id)
    const expectedBranchDiscriminators = expected.flatMap((operation: string) =>
      operation === "workflow_action" && toolName === "concord_work_transition"
        ? variantActionIds.map((actionId: string) => `${operation}:${actionId}`)
        : [operation],
    )
    expect(
      branches.map((branch: any) =>
        branch.properties.operation.const === "workflow_action" && branch.properties.input.properties.action_id?.const
          ? `workflow_action:${branch.properties.input.properties.action_id.const}`
          : branch.properties.operation.const,
      ),
      toolName,
    ).toEqual(expectedBranchDiscriminators)
    for (const branch of branches) {
      expect(branch.type, toolName).toBe("object")
      expect(branch.additionalProperties, toolName).toBe(false)
      expect(branch.properties.input.type, toolName).toBe("object")
      expect(branch.properties.input.additionalProperties, toolName).toBe(false)
      // required is the authored set exactly: an operation that requires
      // nothing publishes no required array — publication no longer merges
      // sibling requireds into a fabricated one.
      expect(branch.properties.input.required === undefined || Array.isArray(branch.properties.input.required), toolName).toBe(true)
      expect(Object.keys(branch.properties.input.properties).length, toolName).toBeGreaterThan(0)
    }
    expect(JSON.stringify(published), toolName).not.toContain("~standard")
    expect(JSON.stringify(published), toolName).not.toContain('"def"')
    expect(JSON.stringify(published), toolName).not.toContain("#/properties/request/definitions/")
    // Publication emits closed authored shapes, never the shared allOf or
    // if-then conditional form: the core keeps consuming that form, and a
    // published input carrying allOf or if is the merge fault back again.
    // Authored bounded unions (outcome_payload variants, the resolve
    // product/project selector oneOf) survive as-is.
    expect(JSON.stringify(published), toolName).not.toContain('"allOf"')
    for (const branch of branches) {
      const inputJson = JSON.stringify(branch.properties.input)
      expect(inputJson, toolName).not.toContain('"allOf"')
      expect(inputJson, toolName).not.toContain('"if"')
    }
  }
  const published = adapter.publishedRequestSchema("concord_work_define") as any
  const expandedDefine: any = expandedPublishedRequestSchema(published)
  const captureBranch = expandedDefine.oneOf.find((candidate: any) => candidate.properties.operation.const === "capture")
  const captureInput = captureBranch.properties.input
  expect(captureInput.required).toEqual(["title", "value_statement", "kind", "project_ids", "idempotency_key"])
  expect(captureInput.properties.urgency.enum).toEqual(["standard", "expedite"])
  const transition = adapter.publishedRequestSchema("concord_work_transition") as any
  // Select the approve_contract variant by its action_id const: it is the
  // variant whose fields carry the outcome_predicates admission rules.
  const actionBranch = actionInput(transition, "approve_contract")
  // CON-412: the items close per kind; each branch binds outcome_kind to
  // outcome_payload.kind and carries that branch's payload inline.
  const itemVariants = actionBranch.properties.fields.properties.outcome_predicates.items.oneOf
  expect(itemVariants.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  expect(itemVariants.map((branch: any) => branch.properties.outcome_payload.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  // The branch names the conditional action fields, so a calling agent can
  // read them before calling.
  for (const field of ["action_id", "selected_choice", "decision_context_digest"]) {
    expect(actionBranch.properties[field], field).toBeObject()
  }
  // The approval premise an author writes is the published action field, so
  // it carries the unit guidance: code points, the UTF-8 byte admission, and
  // its distinction from packet and model-token limits.
  const premise = actionBranch.properties.fields.properties.premise
  expect(premise.maxLength).toBe(4_096)
  expect(premise.description).toContain("Unicode code points")
  expect(premise.description).toContain("UTF-8 bytes")
  expect(premise.description).toContain("model-token limit")
  expect(premise.description).toContain("Do not truncate an approved objective")
  // Every generated field reaches the host through the registration map,
  // which derives its field set from the generated capture/resume branches —
  // no handwritten parity list. project_id is the CD-0182 resume selector:
  // resume-only, never capture.
  const [generatedCapture, generatedResume] = hostToolSchemas.concord_work_start.oneOf
  const expectedWorkStartFields = Object.keys({ ...generatedCapture.properties, ...generatedResume.properties })
  expect(Object.keys((adapter.work_start as any).args).sort(), "work_start registration fields").toEqual(expectedWorkStartFields.sort())
  for (const value of Object.values((adapter.work_start as any).args)) expect(value).toBeObject()
  expect((adapter.work_start as any).args.product_id).toBeUndefined()
  expect((adapter.work_start as any).args.project_id).toBeObject()
  // Conditional branches preserve both generated closed modes without a
  // root union that Anthropic's tool-schema validator would reject.
  const workStartDefinition = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
  const publishedWorkStart = workStartDefinition.jsonSchema as any
  expect(publishedWorkStart.if).toEqual({ required: ["work_id"] })
  expect(publishedWorkStart.else).toEqual(generatedCapture)
  expect(publishedWorkStart.then).toEqual(generatedResume)
})

test("work define publishes the record-only issue identity contract", () => {
  const published = adapter.publishedRequestSchema("concord_work_define") as any
  expect(published.properties.operation.enum).toContain("issue_link_record")
  expect(published.properties.operation.enum).not.toContain("issue_adopt")
  const branch = published.oneOf.find((item: any) => item.properties.operation.const === "issue_link_record")
  const input = branch.properties.input
  expect(input.required).toEqual(["work_id", "human_key", "remote_issue_uuid", "url", "idempotency_key"])
  expect(input.additionalProperties).toBe(false)
  expect(Object.keys(input.properties).sort()).toEqual(["approval", "human_key", "idempotency_key", "remote_issue_uuid", "requested_budget_seconds", "url", "work_id"])
  expect(input.properties.human_key).toEqual(payloadSchemas.work_define_issue_link_record_input.properties.human_key)
  expect(input.properties.url).toEqual(payloadSchemas.work_define_issue_link_record_input.properties.url)
})

test("issue link record forwards the recorded key UUID and URL unchanged", async () => {
  const input = {
    work_id: "work-1",
    human_key: "EX-3",
    remote_issue_uuid: "cccccccc-0000-0000-0000-000000000003",
    url: "https://linear.app/example/issue/EX-3",
    idempotency_key: "link-record-1",
  }
  const invokes: any[] = []
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], stdin: string) => {
    invokes.push(JSON.parse(stdin))
    return coreEnvelope("concord_work_define", "issue_link_record", "ok", {
      changed_refs: [], next_valid_intents: [],
      result: { changed_refs: [], next_valid_intents: [] },
    })
  }) })
  const result: any = await rawHostResult(adapter.work_define.execute(hostCall("issue_link_record", input), contextFor()))
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  expect(result.outcome).toBe("ok")
  expect(result.origin).toBe("core")
  expect(invokes).toHaveLength(1)
  expect(invokes[0]).toMatchObject({ tool: "concord_work_define", operation: "issue_link_record", input })
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
  for (const toolName of ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_work_define", "concord_domain", "concord_work_transition", "concord_work_relate", "concord_work_compact"]) {
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

test("request-wrapped tools refuse a missing request wrapper before any host effect", async () => {
  const tools = { product_view: adapter.product_view, work_browse: adapter.work_browse, work_trace: adapter.work_trace, knowledge: adapter.knowledge, work_define: adapter.work_define, domain: adapter.domain, work_transition: adapter.work_transition, work_relate: adapter.work_relate, work_compact: adapter.work_compact }
  let calls = 0
  adapter.configureConcordAdapter({ runner: { run: async () => { calls++; throw new Error("must not run") } } })
  for (const [name, exportedTool] of Object.entries(tools)) {
    const toolName = `concord_${name}`
    const operation = contractOperations.find((candidate) => candidate.tool === toolName)!.id.split(".")[1]
    for (const args of [{ operation, input: {} }, { operation, request: null }, { operation, request: [] }, {}, { request: null }, { request: [] }, { request: "invalid" }, { operation: "unknown" }, { operation: "x".repeat(65536) }, { operation: 1 }]) {
      const result: any = await exportedTool.execute(args as any, contextFor())
      const envelope = JSON.parse(result.output)
      expect(envelope.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "restart_query" } })
      expect(envelope.error.message).toContain("request wrapper")
      expect(envelope.tool).toBe(toolName)
      expect(envelope.operation).toBe("operation" in args && args.operation === operation ? operation : "")
      expect(validateGeneratedEnvelope(envelope), result.output).toBe(true)
      if (envelope.operation === "") {
        expect(envelope.query_id).toBeUndefined()
        expect(validateGeneratedEnvelope({ ...envelope, origin: "core" })).toBe(false)
        expect(validateGeneratedEnvelope({ ...envelope, query_id: "PM1.Q1" })).toBe(false)
      }
      for (const error of [{ kind: "transport_failure", recovery_action: { kind: "contact_operator" } }, { adapter_reason: "missing_binary" }, { effect_state: "possible" }, { retry_safe: true }, { recovery_action: { kind: "retry_same_request" } }]) {
        expect(validateGeneratedEnvelope({ ...envelope, error: { ...envelope.error, ...error } })).toBe(false)
      }
    }
  }
  expect(calls).toBe(0)
})

test("a missing wrapper refusal carries the stale-release notice without a host effect", async () => {
  await withReleaseLayout("v11.40.6", async () => {
    let calls = 0
    adapter.configureConcordAdapter({ runner: { run: async () => { calls++; throw new Error("must not run") } } })
    const result: any = await adapter.work_browse.execute({ operation: "list", input: {} } as any, contextFor())
    const envelope = JSON.parse(result.output)
    expect(envelope.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "restart_query" } })
    expect(envelope.warnings).toHaveLength(1)
    expect(envelope.warnings[0]).toMatchObject({ kind: "release_stale", source_id: "adapter", details: { pinned_release: "v11.40.3", installed_release: "v11.40.6", remedy: "restart this session to load the installed release" } })
    expect(validateGeneratedEnvelope(envelope), result.output).toBe(true)
    expect(calls).toBe(0)
  })
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

test("a dropped top-level operation is recovered from the input copy", async () => {
  let invokeStdin = ""
  const core = coreEnvelope("concord_work_browse", "list", "error", {
    error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
  })
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    invokeStdin = input
    return core
  }) })
  // The bridge can deliver the wrapper without the published top-level
  // operation, leaving the caller's input copy as the only surviving name.
  const hostResult: any = await adapter.work_browse.execute({ request: { request: { input: { operation: "list", product_id: "concord" } } } } as any, contextFor())
  const envelope = JSON.parse(hostResult.output)
  expect(envelope.origin).toBe("core")
  const sent = JSON.parse(invokeStdin)
  expect(sent.operation).toBe("list")
  expect(sent.input).toEqual({ product_id: "concord" })
})

test("an echoed operation copy never reaches the core payload", async () => {
  let invokeStdin = ""
  const core = coreEnvelope("concord_work_browse", "list", "error", {
    error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
  })
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    invokeStdin = input
    return core
  }) })
  const hostResult: any = await adapter.work_browse.execute({ request: { operation: "list", input: { operation: "list", product_id: "concord" } } } as any, contextFor())
  JSON.parse(hostResult.output)
  const sent = JSON.parse(invokeStdin)
  expect(sent.operation).toBe("list")
  expect(sent.input).toEqual({ product_id: "concord" })
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
  // The lane guard passes: the host answers the ancestry read for an
  // unparented session, but the record carries no directory, so the context
  // resolution fails after the guard and before any core transport.
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", metadata: {} }, response: new Response(null, { status: 200 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
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
  // A read cannot have written, so its failure never reports a possible
  // effect and never sends the caller to reconcile a write.
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
})

// A read failure on a mutation-bearing tool must never report a possible
// effect or a reconcile recovery.
test("a failed domain list read never reports a possible effect", async () => {
  adapter.configureConcordAdapter({ runner: runnerWithContext({ exitCode: 1, stdout: "", stderr: "core marshal failure" }) })
  const result: any = await rawHostResult(adapter.domain.execute(hostCall("list", { product_id: "product-1" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("transport_failure")
  expect(result.error.adapter_reason).toBe("io_failure")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
})

test("typed CLI input refusals report invalid_input and no effect", async () => {
  for (const [tool, operation] of [[adapter.work_define, "capture"], [adapter.domain, "list"]] as const) {
    adapter.configureConcordAdapter({ runner: runnerWithContext({ exitCode: 64, stdout: "", stderr: "concord invoke: missing required field input" }) })
    const result: any = await rawHostResult(tool.execute(hostCall(operation, {}), contextFor()))
    assertAdapterEnvelope(result)
    expect(result.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "restart_query" } })
    expect(result.error.message).toContain("missing required field input")
  }
})

test("validation text without the typed CLI signal cannot erase a possible effect", async () => {
  for (const response of [{ exitCode: 1, stdout: "", stderr: "missing required field input" }, ...["not-json", "\n", " \t"].map((stdout) => ({ exitCode: 64, stdout, stderr: "missing required field input" }))]) {
    adapter.configureConcordAdapter({ runner: runnerWithContext(response) })
    const result: any = await rawHostResult(adapter.work_define.execute(hostCall("capture", {}), contextFor()))
    expect(result.error.effect_state).toBe("possible")
    expect(result.error.kind).not.toBe("invalid_input")
  }
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

// A mutation's core child can commit its transaction before an abort, a
// timeout, or a crash kills it, so a runner failure after the invoke started
// reports a possible effect and reconciles the request work ID. A missing
// binary started nothing and keeps the no-effect refusal.
test("mutation runner failures after the invoke started report a possible effect", async () => {
  const readback = coreEnvelope("concord_work_browse", "list", "ok", {
    result: { items: [{ id: "work-1", kind: "task", title: "Task", lifecycle: "completed", version: 3 }] },
  })
  const failures: Array<[string, () => unknown]> = [
    ["timeout", () => Object.assign(new Error("core invocation timed out"), { name: "TimeoutError" })],
    ["cancelled", () => Object.assign(new Error("aborted"), { name: "AbortError" })],
    ["spawn_failure", () => new Error("core child died")],
  ]
  for (const [label, failure] of failures) {
    let calls = 0
    const result: any = await runTransition({ async run() {
      calls++
      if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (calls === 2) throw failure()
      return { exitCode: 0, stdout: JSON.stringify(readback), stderr: "" }
    } })
    assertAdapterEnvelope(result)
    expect(result.error.kind, label).toBe("operation_conflict")
    expect(result.error.adapter_reason, label).toBe("unknown_effect")
    expect(result.error.effect_state, label).toBe("possible")
    expect(result.error.recovery_action.kind, label).toBe("reconcile_operation")
    expect(result.error.details.reconciled, label).toEqual({ found: true, lifecycle: "completed", version: 3 })
  }

  let calls = 0
  const missing: any = await runTransition({ async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    throw Object.assign(new Error("spawn concord ENOENT"), { code: "ENOENT" })
  } })
  assertAdapterEnvelope(missing)
  expect(calls).toBe(2)
  expect(missing.error.adapter_reason).toBe("missing_binary")
  expect(missing.error.effect_state).toBe("none")
})

// ENOENT is the spawn's own failure: the binary never resolved and no child
// started, so the refusal keeps effect none whatever the abort signal's
// state. A healed skew retry keeps the possible effect: its first invoke's
// child already ran, so the retry's ENOENT proves nothing about the commit.
test("an aborted signal does not mask a missing binary on a mutation", async () => {
  const controller = new AbortController()
  controller.abort()
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    throw Object.assign(new Error("spawn concord ENOENT"), { code: "ENOENT" })
  } } })
  const missing: any = await rawHostResult(adapter.work_transition.execute(
    hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-1" }),
    contextFor(undefined, controller),
  ))
  assertAdapterEnvelope(missing)
  expect(calls).toBe(2)
  expect(missing.error.kind).toBe("transport_failure")
  expect(missing.error.adapter_reason).toBe("missing_binary")
  expect(missing.error.effect_state).toBe("none")
  expect(missing.error.recovery_action.kind).toBe("contact_operator")
  expect(missing.error.details?.reconciled).toBeUndefined()

  const skewPin = "sha256:" + "2".repeat(64)
  try {
    expect(adoptManifestDigest(skewPin)).toBe(true)
    let retryCalls = 0
    adapter.configureConcordAdapter({ runner: { async run() {
      retryCalls++
      if (retryCalls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (retryCalls === 2) return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "lifecycle", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      if (retryCalls === 3) throw Object.assign(new Error("spawn concord ENOENT"), { code: "ENOENT" })
      // The reconcile readback: context resolution, then the work read.
      if (retryCalls === 4) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })), stderr: "" }
    } } })
    const retried: any = await rawHostResult(adapter.work_transition.execute(
      hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-2" }),
      contextFor(undefined, controller),
    ))
    assertAdapterEnvelope(retried)
    expect(retryCalls).toBe(5) // context, skewed invoke whose child ran, healed retry whose binary is gone, readback context, readback read
    expect(retried.error.kind).toBe("operation_conflict")
    expect(retried.error.adapter_reason).toBe("unknown_effect")
    expect(retried.error.effect_state).toBe("possible")
    expect(retried.error.recovery_action.kind).toBe("reconcile_operation")
    expect(retried.error.details.reconciled).toEqual({ found: false, lifecycle: null, version: null })
  } finally {
    resetManifestPinForTesting()
  }
})

// A read's thrown runner failure keeps the no-effect refusal: the child's
// death names the transport event, and a read cannot have written, so the
// refusal never reports a possible effect and never reconciles.
test("a read's thrown runner failure at the invoke keeps the no-effect refusal", async () => {
  let calls = 0
  const result: any = await runProduct({ async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    throw Object.assign(new Error("aborted"), { name: "AbortError" })
  } })
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("cancelled")
  expect(result.error.adapter_reason).toBe("cancelled_no_effect")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
  expect(result.error.details?.reconciled).toBeUndefined()
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
// The envelope is the first line of the tool output. Failed best-effort side
// effects append warning lines after it, so a test that owns a warning parses
// this line alone and asserts on the raw output around it.
const envelopeLine = (output: string) => JSON.parse(output.slice(0, output.indexOf("\n") === -1 ? output.length : output.indexOf("\n")))
// The envelope is the first line of the tool output. Failed best-effort side
// effects append warning lines after it, so the JSON parse reads the first
// line alone and a test that owns a warning asserts on the raw output.
const rawHostResult = async (result: Promise<string | { output: string }>) => {
  const value = await result
  if (typeof value === "string") throw new Error("adapter returned a string ToolResult")
  const newline = value.output.indexOf("\n")
  return JSON.parse(newline === -1 ? value.output : value.output.slice(0, newline))
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

// CD-0017 D4, extended by CD-0196. A session with a managed parent is a
// dispatched worker lane: every concord_* tool refuses before the core is
// invoked, and the refusal is a structured adapter envelope with effect_state
// none. A session the host answers as unparented is a coordinator session and
// passes unchanged — every other tool test in this suite runs through that
// pass path via the default root-session binding.
const recordingRunner = () => {
  const calls: Array<{ argv: string[]; input: string }> = []
  return {
    calls: () => calls,
    async run(argv: string[], input: string) {
      calls.push({ argv, input })
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
}
const bindManagedParentRoute = () => {
  hostControlPlane().bind({
    get: async ({ path }: any) => {
      const id = (path as { id?: string })?.id ?? "session-1"
      const record: Record<string, unknown> = { id, directory: "/worktree" }
      if (id === "session-1") record.parentID = "session-0"
      else record.metadata = { "concord.task_scope": "managed" }
      return { data: record, response: new Response(null, { status: 200 }) }
    },
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
}

test("a lane session's concord call refuses unauthorized with no effect and never reaches the core", async () => {
  bindManagedParentRoute()
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  for (const [tool, call] of [
    [adapter.work_browse, hostCall("list", { product_id: "product-1" })],
    [adapter.work_trace, hostCall("continuity", { work_id: "work-1" })],
    [adapter.domain, hostCall("detail", { domain_id: "agent-surface" })],
    [adapter.work_transition, hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "k" })],
  ] as const) {
    const result: any = await rawHostResult(tool.execute(call as any, contextFor()))
    assertAdapterEnvelope(result)
    expect(result.error.kind).toBe("unauthorized")
    expect(result.error.adapter_reason).toBe("lane_tool_refusal")
    expect(result.error.effect_state).toBe("none")
    expect(result.error.message).toContain("CD-0017 D4")
  }
  expect(runner.calls()).toEqual([])
})

test("a lane session's work_start refuses with the work_start envelope shape", async () => {
  bindManagedParentRoute()
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_start.execute({ title: "t", value_statement: "v", kind: "task", task: "d", idempotency_key: "k" }, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(runner.calls()).toEqual([])
})

test("an unresolvable session scope refuses a concord call closed", async () => {
  hostControlPlane().bind({
    get: async () => ({ data: null, response: new Response(null, { status: 404 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.knowledge.execute(hostCall("search", {}), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(result.error.message).toContain("cannot resolve this session's managed Task scope")
  expect(runner.calls()).toEqual([])
})

// Fail-closed lane boundary (CD-0017 D4): only a host answer that positively
// resolves the caller as an unparented coordinator session passes. An unbound
// control plane, an ancestor HTTP 500, and an ancestor transport failure each
// leave the caller unproven, so every one refuses unauthorized with
// effect_state none and the core never runs.
test("an unbound control plane refuses a concord call closed", async () => {
  hostControlPlane().bind(undefined)
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.adapter_reason).toBe("lane_tool_refusal")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(result.error.message).toContain("the host control plane is unbound")
  expect(runner.calls()).toEqual([])
})

const bindAncestorFailureRoute = (ancestorFailure: () => Promise<RouteResult>) => {
  hostControlPlane().bind({
    get: async ({ path }: any): Promise<RouteResult> => {
      const id = (path as { id?: string })?.id ?? "session-1"
      if (id === "session-1") {
        return { data: { id, directory: "/worktree", parentID: "session-0" }, response: new Response(null, { status: 200 }) }
      }
      return await ancestorFailure()
    },
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
}

const expectLaneRefusal = async (tool: { execute: (args: any, context: any) => Promise<string | { output: string }> }, runner: { calls: () => unknown[] }) => {
  const result: any = await rawHostResult(tool.execute(hostCall("resolve", { product_id: "product-1" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.adapter_reason).toBe("lane_tool_refusal")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(result.error.message).toContain("cannot resolve this session's managed Task scope")
  expect(runner.calls()).toEqual([])
}

test("an ancestor HTTP 500 refuses a concord call closed with no core call", async () => {
  bindAncestorFailureRoute(async () => ({ data: "upstream exploded", response: new Response(null, { status: 500 }) }))
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  await expectLaneRefusal(adapter.product_view, runner)
})

test("an ancestor transport failure refuses a concord call closed with no core call", async () => {
  bindAncestorFailureRoute(() => {
    throw new Error("connection reset by peer")
  })
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  await expectLaneRefusal(adapter.product_view, runner)
})

test("I/O, malformed, timeout, and cancellation outcomes remain schema-valid", async () => {
  const io: any = await runProduct(runnerWithContext({ exitCode: 1, stdout: "", stderr: "broken pipe" }))
  assertAdapterEnvelope(io)
  // A read that fails with an unknown outcome is classified as the transport
  // event it is, reports no effect, and never sends the caller to reconcile
  // a write a read cannot have made.
  expect(io.error.kind).toBe("transport_failure")
  expect(io.error.effect_state).toBe("none")
  expect(io.error.recovery_action.kind).toBe("retry_same_request")

  for (const stdout of ["not-json", "{}\n{}", "{} {}"] as const) {
    const malformed: any = await runProduct(runnerWithContext({ exitCode: 0, stdout, stderr: "" }))
    assertAdapterEnvelope(malformed)
    expect(malformed.error.kind).toBe("malformed_response")
    expect(malformed.error.effect_state).toBe("none")
    expect(malformed.error.recovery_action.kind).toBe("retry_same_request")
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
  error: {
    kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
    consequence_summary: {
      tool: "concord_work_transition", operation: "lifecycle", consequence: "lifecycle",
      operation_digest: `sha256:${"a".repeat(64)}`,
      scope: ["product_id:product-1", "project_ids:project-1", "work_ids:work-1"], versions: ["work:2"], expires_at: "2026-10-01T00:00:00Z",
    },
    details: { approval_ref: "challenge-1", operation_digest: `sha256:${"a".repeat(64)}` },
  },
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
    consequence_summary: { ...approvalChallenge().error.consequence_summary, operation: "workflow_action" },
    details: {
      approval_ref: "challenge-1", operation_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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

test("host publication round-trips check predicate payloads unchanged", async () => {
  const input = {
    work_id: "work-1",
    expected_version: 2,
    action_id: "approve_contract",
    idempotency_key: "idem-check-predicate-1",
    fields: {
      outcome_predicates: [{
        predicate_id: "predicate:primary",
        ordinal: 0,
        outcome_kind: "check",
        outcome_payload: {
          kind: "check",
          check_ref: "check:approve-contract",
          immutable_subject_ref: "commit:approve-contract",
          expected_result: "pass",
        },
      }],
    },
  }
  const published: any = adapter.publishedRequestSchema("concord_work_transition")
  const expandedPublished: any = expandedPublishedRequestSchema(published)
  const actionBranch: any = expandedPublished.oneOf.find((branch: any) => branch.properties.operation.const === "workflow_action" && branch.properties.input.properties.action_id.const === "approve_contract").properties.input
  // CON-412: the items close per kind; each branch binds outcome_kind to
  // outcome_payload.kind and carries that branch's payload inline.
  const itemVariants = actionBranch.properties.fields.properties.outcome_predicates.items.oneOf
  expect(itemVariants.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  expect(itemVariants.map((branch: any) => branch.properties.outcome_payload.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  let sentInput: unknown
  const success = coreEnvelope("concord_work_transition", "workflow_action", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], raw: string) => {
    sentInput = JSON.parse(raw).input
    return success
  }) })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", input), contextFor()))
  expect(result.outcome).toBe("ok")
  expect(sentInput).toEqual(input)
})

test("predicate host-round-trip asks and resubmits the exact restore challenge once", async () => {
  const input = {
    predecessor_id: "work-a",
    predecessor_expected_version: 3,
    successor_id: "work-b",
    successor_expected_version: 3,
    reason: "restore superseded work",
    idempotency_key: "restore-adapter-1",
  }
  const digest = `sha256:${"d".repeat(64)}`
  const challenge = coreEnvelope("concord_work_relate", "restore_superseded", "error", {
    error: {
      kind: "approval_required", retry_safe: false,
      recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: {
        tool: "concord_work_relate", operation: "restore_superseded", consequence: "supersession",
        operation_digest: digest,
        scope: ["product_id:product-a", "product_ids:product-a", "product_ids:product-b", "project_ids:ambient", "work_ids:work-a", "work_ids:work-b", "scope_version:1"],
        versions: ["predecessor:3", "successor:3"], expires_at: "2026-09-14T12:10:00Z",
      },
      details: {
        approval_ref: "e".repeat(64), operation_digest: digest,
      },
    },
  })
  const success = coreEnvelope("concord_work_relate", "restore_superseded", "ok", {
    result: { changed_refs: [], next_valid_intents: [] },
    changed_refs: [],
    next_valid_intents: [],
  })
  const sentInputs: unknown[] = []
  let approvals = 0
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], raw: string, _signal: AbortSignal, calls: number) => {
    if (calls > 1) sentInputs.push(JSON.parse(raw).input)
    return calls === 2 ? challenge : success
  }) })
  const result: any = await rawHostResult(adapter.work_relate.execute(hostCall("restore_superseded", input), contextFor(async () => { approvals++ })))
  expect(result.outcome).toBe("ok")
  expect(approvals).toBe(1)
  expect(sentInputs).toEqual([input, { ...input, approval: { approval_ref: "e".repeat(64) } }])
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

test("confirm_premise refusals come from the core, not the adapter", async () => {
  // The core owns question admission. It validates selected_choice and
  // decision_context_digest itself, one field per refusal, and when the
  // investigation-artifact gate withholds the question it names the missing
  // artifact — a state the adapter cannot see. Any adapter-authored
  // confirm_premise refusal can only mask the authoritative one, so every
  // call reaches the core and the caller sees the core's own message.
  const artifactRefusal = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "missing_evidence", retry_safe: false, recovery_action: { kind: "provide_evidence" }, effect_state: "none", message: "operator question requires a recorded investigation artifact naming a current Domain of the Product and another work item" },
  })
  const first = runnerWithContext(artifactRefusal)
  const result: any = await runWorkflowAction(first,
    { work_id: "work-1", expected_version: 2, action_id: "confirm_premise", idempotency_key: "idem-gate" })
  expect(first.calls()).toBe(2)
  expect(result.outcome).toBe("error")
  expect(result.origin).toBe("core")
  expect(result.error.message).toContain("investigation artifact")

  const digest = "sha256:" + "b".repeat(64)
  for (const input of [
    { decision_context_digest: digest },
    { selected_choice: "revise", decision_context_digest: digest },
    { selected_choice: "confirm" },
    { selected_choice: "confirm", decision_context_digest: 7 },
    { selected_choice: "confirm", decision_context_digest: "sha256:NOTHEX" },
  ]) {
    const runner = runnerWithContext(artifactRefusal)
    const perCase: any = await runWorkflowAction(runner,
      { work_id: "work-1", expected_version: 2, action_id: "confirm_premise", idempotency_key: "idem-gate", ...input })
    expect(runner.calls(), JSON.stringify(input)).toBe(2)
    expect(perCase.origin, JSON.stringify(input)).toBe("core")
    expect(perCase.error.message, JSON.stringify(input)).toContain("investigation artifact")
  }
})

const coreEnvelope = <T extends Record<string, unknown>>(tool: string, operation: string, outcome: string, fields: T) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool, operation, ...((contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any)?.query_id ? { query_id: (contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any).query_id } : {}), outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

for (const blockedVerb of ["project-resolve", "invoke"] as const) {
  for (const interruption of ["deadline", "deadline-as-abort-error", "caller-cancellation"] as const) {
    test(`worker recovery ${interruption} during ${blockedVerb} preserves its effect boundary`, async () => {
      const controller = new AbortController()
      const calls: string[] = []
      const signals: AbortSignal[] = []
      let abortedAt: number | null = null
      adapter.configureConcordAdapter({ runner: { async run(argv, _input, signal) {
        calls.push(argv[1])
        signals.push(signal)
        if (signal.aborted) throw signal.reason
        if (argv[1] !== blockedVerb) return { exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }
        return await new Promise<{ exitCode: number; stdout: string; stderr: string }>((resolve, reject) => {
          const abort = () => {
            abortedAt = performance.now()
            clearTimeout(timer)
            reject(interruption === "deadline-as-abort-error" ? new DOMException("transport interrupted", "AbortError") : signal.reason)
          }
          const timer = setTimeout(() => {
            signal.removeEventListener("abort", abort)
            resolve({ exitCode: 0, stdout: "{}", stderr: "" })
          }, 2_000)
          signal.addEventListener("abort", abort, { once: true })
          if (interruption === "caller-cancellation") controller.abort()
          else if (signal.aborted) abort()
        })
      } } })
      const started = performance.now()
      try {
        const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", {
          work_id: "work-1", attempt_id: "attempt-1", task_part_id: "retained-part",
          idempotency_key: "recover-interruption", requested_budget_seconds: 1,
        }), contextFor(() => {}, controller)))
        assertAdapterEnvelope(result)
        expect(result.outcome).toBe("error")
        // An uncertain mutation attempts a readback with the same expired
        // signal; it cannot renew the deadline or start another invocation.
        expect(calls).toEqual(blockedVerb === "project-resolve" ? ["project-resolve"] : ["project-resolve", "invoke", "project-resolve"])
        if (blockedVerb === "project-resolve") {
          expect(result.error.kind, JSON.stringify(result)).toBe(interruption === "caller-cancellation" ? "cancelled" : "timeout")
          expect(result.error.adapter_reason).toBe(interruption === "caller-cancellation" ? "cancelled_no_effect" : "timeout_no_effect")
          expect(result.error.effect_state).toBe("none")
          expect(result.error.retry_safe).toBe(true)
          expect(result.error.recovery_action.kind).toBe("retry_same_request")
        } else {
          expect(result.error.kind).toBe("operation_conflict")
          expect(result.error.adapter_reason).toBe("unknown_effect")
          expect(result.error.effect_state).toBe("possible")
          expect(result.error.retry_safe).toBe(false)
          expect(result.error.recovery_action.kind).toBe("reconcile_operation")
        }
        expect(abortedAt).not.toBeNull()
        expect(abortedAt! - started).toBeLessThan(1_600)
        expect(new Set(signals).size).toBe(1)
        expect(signals[0]).not.toBe(controller.signal)
        expect(signals[0].reason.name).toBe(interruption === "caller-cancellation" ? "AbortError" : "TimeoutError")
      } finally {
        adapter.configureConcordAdapter({ reset: true })
      }
    })
  }
}

for (const blockedRead of [1, 2]) {
  for (const interruption of ["deadline", "caller-cancellation"] as const) {
    test(`worker recovery ${interruption} during host context read ${blockedRead} preserves its cause`, async () => {
      const controller = new AbortController()
      const signals: AbortSignal[] = []
      const calls: string[] = []
      let reads = 0
      let abortedAt: number | null = null
      hostControlPlane().bind({
        get: async ({ signal }) => {
          signals.push(signal!)
          signal!.throwIfAborted()
          if (++reads !== blockedRead) return { data: { id: "session-1", directory: "/worktree" }, response: new Response("{}") }
          return await new Promise<RouteResult>((resolve, reject) => {
            const abort = () => { abortedAt = performance.now(); clearTimeout(timer); reject(signal!.reason) }
            const timer = setTimeout(() => {
              signal!.removeEventListener("abort", abort)
              resolve({ data: { id: "session-1", directory: "/worktree" }, response: new Response("{}") })
            }, 2_000)
            signal!.addEventListener("abort", abort, { once: true })
            if (interruption === "caller-cancellation") controller.abort()
            else if (signal!.aborted) abort()
          })
        },
        post: async () => { throw new Error("context resolution must not create or move a session") },
      })
      adapter.configureConcordAdapter({ runner: { async run(argv) {
        calls.push(argv[1])
        throw new Error("interrupted host context must stop before core transport")
      } } })
      const started = performance.now()
      try {
        const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", {
          work_id: "work-1", attempt_id: "attempt-1", task_part_id: "retained-part",
          idempotency_key: "recover-context-read", requested_budget_seconds: 1,
        }), contextFor(() => {}, controller)))
        assertAdapterEnvelope(result)
        expect(result.error.kind, JSON.stringify(result)).toBe(interruption === "deadline" ? "timeout" : "cancelled")
        expect(result.error.effect_state).toBe("none")
        expect(result.error.recovery_action.kind).toBe("retry_same_request")
        expect(calls).toEqual([])
        expect(new Set(signals).size).toBe(1)
        expect(abortedAt).not.toBeNull()
        expect(abortedAt! - started).toBeLessThan(1_600)
      } finally {
        adapter.configureConcordAdapter({ reset: true })
      }
    })
  }
}

test("worker recovery deadline keeps its identity when the default runner kills a context child", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-context-deadline-"))
  const binary = join(root, "synthetic-core")
  await Bun.write(binary, "#!/usr/bin/env bun\nawait new Promise(resolve => setTimeout(resolve, 2_000))\n")
  await chmod(binary, 0o700)
  configureCoreBinary(binary)
  adapter.configureConcordAdapter({ reset: true })
  const started = performance.now()
  try {
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", {
      work_id: "work-1", attempt_id: "attempt-1", task_part_id: "retained-part",
      idempotency_key: "recover-default-runner", requested_budget_seconds: 1,
    }), contextFor()))
    assertAdapterEnvelope(result)
    expect(result.error.kind, JSON.stringify(result)).toBe("timeout")
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("retry_same_request")
    expect(performance.now() - started).toBeLessThan(1_600)
  } finally {
    configureCoreBinary("concord")
    adapter.configureConcordAdapter({ reset: true })
    await rm(root, { recursive: true, force: true })
  }
})

test("worker recovery bounds a cooperative two-second host read with one caller deadline", async () => {
  const lane = (await import("./generated-agent-lanes")).agentLanes[0]
  const directory = process.cwd()
  const recovery = {
    packet_digest: "sha256:" + "a".repeat(64), worker_worktree: directory,
    coordinator_session: "session-1", attempt_epoch: 1,
    dispatch_event_id: "original-dispatch", lifecycle_state: "dispatched",
    dispatch: {
      attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
      lane_digest: lane.digest, packet_digest: "sha256:" + "a".repeat(64),
      capability_class: lane.capability_class, packet_schema_version: "1.0", report_schema_version: "1.0",
      readback_model: "openai/test-model",
      host_provenance: { digest: "sha256:" + "b".repeat(64), sources: [{ kind: "agent_definition", path: "synthetic-agent.md", sha256: "sha256:" + "c".repeat(64) }] },
    },
  }
  const response = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: recovery },
    changed_refs: [], next_valid_intents: [],
  })
  const signals: AbortSignal[] = []
  const evidence: string[] = []
  let readStarted = false
  let abortedAt: number | null = null
  hostControlPlane().bind({
    get: async ({ url, signal }) => {
      if (url !== SESSION_MESSAGES_ROUTE) return { data: { id: "session-1", directory }, response: new Response("{}") }
      readStarted = true
      return await new Promise<RouteResult>((resolve, reject) => {
        const abort = () => { abortedAt = performance.now(); clearTimeout(timer); reject(signal!.reason) }
        const timer = setTimeout(() => { signal!.removeEventListener("abort", abort); resolve({ data: [], response: new Response("[]") }) }, 2_000)
        signal!.addEventListener("abort", abort, { once: true })
        if (signal!.aborted) abort()
      })
    },
    post: async () => { throw new Error("recovery must not move or create a session") },
  })
  adapter.configureConcordAdapter({ runner: { async run(argv, _input, signal) {
    signals.push(signal)
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }
    if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(response), stderr: "" }
    evidence.push(argv[1])
    throw new Error("deadline must stop before worker evidence")
  } } })
  const started = performance.now()
  try {
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", {
      work_id: "work-1", attempt_id: "attempt-1", task_part_id: "retained-part",
      idempotency_key: "recover-deadline", requested_budget_seconds: 1,
    }), contextFor(() => {}, new AbortController(), directory)))
    expect(result.error.kind, JSON.stringify(result)).toBe("timeout")
    expect(readStarted).toBe(true)
    expect(result.error.effect_state).toBe("none")
    expect(abortedAt).not.toBeNull()
    expect(abortedAt! - started).toBeLessThan(1_600)
    expect(new Set(signals).size).toBe(1)
    expect(evidence).toEqual([])
  } finally {
    adapter.configureConcordAdapter({ reset: true })
  }
})

test("large membership approval uses only the consequence summary bindings", async () => {
  const scope = [
    "product_id:product-1",
    ...Array.from({ length: 9 }, (_, i) => `product_ids:product-${i + 1}`),
    ...Array.from({ length: 13 }, (_, i) => `project_ids:project-${i + 1}`),
    "scope_version:1", "work_ids:work-1",
  ].sort()
  const summary = {
    tool: "concord_work_relate", operation: "set_memberships", consequence: "scope",
    operation_digest: `sha256:${"a".repeat(64)}`, scope, versions: ["work:2"], expires_at: "2026-10-01T00:00:00Z",
  }
  const challenge = coreEnvelope(summary.tool, summary.operation, "error", {
    error: {
      kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: summary,
      details: { approval_ref: "a".repeat(64), operation_digest: summary.operation_digest },
    },
  })
  expect(validateGeneratedEnvelope(challenge)).toBe(true)
  const input = {
    work_id: "work-1", expected_version: 2, idempotency_key: "large-memberships",
    memberships: Array.from({ length: 13 }, (_, i) => ({ project_id: `project-${i + 1}`, role: i === 0 ? "primary" : "secondary" })),
  }
  let approvedCall: any
  let asked: any
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], raw: string, _signal: AbortSignal, calls: number) => {
    if (calls === 2) return challenge
    approvedCall = JSON.parse(raw)
    return coreEnvelope(summary.tool, summary.operation, "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })
  }) })
  const result: any = await rawHostResult(adapter.work_relate.execute(hostCall("set_memberships", input), contextFor(async (request: any) => { asked = request })))
  expect(result.outcome).toBe("ok")
  expect(asked.metadata.consequence_summary).toEqual(summary)
  expect(approvedCall.call_envelope.host_approval_assertion.scope).toEqual(scope)
  expect(approvedCall.call_envelope.host_approval_assertion.versions).toEqual(summary.versions)
  expect(approvedCall.input).toEqual({ ...input, approval: { approval_ref: "a".repeat(64) } })
})

test("approval refuses mismatched summary identity before asking or resubmitting", async () => {
  for (const changed of [
    { tool: "concord_work_relate" },
    { operation: "set_memberships" },
    { operation_digest: `sha256:${"b".repeat(64)}` },
  ]) {
    const challenge = approvalChallenge()
    Object.assign(challenge.error.consequence_summary, changed)
    const runner = runnerWithContext(challenge)
    let asks = 0
    const result: any = await runTransition(runner, async () => { asks++ })
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.effect_state).toBe("none")
    expect(asks).toBe(0)
    expect(runner.calls()).toBe(2)
  }
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
    scope: ["product_id:product-1", "work_ids:work-1", "work_ids:work-2"],
    versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"],
    resolution_kind: "depends_on", from_work_id: "work-1", to_work_id: "work-2",
    consequence_summary: {
      tool: "concord_work_relate", operation: "resolve_overlap", consequence: "relation",
      operation_digest: digest, scope: ["product_id:product-1", "work_ids:work-1", "work_ids:work-2"],
      versions: ["from:2", "from_contract:1", "to:3", "to_contract:1"], expires_at: "2026-08-20T00:00:00Z",
    },
  })
})

test("client policy grant request asks with the calling client, policy version, and reason", async () => {
  const digest = `sha256:${"d".repeat(64)}`
  const policyVersion = `sha256:${"e".repeat(64)}`
  const scope = ["capabilities:cross_scope", "client_ref:client-1", "policy_version:" + policyVersion, "product_scope:product-2"]
  const challenge = coreEnvelope("concord_work_relate", "client_policy_grant_request", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: {
        tool: "concord_work_relate", operation: "client_policy_grant_request", consequence: "scope",
        operation_digest: digest, scope, versions: [], expires_at: "2026-08-20T00:00:00Z",
      },
      details: {
        approval_ref: "grant-challenge-1", operation_digest: digest,
        summary: "Approve the exact added grants for your own trusted client; every existing grant and the stored principal stay unchanged.",
        client_ref: "client-1", policy_version: policyVersion,
        reason: "dependent work claims a cross-Product worktree",
      } },
  })
  const success = coreEnvelope("concord_work_relate", "client_policy_grant_request", "ok", {
    result: { client_ref: "client-1", policy_version: policyVersion, added_capabilities: ["cross_scope"], added_product_scope: ["product-2"], added_project_scope: [], added_agent_scope: [] },
    changed_refs: [{ entity_kind: "trusted_client", id: "client-1", version: policyVersion }], next_valid_intents: [],
  })
  let calls = 0
  const submitted: any[] = []
  const runner = { async run(_argv: string[], input: any) {
    calls++
    submitted.push(input)
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? challenge : success), stderr: "" }
  } }
  let askMetadata: any
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_relate.execute(hostCall("client_policy_grant_request", {
    capabilities: ["cross_scope"], product_scope: ["product-2"], project_scope: [], agent_scope: [],
    reason: "dependent work claims a cross-Product worktree", idempotency_key: "grant-request-adapter-1",
  }), contextFor(async (request: any) => { askMetadata = request.metadata })))
  expect(result.outcome).toBe("ok")
  expect(askMetadata).toEqual({
    approval_ref: "grant-challenge-1", operation_digest: digest,
    summary: "Approve the exact added grants for your own trusted client; every existing grant and the stored principal stay unchanged.",
    scope, versions: [],
    client_ref: "client-1", policy_version: policyVersion,
    reason: "dependent work claims a cross-Product worktree",
    consequence_summary: {
      tool: "concord_work_relate", operation: "client_policy_grant_request", consequence: "scope",
      operation_digest: digest, scope, versions: [], expires_at: "2026-08-20T00:00:00Z",
    },
  })
  // The resubmission is the same request with only the approval binding added,
  // so the core's digest check sees the approved arguments, not edited ones.
  const finalCall = JSON.parse(submitted[2])
  expect(finalCall.input.approval).toEqual({ approval_ref: "grant-challenge-1" })
  expect(finalCall.input.capabilities).toEqual(["cross_scope"])
  expect(finalCall.call_envelope.host_approval_assertion.challenge_ref).toBe("grant-challenge-1")
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
  bindSessionRoutes({ sessions: [] })
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

test("approval requests suspend under ask policy before resubmission", async () => {
  const challenge = approvalChallenge()
  const digest = challenge.error.details.operation_digest
  const runner = runnerWithContext((_argv: string[], _input: string, _signal: AbortSignal, calls: number) => calls === 2 ? challenge : approvalSuccess())
  let notifyPrompt!: (request: any) => void
  const prompted = new Promise<any>((resolve) => { notifyPrompt = resolve })
  let approve!: () => void
  const decision = new Promise<void>((resolve) => { approve = resolve })
  adapter.configureConcordAdapter({ runner })
  const invocation = rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
    work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "ask-policy-1",
  }), contextFor(async (request: any) => {
    // OpenCode evaluates policy per pattern; an empty list returns without a prompt.
    if (request.patterns.length === 0) return
    notifyPrompt(request)
    await decision
  })))
  let result: any
  try {
    const phase = await Promise.race([
      prompted.then((request) => ({ kind: "prompted", request })),
      invocation.then(() => ({ kind: "returned", request: null })),
    ])
    expect(phase.kind).toBe("prompted")
    expect(phase.request.patterns).toEqual([digest])
    expect(phase.request.always).toEqual([])
    expect(runner.calls()).toBe(2)
  } finally {
    approve()
    result = await invocation
  }
  expect(result.outcome).toBe("ok")
  expect(runner.calls()).toBe(3)
})

test("approval requests respect deny policy without resubmission", async () => {
  const challenge = workflowActionChallenge("approve_contract")
  const runner = runnerWithContext((_argv: string[], _input: string, _signal: AbortSignal, calls: number) => calls === 2 ? challenge : { ...approvalSuccess(), operation: "workflow_action" })
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", {
    work_id: "work-1", expected_version: 2, action_id: "approve_contract", idempotency_key: "deny-policy-1",
  }), contextFor(async (request: any) => {
    if (request.patterns.length === 0) return
    throw new Error("host policy denies this approval")
  })))
  expect(result.error?.kind).toBe("cancelled")
  expect(result.error?.effect_state).toBe("none")
  expect(runner.calls()).toBe(2)
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
  expect(requests[1].call_envelope.host_approval_assertion.scope).toEqual(["product_id:product-1", "project_ids:project-1", "work_ids:work-1"])
  expect(requests[1].call_envelope.host_approval_assertion.versions).toEqual(["work:2"])
  expect(requests[1].call_envelope.host_approval_assertion.signature).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.nonce).toBeUndefined()
})

test("a challenge-free approval refusal reaches the operator unchanged", async () => {
  // allOf[5] of the envelope schema binds approval_required to the
  // request_approval recovery action and requires nothing else, so the store
  // sites that refuse approval without minting a challenge are
  // contract-conformant. Only the allOf[16]/allOf[17] consequence_summary
  // and details.approval_ref pair marks a minted challenge.
  const challengeFree = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none", message: "operator takeover required" },
  })
  expect(validateGeneratedEnvelope(challengeFree)).toBe(true)
  let calls = 0
  let approvals = 0
  const runner = { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(challengeFree), stderr: "" }
    throw new Error("no resubmission may follow a challenge-free refusal")
  } }
  const result: any = await runWorkflowAction(
    runner,
    { work_id: "work-1", expected_version: 2, action_id: "approve_contract", idempotency_key: "idem-challenge-free-1" },
    async () => { approvals++ },
  )
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("approval_required")
  expect(result.error.message).toBe("operator takeover required")
  expect(result.error.recovery_action.kind).toBe("request_approval")
  expect(approvals).toBe(0)
  expect(calls).toBe(2)
})

test("a minted challenge without its workflow metadata still refuses as malformed", async () => {
  const minted: any = workflowActionChallenge("approve_contract")
  delete minted.error.details.operation_digest
  const result: any = await runWorkflowAction(
    challengeRunner(minted),
    { work_id: "work-1", expected_version: 2, action_id: "approve_contract", idempotency_key: "idem-minted-1" },
    async () => {},
  )
  expect(result.outcome).not.toBe("ok")
  expect(result.error.kind).toBe("malformed_response")
  expect(result.error.adapter_reason).toBe("malformed_core_response")
  expect(result.error.message).toBe("core approval challenge lacked exact workflow metadata")
})

test("workflow premise approval asks with exact checkpoint metadata and no human identity", async () => {
  const requests: any[] = []
  const challenge = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
    consequence_summary: { ...approvalChallenge().error.consequence_summary, operation: "workflow_action", versions: ["contract:1", "work:7"] },
    details: {
      approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64),
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
  expect(askMetadata).toEqual({ approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64), scope: challenge.error.consequence_summary.scope, versions: challenge.error.consequence_summary.versions, work_id: "work-1", action_id: "confirm_premise", contract_version: "1", selected_choice: "confirm", decision_context_digest: "sha256:" + "b".repeat(64), premise_summary: "Ship the approved workflow premise.", consequence_summary: challenge.error.consequence_summary })
  expect(requests[1].call_envelope.host_approval_assertion.operator_principal_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_agent_ref).toBeUndefined()
  expect(requests[1].call_envelope.host_approval_assertion.operator_session_ref).toBeUndefined()
})

// The host tool routes dispatch_worker to the lane dispatcher, so the generic
// transport below meets the escalated dispatch challenge through the lane
// path's internal invoke. The challenge the core mints for an escalated
// dispatch_worker carries the failed attempt bindings; this test drives that
// exact challenge shape through the generic approval round trip the lane path
// depends on.
test("escalated correction challenge round-trips with the failed attempt bindings", async () => {
  const challenge = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
    consequence_summary: {
      ...approvalChallenge().error.consequence_summary, operation: "workflow_action",
      scope: ["failed_attempt_id:attempt:work-1:3", "product_id:product-1", "project_ids:project-1", "work_ids:work-1"],
      versions: ["contract:1", "failed_attempt_epoch:3", "work:7"],
    },
    details: {
      approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64),
      work_id: "work-1", action_id: "dispatch_worker", contract_version: "1", selected_choice: "", decision_context_digest: "", premise_summary: "approved retry objective",
    } },
  })
  const success = coreEnvelope("concord_work_transition", "workflow_action", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })
  const requests: any[] = []
  let calls = 0
  const runner = { async run(_argv: string[], input: string) {
    calls++
    if (calls > 1) requests.push(JSON.parse(input))
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? challenge : success), stderr: "" }
  } }
  let askMetadata: any
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", { work_id: "work-1", expected_version: 7, action_id: "approve_contract", idempotency_key: "escalated-challenge-transport" }), contextFor(async (request: any) => { askMetadata = request.metadata })))
  expect(result.outcome).toBe("ok")
  expect(askMetadata).toEqual({ approval_ref: "challenge-1", operation_digest: "sha256:" + "a".repeat(64), scope: challenge.error.consequence_summary.scope, versions: challenge.error.consequence_summary.versions, work_id: "work-1", action_id: "dispatch_worker", contract_version: "1", selected_choice: "", decision_context_digest: "", premise_summary: "approved retry objective", consequence_summary: challenge.error.consequence_summary })
  expect(requests[1].call_envelope.host_approval_assertion.scope).toEqual(["failed_attempt_id:attempt:work-1:3", "product_id:product-1", "project_ids:project-1", "work_ids:work-1"])
  expect(requests[1].call_envelope.host_approval_assertion.versions).toEqual(["contract:1", "failed_attempt_epoch:3", "work:7"])
})

test("a metadata-less escalation refusal stays a core refusal without asking the operator", async () => {
  // The pre-repair core answered an escalated dispatch with approval_required
  // and no challenge details. The adapter must not put an unbindable approval
  // in front of the operator, so it preserves the typed core refusal without
  // an ask.
  const refusal = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none" },
  })
  let calls = 0
  const runner = { async run() {
    calls++
    if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(refusal), stderr: "" }
  } }
  let asks = 0
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", { work_id: "work-1", expected_version: 7, action_id: "approve_contract", idempotency_key: "escalated-dead-end" }), contextFor(async () => { asks++ })))
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  expect(result.origin).toBe("core")
  expect(result.error.kind).toBe("approval_required")
  expect(asks).toBe(0)
  expect(calls).toBe(2)
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
  const linked: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
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
  const result: any = await rawHostResult(adapter.work_start.execute(validTask as any, landedContextFor()))
  expect(result.outcome).toBe("ok")
})

// CD-0098 D1/D2/D3/D4. Work start moves the calling session to the worktree it
// claimed. These tests pin the route's contract rather than a launch's: the
// claim exists before the move, an absent route refuses with no fallback, and
// success is refused unless the host reports the session in the claimed
// worktree. No step records intent ahead of its effect, so there is no
// partial outcome and no rollback: a replay under the same key converges.
const WORKTREE = "/data/worktrees/project-1/work-1"

// The work_start landing gate (issue #1322) admits success only when the
// calling tool context resolves inside the claimed worktree — the posture of a
// replay that runs after the context has landed. The bare contextFor() keeps
// the pre-move directory and drives the metadata-only refusals.
const landedContextFor = (): any => contextFor(() => {}, new AbortController(), WORKTREE)

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
    if (command === "invoke") return { exitCode: 0, stdout: consumeNoHandoffAnswer(), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

// The typed Project-handoff consume rides the boot flow after the verified
// landing. The ordinary boot state — no handoff addresses this Project — is
// a typed unknown_scope refusal the consume treats as quiet, so flows that
// are not about handoffs carry no warning line.
const consumeNoHandoffAnswer = () =>
  JSON.stringify({
    schema_version: "1.0",
    request_id: "core-answer",
    origin: "core",
    tool: "concord_work_transition",
    operation: "project_handoff_consume",
    outcome: "error",
    manifest_digest: manifestDigest,
    resolved_scope: null,
    authority: "authoritative",
    freshness: null,
    source_version_watermark: [],
    ordering_keys: [],
    next_cursor: null,
    omissions: [],
    warnings: [],
    evidence_refs: [],
    replayed: false,
    error: { kind: "unknown_scope", retry_safe: false, recovery_action: { kind: "none" }, effect_state: "none", message: "no recorded project handoff addresses this Project" },
  }) + "\n"

// bindRetargetRoute stands in for the host's control plane. A test drives the
// route's answers directly, so the contract is exercised without a server.
// The patch carries two body shapes: manageSession writes metadata, and the
// session goal title writes { title }; `titleStatus` turns the title write
// into a refusal so its best-effort branch stays observable.
const bindRetargetRoute = (options: { moveStatus?: number; moveBody?: string; landedDirectory?: string; titleStatus?: number; unbound?: boolean } = {}) => {
  const moved: Array<Record<string, unknown>> = []
  const titles: string[] = []
  let metadata: Record<string, unknown> = {}
  if (options.unbound) {
    hostControlPlane().bind(undefined)
    return { moved, titles }
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
      const patchBody = body as { metadata?: Record<string, unknown>; title?: string }
      if (typeof patchBody.title === "string") {
        if (options.titleStatus !== undefined) return { response: new Response(null, { status: options.titleStatus }) }
        titles.push(patchBody.title)
        return { response: new Response(null, { status: 200 }) }
      }
      metadata = patchBody.metadata as Record<string, unknown>
      return { response: new Response(null, { status: 200 }) }
    },
  })
  return { moved, titles }
}

test("work start moves the calling session into the claimed worktree", async () => {
  const { moved, titles } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(result).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1", agent: "agent-1" })
  expect(await hostControlPlane().taskScope("session-1")).toBe("managed")
  // The claim exists before the session moves, so a failed move leaves a
  // resumable claim rather than a moved session with none. A capture boot
  // renders no addressed handoff, so it issues no consume invoke.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  // The landing is confirmed, so the session title names the goal the
  // session-prepare contract derived.
  expect(titles).toEqual(["Goal: Add atomic start"])
  // session-prepare verifies the ACTIVE host agent and derives; it carries
  // no process identity and records nothing.
  expect(JSON.parse(calls[2].input)).toEqual({ product_id: "product-1", work_id: "work-1", task: bootstrapArgs.task, agent: "agent-1" })
  // No launch: the adapter never spawns a host session for the work.
  expect(calls.some(({ argv }) => argv[1] === "session-exec" || argv[0] === "opencode")).toBe(false)
})

// The move notice replaces the bare runs-in line: the envelope output names
// the new path, points reads, edits, and the shell working directory under
// it, and marks the stale surfaces, so the agent retargets its next actions.
test("work start replaces the bare move line with the move notice", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.output).toContain(`Concord moved this session to ${WORKTREE}`)
  expect(result.output).toContain(`Use paths under ${WORKTREE} for reads, edits, and the shell working directory`)
  expect(result.output).toContain("The <env> working directory and the pre-move checkout are stale until the next turn")
  expect(result.output).not.toContain("This session now runs in")
  // work_start succeeds only once the tool context runs in the worktree, so
  // it arms no turn-move boundary and its notice names none.
  expect(result.output).not.toContain("turn-move boundary")
  expect(result.output).not.toContain("replay")
})

// A refused move records no notice: the notice states a confirmed move, and
// the refusal already names its own effect and recovery.
test("a refused work start records no move notice", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
    "work-bootstrap": () => ({ exitCode: 1, stdout: "", stderr: "concord work-bootstrap: invalid_operation: cannot chain work_start from live work item work-origin" }),
  }) })
  const refused: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(refused.outcome).toBe("error")
  expect(JSON.stringify(refused)).not.toContain("Concord moved this session")
  expect(takeMoveNotice("session-1")).toBeNull()
})

// The confirmed landing arms the session's active claimed worktree, so a later
// dispatch compares the host's answer against a record the host does not own.
test("work start arms the claimed worktree after the confirmed landing", async () => {
  try {
    bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
    expect(result.outcome).toBe("ok")
    expect(armedClaimedWorktree("session-1")).toBe(WORKTREE)
  } finally {
    clearClaimedWorktree("session-1")
  }
})

// A refused move never landed, so nothing may be armed: the dispatch check
// must stay exactly as it was for this session. Issue #1322: the refusal still
// records the pending target, so the dispatch gate fails closed for the
// retarget instead of staying open on a prior claim.
test("work start arms nothing and records the pending target when the landing mismatch refuses", async () => {
  try {
    bindRetargetRoute({ landedDirectory: "/elsewhere" })
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("session_directory_mismatch")
    expect(armedClaimedWorktree("session-1")).toBeNull()
    expect(unlandedClaimedWorktree("session-1")).toBe(WORKTREE)
  } finally {
    clearClaimedWorktree("session-1")
  }
})

// Issue #1322: a host can accept the retarget and answer the claimed worktree
// on the read-back while the session's tools still run in the pre-move
// directory. That metadata-only move reports no success and arms nothing; the
// durable claim stays adoptable by a replay. The refusal separates the
// unconfirmed landing from an armed turn-move boundary, names the next-turn
// recovery opportunity, and keeps the declared replay behind an actual
// target-context confirmation.
test("work start refuses a metadata-only move whose tool context has not landed", async () => {
  try {
    bindRetargetRoute({ landedDirectory: WORKTREE })
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    // The bare context runs in /worktree, not the claimed worktree, so the
    // confirmed read-back alone is a metadata-only move.
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("session_directory_mismatch")
    expect(result.error.message).toContain(WORKTREE)
    expect(result.error.message).toContain("/worktree")
    // The refusal distinguishes the unconfirmed landing from the armed
    // question/dispatch boundary: it asserts neither.
    expect(result.error.message).toContain("this refusal arms no turn-move boundary")
    expect(result.error.message).not.toContain("A turn-move boundary is active")
    // The next operator message is the recovery opportunity, and the replay
    // stays behind an actual target-context confirmation.
    expect(result.error.message).toContain("ask the operator to send the next message")
    expect(result.error.message).toContain("not placement proof")
    expect(result.error.message).toContain(`actually resolves in ${JSON.stringify(WORKTREE)}`)
    expect(result.error.message).toContain("replay this same work_start request")
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("retry_same_request")
    // The work item and worktree that exist ride along so the replay's target is visible.
    expect(result.work_id).toBe("work-1")
    expect(result.worktree_path).toBe(WORKTREE)
    // The declared refusal leaves the claimed worktree unarmed for dispatch,
    // and records the move as unlanded so the dispatch gate stays closed.
    expect(armedClaimedWorktree("session-1")).toBeNull()
    expect(unlandedClaimedWorktree("session-1")).toBe(WORKTREE)
    // The bootstrap ran and stays durable, so the replay adopts it.
    expect(calls.map(({ argv }) => argv[1])).toContain("work-bootstrap")
  } finally {
    clearClaimedWorktree("session-1")
  }
})

test("an unlanded resume does not deny a boundary armed by an earlier move", async () => {
  try {
    bindRetargetRoute({ landedDirectory: WORKTREE })
    adapter.configureConcordAdapter({ runner: retargetRunner([]) })
    armTurnMoveBoundary("session-1")
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(result.error.kind).toBe("session_directory_mismatch")
    expect(dispatchRequiresNextTurn("session-1")).toBe(true)
    expect(result.error.message).not.toContain("no turn-move boundary is active")
    expect(result.error.message).toContain("this refusal arms no turn-move boundary")
  } finally {
    clearClaimedWorktree("session-1")
    clearTurnMoveBoundary("session-1")
  }
})

// The replay runs once the host tool context resolves inside the claimed
// worktree: the same request adopts the durable claim, the move is a no-op,
// and the confirmed landing reports success and arms the claim.
test("a replay after the tool context lands reports success and arms the claim", async () => {
  try {
    bindRetargetRoute({ landedDirectory: WORKTREE })
    adapter.configureConcordAdapter({ runner: retargetRunner([]) })
    const refused: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(refused.outcome).toBe("error")
    expect(armedClaimedWorktree("session-1")).toBeNull()
    expect(unlandedClaimedWorktree("session-1")).toBe(WORKTREE)
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const replay: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
    expect(replay).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1" })
    expect(armedClaimedWorktree("session-1")).toBe(WORKTREE)
    expect(unlandedClaimedWorktree("session-1")).toBeNull()
  } finally {
    clearClaimedWorktree("session-1")
  }
})

// A core that answers session-prepare with any agent other than the one this
// session runs as fails the strict read-back: the move must not happen on an
// agent identity the session does not hold.
test("work start refuses a session-prepare read-back that names another agent", async () => {
  const { moved } = bindRetargetRoute()
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

// Issue #917: a successful work_start names the zellij pane frame. The
// session-prepare contract carries the title alone, so the start-time name is
// the shared pane rendering with fewer fields. The fork sits after every
// refusal point, so a success without ZELLIJ_PANE_ID stays fork-free and a
// failed fork stays a warning.
test("work start renames the zellij pane frame to the work title on success", async () => {
  process.env.ZELLIJ_PANE_ID = "402"
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(result.outcome).toBe("ok")
  // One rename fork, after session-prepare supplied the title and before the
  // gate brief's reads: exactly one per success.
  const renames = calls.filter(({ argv }) => argv[0] === "zellij")
  expect(renames).toHaveLength(1)
  expect(renames[0].argv).toEqual(["zellij", "action", "rename-pane", "-p", "402", "Add atomic start"])

  const resumeCalls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(resumeCalls) })
  const resumed: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
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
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
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
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(calls.some(({ argv }) => argv[0] === "zellij")).toBe(false)
})

test("a failed pane rename is a warning in the tool output and never fails work_start", async () => {
  process.env.ZELLIJ_PANE_ID = "9"
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  // The exitCode-1 fork and the throwing fork are both warnings: the envelope
  // reports the completed start unchanged in either mode, and the warning
  // rides the tool result output where the agent can respond to it.
  const failing = { async run(argv: string[], input: string, signal: AbortSignal, options?: any) {
    if (argv[0] === "zellij") return { exitCode: 1, stdout: "", stderr: "no such pane" }
    return retargetRunner(calls).run(argv, input, signal, options)
  } }
  adapter.configureConcordAdapter({ runner: failing })
  const refused: any = await adapter.work_start.execute(bootstrapArgs, landedContextFor())
  expect(envelopeLine(refused.output)).toMatchObject({ outcome: "ok", work_id: "work-1" })
  expect(refused.output).toContain("Concord could not rename the pane frame to the work title: exit 1.")

  const throwing = { async run(argv: string[], input: string, signal: AbortSignal, options?: any) {
    if (argv[0] === "zellij") throw new Error("zellij is absent")
    return retargetRunner(calls).run(argv, input, signal, options)
  } }
  adapter.configureConcordAdapter({ runner: throwing })
  const thrown: any = await adapter.work_start.execute(bootstrapArgs, landedContextFor())
  expect(envelopeLine(thrown.output)).toMatchObject({ outcome: "ok", work_id: "work-1" })
  expect(thrown.output).toContain("Concord could not rename the pane frame to the work title: the fork failed.")
})

// The session title names the goal for the session list and the compaction
// hook. The write sits after the confirmed landing, so an absent route or a
// failed call is a warning in the tool output that leaves the completed start
// untouched.
test("a refused session goal title write warns in the tool output and never fails work_start", async () => {
  for (const titleStatus of [404, 405, 500]) {
    bindRetargetRoute({ titleStatus })
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result: any = await adapter.work_start.execute(bootstrapArgs, landedContextFor())
    expect(envelopeLine(result.output)).toMatchObject({ outcome: "ok", work_id: "work-1" })
    expect(result.output).toContain("Concord could not write the session goal title: the session title route is absent or refused the write.")
  }
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
  branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: 2 },
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
    if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
    // A handoff-free resume renders no project_handoff, so the boot issues no
    // consume invoke at all; the leg answers only an unexpected call.
    if (command === "invoke") return { exitCode: 0, stdout: consumeNoHandoffAnswer(), stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

test("work start resume derives the entry by work_id and moves the session", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(await hostControlPlane().taskScope("session-1")).toBe("managed")
  expect(result).toMatchObject({ outcome: "ok", product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: WORKTREE, agent: "agent-1", session_id: "session-1" })
  // The active resume read stays journal-free, the verified landing records
  // itself afterwards through the claim-landing verb, and a handoff-free
  // boot issues no Project-handoff consume at all. The post-landing
  // handoff re-read (CD-0182 D5 amendment) still runs once, because a cold
  // second session of the receiving Project can only render the shared
  // bind after its landing records placement — and renders nothing here.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume", "session-prepare", "claim-landing", "work-resume"])
  expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", work_id: "work-1", session_ref: "session-1" })
  // A resume carries no task; session-prepare still verifies the active
  // agent and the worktree.
  expect(JSON.parse(calls[2].input)).toEqual({ product_id: "product-1", work_id: "work-1", task: "", agent: "agent-1" })
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
})

// The typed refusal contract (work-resume commandSpecs): a deterministic
// refusal exits 2 and stays a genuine non-retry refusal through work_start —
// contact_operator, retry_safe false, the complete core diagnostic preserved,
// no movement, and no claimed worktree armed — while an ordinary failure exit
// keeps the declared retry route.
test("work start resume classifies the typed refusal exit without retry or movement", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 2, stdout: "", stderr: "concord work-resume: store: work_resume: invalid_operation: cannot resume terminal work item work-1 (completed)" }),
  }) })
  const refused: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(refused.outcome).toBe("error")
  expect(refused.error.kind).toBe("resume_failure")
  expect(refused.error.message).toContain("cannot resume terminal work item work-1")
  expect(refused.error.effect_state).toBe("none")
  expect(refused.error.recovery_action.kind).toBe("contact_operator")
  expect(refused.error.retry_safe).toBe(false)
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
  expect(moved).toEqual([])
  expect(armedClaimedWorktree("session-1")).toBeNull()

  // The dirty-origin refusal keeps its complete core boundary diagnostic and
  // the same non-retry classification: no success, movement, or replay grant.
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 2, stdout: "", stderr: "concord work-resume: store: work_bootstrap: invalid_operation: cannot chain from dirty worktree of work-0" }),
  }) })
  const dirty: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(dirty.outcome).toBe("error")
  expect(dirty.error.kind).toBe("resume_failure")
  expect(dirty.error.message).toContain("concord work-resume: store: work_bootstrap: invalid_operation: cannot chain from dirty worktree of work-0")
  expect(dirty.error.recovery_action.kind).toBe("contact_operator")
  expect(dirty.error.retry_safe).toBe(false)
  expect(calls.some(({ argv }) => argv[1] === "session-prepare" || argv[1] === "claim-landing")).toBe(false)
  expect(moved).toEqual([])
  expect(armedClaimedWorktree("session-1")).toBeNull()
})

test("work start resume keeps an ordinary work-resume failure retryable", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 1, stdout: "", stderr: "concord work-resume: store: work_resume: unavailable: cannot read the work item" }),
  }) })
  const failed: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(failed.outcome).toBe("error")
  expect(failed.error.kind).toBe("resume_failure")
  expect(failed.error.recovery_action.kind).toBe("retry_same_request")
  expect(failed.error.retry_safe).toBe(true)
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
  expect(moved).toEqual([])
})

// The post-landing handoff re-read is the second work-resume call site: its
// typed refusal exit must classify the same way, not collapse into the retry
// route a stale-environment read failure keeps.
test("work start resume classifies the post-landing handoff re-read refusal exit", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  let resumeReads = 0
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
      calls.push({ argv, input, options })
      if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
      const command = argv[1]
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") {
        resumeReads += 1
        if (resumeReads === 1) return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
        return { exitCode: 2, stdout: "", stderr: "concord work-resume: store: work_resume: unknown_scope: work item does not exist" }
      }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    },
  } as never })
  const refused: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(refused.outcome).toBe("error")
  expect(refused.error.kind).toBe("resume_failure")
  expect(refused.error.message).toContain("the post-landing handoff re-read refused: concord work-resume: store: work_resume: unknown_scope: work item does not exist")
  expect(refused.error.recovery_action.kind).toBe("contact_operator")
  expect(refused.error.retry_safe).toBe(false)
  expect(resumeReads).toBe(2)
  // The refused re-read arms no dispatch and opens no handoff consume.
  expect(calls.some(({ argv }) => argv[1] === "invoke")).toBe(false)
  expect(armedClaimedWorktree("session-1")).toBeNull()
})

test("work start resume keeps a retryable post-landing re-read failure on the replay route", async () => {
  bindRetargetRoute()
  let resumeReads = 0
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
      if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
      const command = argv[1]
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") {
        resumeReads += 1
        if (resumeReads === 1) return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
        return { exitCode: 1, stdout: "", stderr: "concord work-resume: store: work_resume: unavailable: cannot read the work item" }
      }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    },
  } as never })
  const failed: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(failed.outcome).toBe("error")
  expect(failed.error.kind).toBe("resume_failure")
  expect(failed.error.message).toContain("the post-landing handoff re-read refused:")
  expect(failed.error.recovery_action.kind).toBe("retry_same_request")
  expect(failed.error.retry_safe).toBe(true)
  expect(resumeReads).toBe(2)
})

test("work start resume reads the landing back after a refusal", async () => {
  bindRetargetRoute({ landedDirectory: "/somewhere-else" })
  adapter.configureConcordAdapter({ runner: resumeRunner([]) })
  const mismatch: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(mismatch.outcome).toBe("error")
  expect(mismatch.error.kind).toBe("session_directory_mismatch")
  expect(mismatch.work_id).toBe("work-1")
  expect(mismatch.worktree_path).toBe(WORKTREE)
})

// The resumed session records itself as the worktree's occupant once its move
// reads back: the read that derived the worktree records nothing (CD-0104 D1),
// so the landing record is what makes worktree_audit_reclaim and
// worktree_reclaim hold the worktree for the session that runs in it.
test("work start resume records the verified landing naming the session, work item, and claimed path", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  const landings = calls.filter(({ argv }) => argv[1] === "claim-landing")
  expect(landings).toHaveLength(1)
  // The landing payload carries the adapter's process pid; the core reads
  // the process start time from the kernel.
  const landingInput = JSON.parse(landings[0].input)
  expect(landingInput.work_id).toBe("work-1")
  expect(landingInput.session_ref).toBe("session-1")
  expect(landingInput.landed_directory).toBe(WORKTREE)
  expect(landingInput.host_pid).toBe(process.pid)

  // A move whose readback names another directory records no landing.
  bindRetargetRoute({ landedDirectory: "/somewhere-else" })
  const mismatchCalls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(mismatchCalls) })
  const mismatch: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, contextFor()))
  expect(mismatch.outcome).toBe("error")
  expect(mismatch.error.kind).toBe("session_directory_mismatch")
  expect(mismatchCalls.some(({ argv }) => argv[1] === "claim-landing")).toBe(false)
})

// The bounded addressed handoff a Project-selected work-resume renders
// (CD-0182 amendment): the boot consumes exactly the rendered handoff, by its
// own id, through the authenticated invoke route, and the consumed bounded
// job rides the result so the receiving session holds its repository job
// without the operator copying context.
const renderedHandoff = () => ({
  handoff_id: "work-1:project-handoff:project-0:project-1:abcd1234abcd1234",
  source_project_id: "project-0",
  bounded_job: "verify the receiving repository's adapter surface",
  changes: ["adapter/opencode: opener route"],
  verification: ["bun test adapter/opencode/ pass"],
  artifact_refs: null,
  blockers: [],
  next_action: "consume the handoff and verify the opener route",
  recorded_at: "2026-09-30T00:00:00Z",
})

const consumeOKAnswer = (handoffID: string) =>
  JSON.stringify(coreEnvelope("concord_work_transition", "project_handoff_consume", "ok", {
    changed_refs: [],
    next_valid_intents: [],
    result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", handoff_id: handoffID, already_consumed: false },
  })) + "\n"

test("work start resume consumes the rendered handoff by id and carries the bounded job", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  const consumeInput: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
      calls.push({ argv, input, options })
      if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
      const command = argv[1]
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") return { exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), project_handoff: renderedHandoff() }), stderr: "" }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      if (command === "invoke") {
        const parsed = JSON.parse(input) as { operation: string; input: Record<string, unknown> }
        expect(parsed.operation).toBe("project_handoff_consume")
        // The generated tool contract requires handoff_id; the boot sends
        // the rendered handoff's own id, never an addressed resolution the
        // contract refuses.
        expect(parsed.input).toMatchObject({ work_id: "work-1", handoff_id: renderedHandoff().handoff_id })
        consumeInput.push(calls[calls.length - 1])
        return { exitCode: 0, stdout: consumeOKAnswer(renderedHandoff().handoff_id), stderr: "" }
      }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    },
  } as never })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.project_handoff).toEqual(renderedHandoff())
  expect(result.output).toContain("verify the receiving repository's adapter surface")
  expect(consumeInput).toHaveLength(1)
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
})

// A cold second session of the receiving Project (CD-0182 D5 amendment):
// the pre-landing resume read renders no handoff — the frontier's consumed
// bind renders only to a session whose verified placement stands — so the
// boot must re-read the addressed handoff after the claim landing records
// placement, render the shared bind's bounded job, and resolve the standing
// bind (already_consumed) instead of booting admitted with no job.
test("work start resume re-reads the handoff after the landing for a cold second session", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  const consumeInput: RetargetCall[] = []
  let resumeReads = 0
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
      calls.push({ argv, input, options })
      if (argv[0] === "zellij") return { exitCode: 0, stdout: "", stderr: "" }
      const command = argv[1]
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") {
        resumeReads += 1
        // The first read precedes the landing and renders nothing; the
        // post-landing re-read renders the shared bind another session
        // of the Project consumed.
        if (resumeReads === 1) return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
        return { exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), project_handoff: renderedHandoff() }), stderr: "" }
      }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      if (command === "invoke") {
        const parsed = JSON.parse(input) as { operation: string; input: Record<string, unknown> }
        expect(parsed.operation).toBe("project_handoff_consume")
        expect(parsed.input).toMatchObject({ work_id: "work-1", handoff_id: renderedHandoff().handoff_id })
        consumeInput.push(calls[calls.length - 1])
        return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "project_handoff_consume", "ok", {
          changed_refs: [],
          next_valid_intents: [],
          result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", handoff_id: renderedHandoff().handoff_id, already_consumed: true },
        })) + "\n", stderr: "" }
      }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    },
  } as never })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.project_handoff).toEqual(renderedHandoff())
  expect(result.output).toContain("verify the receiving repository's adapter surface")
  expect(resumeReads).toBe(2)
  expect(consumeInput).toHaveLength(1)
  // The refresh sits between the recorded landing and the consume: the
  // read that rendered the job ran only after placement stood.
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume", "session-prepare", "claim-landing", "work-resume", "invoke"])
})

test("work start resume refuses when the rendered handoff does not bind", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), project_handoff: renderedHandoff() }), stderr: "" }),
    "invoke": () => ({ exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "project_handoff_consume", "error", { error: { kind: "invalid_input", retry_safe: false, recovery_action: { kind: "reread_entities" }, effect_state: "none", message: "the handoff was recorded under contract version 1, but the active contract is version 2" } })) + "\n", stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("resume_failure")
  expect(result.error.message).toContain("did not consume the addressed Project handoff")
  expect(result.error.message).toContain("active contract is version 2")
  // The refused boot arms no claimed worktree for dispatch.
  expect(armedClaimedWorktree("session-1")).toBeNull()
})

test("work start resume refuses when the landing record fails on a handoff-bearing boot", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), project_handoff: renderedHandoff() }), stderr: "" }),
    "claim-landing": () => ({ exitCode: 1, stdout: "", stderr: "concord claim-landing: worktree_ownership_conflict: the claimed worktree is not recorded as occupied by this session" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("resume_failure")
  expect(result.error.message).toContain("did not record this session as the occupant")
  expect(result.error.message).toContain("requires verified placement")
  // No consume is attempted: the unplaced session cannot bind.
  expect(calls.some(({ argv }) => argv[1] === "invoke")).toBe(false)
})

// Occupancy never refuses the move (CD-0104 D5, CD-0119): a landing record the
// core refuses, such as a worktree another live session already occupies, is
// a warning, and the resumed start still succeeds and arms the worktree.
test("work start resume succeeds with a warning when the core refuses the landing record", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "claim-landing": () => ({ exitCode: 1, stdout: "", stderr: "concord claim-landing: worktree_ownership_conflict: the claimed worktree is not recorded as occupied by this session" }),
  }) })
  const raw: any = await adapter.work_start.execute({ work_id: "work-1" }, landedContextFor())
  expect(envelopeLine(raw.output)).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE })
  expect(raw.output).toContain(`Concord did not record this session as the occupant of ${WORKTREE}`)
  expect(raw.output).toContain("replay work_start")
})

const recordedLinearIssue = () => ({
  human_key: "EX-3",
  remote_issue_uuid: "cccccccc-0000-0000-0000-000000000003",
  url: "https://linear.app/example/issue/EX-3",
})

test("work start resume forwards the locally recorded linear_issue unchanged", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), linear_issue: recordedLinearIssue() }), stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.linear_issue).toEqual(recordedLinearIssue())
  expect(result.branch_freshness).toEqual(resumeSuccess().branch_freshness)
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume", "session-prepare", "claim-landing", "work-resume"])
})

test("work start resume omits linear_issue when no identity is recorded", async () => {
  bindRetargetRoute()
  adapter.configureConcordAdapter({ runner: resumeRunner([]) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect("linear_issue" in result).toBe(false)
})

test("work start resume refuses malformed recorded linear_issue before the move", async () => {
  for (const linear_issue of [
    null,
    { human_key: "EX-3", url: recordedLinearIssue().url },
    { ...recordedLinearIssue(), status: "in_progress" },
    { ...recordedLinearIssue(), remote_issue_uuid: [] },
    { ...recordedLinearIssue(), human_key: "invalid" },
    { ...recordedLinearIssue(), url: "http://example.invalid/issue/EX-3" },
  ]) {
    bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
      "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), linear_issue }), stderr: "" }),
    }) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
    expect(result.outcome, JSON.stringify(linear_issue)).toBe("error")
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.message).toContain("strict resume contract")
    expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
    expect(armedClaimedWorktree("session-1")).toBeNull()
  }
})

test("work start resume refuses undeclared response sections before the move", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), unexpected_section: {} }), stderr: "" }),
  }) })
  const refused: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(refused.outcome).toBe("error")
  expect(refused.error.kind).toBe("malformed_response")
  expect(refused.error.message).toContain("strict resume contract")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
  expect(armedClaimedWorktree("session-1")).toBeNull()
})

// The branch freshness sample rides the work-resume result into the envelope,
// so the resuming session sees how far its branch sits behind the origin
// default branch before it builds on it. The strict validator admits the
// exact sampled shape and the typed unknown, and refuses anything outside
// the contract.
test("work start resume passes the branch freshness sample through to the envelope", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: 3 } }), stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.branch_freshness).toEqual({ status: "ok", head_sha: "b".repeat(40), default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: 3 })

  // A failed refresh degrades to the typed unknown with no count, and the
  // start still succeeds.
  adapter.configureConcordAdapter({ runner: resumeRunner([], {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), branch_freshness: { status: "unknown", reason: "fetch_failed" } }), stderr: "" }),
  }) })
  const degraded: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(degraded.outcome).toBe("ok")
  expect(degraded.branch_freshness).toEqual({ status: "unknown", reason: "fetch_failed" })

  // A nested default branch is a valid Git ref name: the sample passes
  // through the strict validator unchanged.
  adapter.configureConcordAdapter({ runner: resumeRunner([], {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref: "origin/release/stable", default_sha: "c".repeat(40), behind_count: 1 } }), stderr: "" }),
  }) })
  const nested: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(nested.outcome).toBe("ok")
  expect(nested.branch_freshness.default_ref).toBe("origin/release/stable")
})

test("work start resume refuses a malformed branch freshness sample", async () => {
  bindRetargetRoute()
  const malformed = [
    { ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: -1 } },
    { ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "short", default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: 0 } },
    { ...resumeSuccess(), branch_freshness: { status: "unknown", reason: "invented_reason" } },
    { ...resumeSuccess(), branch_freshness: { status: "unknown", reason: "timeout", behind_count: 4 } },
    { ...resumeSuccess(), branch_freshness: { status: "degraded" } },
    // A sampled SHA is a string field: an array wrapping the hex must fail
    // the strict contract, not coerce through String() into a passing test.
    { ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: ["b".repeat(40)], default_ref: "origin/main", default_sha: "c".repeat(40), behind_count: 0 } },
    { ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref: "origin/main", default_sha: ["c".repeat(40)], behind_count: 0 } },
    // The worktree base SHA is likewise a string field in the same contract.
    { ...resumeSuccess(), worktree: { set_id: "worktree-set-1", path: WORKTREE, branch: "work/work-1", base_sha: ["a".repeat(40)], state: "active" } },
  ]
  const malformedDefaultRefs = ["origin/..", "origin/main.lock", "origin/.hidden", "origin/sp ace", "origin/x.", "origin/a..b", "origin//double", "origin/", "refs/heads/main", "origin/tilde~x"]
  for (const default_ref of malformedDefaultRefs) {
    malformed.push({ ...resumeSuccess(), branch_freshness: { status: "ok", head_sha: "b".repeat(40), default_ref, default_sha: "c".repeat(40), behind_count: 2 } })
  }
  for (const stdout of malformed) {
    adapter.configureConcordAdapter({ runner: resumeRunner([], {
      "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify(stdout), stderr: "" }),
    }) })
    const refused: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
    expect(refused.outcome).toBe("error")
    expect(refused.error.kind).toBe("malformed_response")
    expect(refused.error.message).toContain("strict resume contract")
  }

  // A resume that omits the freshness section fails the strict contract:
  // the core always samples it, and a missing section hides the lag.
  adapter.configureConcordAdapter({ runner: resumeRunner([], {
    "work-resume": () => {
      const { branch_freshness: _omitted, ...without } = resumeSuccess()
      return { exitCode: 0, stdout: JSON.stringify(without), stderr: "" }
    },
  }) })
  const missing: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
  expect(missing.outcome).toBe("error")
  expect(missing.error.kind).toBe("malformed_response")
})

// CD-0182: a resume whose named member Project lives in another repository
// never reaches the host move. The registered session opener probes that the
// target canonical path resolves, then runs the core launch argv without a
// shell and the answer reports its exit status and argv without claiming the
// new session is running; with no opener, an invalid one, or a canonical
// path that no longer resolves, the exact launch command and directory still
// return. selectedPath names project-2's registered canonical path: tests
// that reach the opener run pass a real directory, because the fail-closed
// probe refuses a path the filesystem cannot resolve.
const secondRepoRunner = (calls: RetargetCall[], opener: () => { exitCode: number; stdout: string; stderr: string } | null, selectedPath = "/other-repo") => ({
  async run(argv: string[], input: string, _signal: AbortSignal, _options?: any) {
    calls.push({ argv, input })
    if (argv[0] === "tab-opener") {
      const outcome = opener()
      if (outcome === null) throw new Error("the opener must not run")
      return outcome
    }
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "project-canonical-path") {
      const parsed = JSON.parse(input) as { project_id: string }
      return { exitCode: 0, stdout: JSON.stringify({ project_id: parsed.project_id, canonical_path: parsed.project_id === "project-2" ? selectedPath : "/repo-1" }), stderr: "" }
    }
    // The routing decision needs only canonical paths: worktree-locate, which
    // resolves a default branch ref and a commit, must never run on this route.
    if (argv[1] === "worktree-locate") throw new Error("worktree-locate must not serve the routing decision")
    throw new Error(`unexpected command ${argv.join(" ")}`)
  },
})

test("work start resume routes a second repository through the registered session opener", async () => {
  const selectedRepo = await mkdtemp(join(tmpdir(), "concord-second-repo-"))
  try {
    const calls: RetargetCall[] = []
    adapter.configureSessionOpener(["tab-opener", "--cwd", "{directory}", "--title", "{title}", "--", "{command}"])
    adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => ({ exitCode: 0, stdout: "", stderr: "" }), selectedRepo) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("second_session_opened")
    expect(result.error.retry_safe).toBe(false)
    expect(result.error.effect_state).toBe("none")
    expect(result.work_id).toBe("work-1")
    expect(result.project_id).toBe("project-2")
    expect(result.launch.directory).toBe(selectedRepo)
    expect(result.launch.argv).toEqual(["concord", "zl", "work-1", "--project", "project-2"])
    expect(result.launch.runnable).toBe("concord zl work-1 --project project-2")
    expect(result.opener.exit_code).toBe(0)
    expect(result.opener.argv).toEqual(["tab-opener", "--cwd", selectedRepo, "--title", "work-1", "--", "concord", "zl", "work-1", "--project", "project-2"])
    expect(result.error.message).toContain("does not claim the new session is running")
    expect(result.error.message).toContain("concord_work_start with work_id work-1 and project_id project-2")
    // The branch runs before the probe, the resume read, and the move, so a
    // second-repository resume captures and moves nothing.
    expect(calls.some(({ argv }) => argv[1] === "work-resume" || argv[1] === "session-prepare" || argv[1] === "work-bootstrap")).toBe(false)
  } finally {
    adapter.configureSessionOpener(undefined)
    await rm(selectedRepo, { recursive: true, force: true })
  }
})

// A member Project can hold a valid canonical path while its repository has
// no resolvable default ref, so worktree-locate — which resolves a default
// branch ref and a commit — cannot answer for it. The routing decision needs
// only each Project's canonical repository path and must still reach both
// the opener run and the no-opener command through the canonical-path read.
test("a second-repository route with no resolvable default ref opens through the canonical-path read", async () => {
  const selectedRepo = await mkdtemp(join(tmpdir(), "concord-second-repo-"))
  try {
    const calls: RetargetCall[] = []
    adapter.configureSessionOpener(["tab-opener", "--cwd", "{directory}", "--title", "{title}", "--", "{command}"])
    adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => ({ exitCode: 0, stdout: "", stderr: "" }), selectedRepo) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
    expect(result.error.kind).toBe("second_session_opened")
    expect(result.launch.directory).toBe(selectedRepo)
    const selectedReads = calls.filter(({ argv, input }) => argv[1] === "project-canonical-path" && (JSON.parse(input) as { project_id: string }).project_id === "project-2")
    expect(selectedReads).toHaveLength(1)
    // The mock refuses worktree-locate outright, so reaching this point with
    // a routed session proves the route never asked for a ref resolution.
    expect(calls.some(({ argv }) => argv[1] === "worktree-locate")).toBe(false)
  } finally {
    adapter.configureSessionOpener(undefined)
    await rm(selectedRepo, { recursive: true, force: true })
  }
})

test("a resume into a second repository without an opener returns the exact launch command", async () => {
  const calls: RetargetCall[] = []
  adapter.configureSessionOpener(undefined)
  adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => null) })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
  expect(result.error.kind).toBe("session_opener_unregistered")
  expect(result.launch.directory).toBe("/other-repo")
  expect(result.launch.runnable).toBe("concord zl work-1 --project project-2")
  expect(result.error.message).toContain("No session opener is registered")
  expect(result.opener).toBeUndefined()
})

test("an invalid registered opener refuses naming the invalid field and still returns the command", async () => {
  for (const [template, fragment] of [
    [["tab-opener", "{command} {command}"], "session_opener[1] embeds {command}"],
    [["tab-opener", "--cwd", "{path}", "{command}"], "unknown placeholder {path}"],
    [["tab-opener", "--flag"], "exactly once as a whole element"],
    ["not-an-array", "session_opener is not an array"],
  ] as Array<[unknown, string]>) {
    const calls: RetargetCall[] = []
    adapter.configureSessionOpener(template)
    adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => null) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
    expect(result.error.kind).toBe("invalid_session_opener")
    expect(result.error.message).toContain(fragment)
    expect(result.launch.runnable).toBe("concord zl work-1 --project project-2")
    expect(result.opener).toBeUndefined()
    expect(calls.some(({ argv }) => argv[0] === "tab-opener")).toBe(false)
  }
  adapter.configureSessionOpener(undefined)
})

test("a failed opener run reports the exit status and the launch command", async () => {
  const selectedRepo = await mkdtemp(join(tmpdir(), "concord-second-repo-"))
  try {
    const calls: RetargetCall[] = []
    adapter.configureSessionOpener(["tab-opener", "--cwd", "{directory}", "{command}"])
    adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => ({ exitCode: 1, stdout: "", stderr: "no display server" }), selectedRepo) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
    expect(result.error.kind).toBe("session_opener_failed")
    expect(result.error.message).toContain("exited 1")
    expect(result.error.message).toContain("no display server")
    expect(result.opener.exit_code).toBe(1)
    expect(result.launch.runnable).toBe("concord zl work-1 --project project-2")
  } finally {
    adapter.configureSessionOpener(undefined)
    await rm(selectedRepo, { recursive: true, force: true })
  }
})

// CD-0093 D3's fail-closed rule rides into the opener route (CD-0182 D2): a
// member Project whose registered canonical path no longer resolves must
// never spawn the opener against a directory that is gone. The probe refuses
// before the run and the answer falls back to the no-opener launch command.
test("a canonical path removed after registration refuses the opener and returns the launch command", async () => {
  const repoRoot = await mkdtemp(join(tmpdir(), "concord-second-repo-"))
  const removedRepo = join(repoRoot, "removed")
  try {
    await mkdir(removedRepo)
    await rm(removedRepo, { recursive: true })
    const calls: RetargetCall[] = []
    adapter.configureSessionOpener(["tab-opener", "--cwd", "{directory}", "--title", "{title}", "--", "{command}"])
    adapter.configureConcordAdapter({ runner: secondRepoRunner(calls, () => ({ exitCode: 0, stdout: "", stderr: "" }), removedRepo) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("canonical_path_unresolved")
    expect(result.error.retry_safe).toBe(false)
    expect(result.error.effect_state).toBe("none")
    expect(result.error.message).toContain(removedRepo)
    expect(result.error.message).toContain("concord zl work-1 --project project-2")
    expect(result.launch.directory).toBe(removedRepo)
    expect(result.launch.runnable).toBe("concord zl work-1 --project project-2")
    expect(result.opener).toBeUndefined()
    expect(calls.some(({ argv }) => argv[0] === "tab-opener")).toBe(false)
  } finally {
    adapter.configureSessionOpener(undefined)
    await rm(repoRoot, { recursive: true, force: true })
  }
})

test("a resume whose selected Project shares the calling repository keeps the claim-and-move route", async () => {
  bindRetargetRoute({ landedDirectory: "/data/worktrees/project-2/work-1" })
  const landedThere = contextFor(() => {}, new AbortController(), "/data/worktrees/project-2/work-1")
  const calls: RetargetCall[] = []
  adapter.configureSessionOpener(["tab-opener", "{command}"])
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string, _signal: AbortSignal, _options?: any) {
    calls.push({ argv, input })
    if (argv[0] === "tab-opener") throw new Error("the opener must not run inside one repository")
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "project-canonical-path") return { exitCode: 0, stdout: JSON.stringify({ project_id: (JSON.parse(input) as { project_id: string }).project_id, canonical_path: "/repo-1" }), stderr: "" }
    if (argv[1] === "worktree-locate") {
      const parsed = JSON.parse(input) as { project_id: string; work_id: string }
      return { exitCode: 0, stdout: JSON.stringify({ branch: `work/${parsed.work_id}`, base_sha: "a".repeat(40), path: `/data/worktrees/${parsed.project_id}/${parsed.work_id}`, repo: "/repo-1", ref: "HEAD" }), stderr: "" }
    }
    if (argv[1] === "work-resume") return { exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), project_id: "project-2", worktree: { ...resumeSuccess().worktree, path: "/data/worktrees/project-2/work-1" } }), stderr: "" }
    if (argv[1] === "session-prepare") return { exitCode: 0, stdout: JSON.stringify({ ...preparedContract(), directory: "/data/worktrees/project-2/work-1" }), stderr: "" }
    if (argv[1] === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
    throw new Error(`unexpected command ${argv.join(" ")}`)
  } } })
  try {
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, landedThere))
    expect(result.outcome).toBe("ok")
    expect(result.project_id).toBe("project-2")
    expect(result.worktree_path).toBe("/data/worktrees/project-2/work-1")
    const resume = calls.find(({ argv }) => argv[1] === "work-resume")
    expect(JSON.parse(resume!.input).project_id).toBe("project-2")
  } finally {
    adapter.configureSessionOpener(undefined)
  }
})

test("a failed project location lookup refuses before the opener runs", async () => {
  const calls: RetargetCall[] = []
  adapter.configureSessionOpener(["tab-opener", "{command}"])
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], _input: string, _signal: AbortSignal, _options?: any) {
    calls.push({ argv, input: "" })
    if (argv[0] === "tab-opener") throw new Error("the opener must not run")
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 1, stdout: "", stderr: "concord project-canonical-path: unknown_scope: Project has no canonical_path locator" }
  } } })
  try {
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-unknown" }, contextFor()))
    expect(result.error.kind).toBe("invalid_input")
    expect(result.error.message).toContain("canonical_path locator")
    expect(calls.some(({ argv }) => argv[0] === "tab-opener")).toBe(false)
  } finally {
    adapter.configureSessionOpener(undefined)
  }
})

test("work start resume rejects mixed and malformed argument shapes", async () => {
  // The passing route lets the lane guard admit the coordinator, so the
  // assertions pin the argument-shape refusals, not the lane boundary.
  bindRetargetRoute()
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
  // The passing route lets the lane guard admit the coordinator, so the
  // assertions pin the missing-field refusal, not the lane boundary.
  bindRetargetRoute()
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
  const corrected = await rawHostResult(adapter.work_start.execute({ ...incomplete, value_statement: "Start valid work without operator repair.", idempotency_key: "corrected-start-1" }, landedContextFor()))
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
  // The lane guard's caller check (CD-0196) is the one permitted host read;
  // invalid input reaches no host write and no core transport.
  let hostReads = 0
  let hostWrites = 0
  hostControlPlane().bind({
    get: async () => {
      hostReads++
      return { data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }
    },
    post: async () => { hostWrites++; throw new Error("invalid input must not reach the host") },
    patch: async () => { hostWrites++; throw new Error("invalid input must not reach the host") },
  })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const { idempotency_key: _key, ...invalid } = bootstrapArgs
  const result: any = await rawHostResult(adapter.work_start.execute(invalid, contextFor()))
  expect(result.error.kind).toBe("invalid_input")
  expect(hostWrites).toBe(0)
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
  { name: "empty title", args: { ...bootstrapArgs, title: "" }, fragments: ["title", "Unicode code points", "minimum of 1"] },
  { name: "title character limit", args: { ...bootstrapArgs, title: "x".repeat(257) }, fragments: ["title", "256"] },
  { name: "title byte limit", args: { ...bootstrapArgs, title: "é".repeat(129) }, fragments: ["title", "256", "UTF-8 bytes"] },
  { name: "value statement byte limit", args: { ...bootstrapArgs, value_statement: "é".repeat(129) }, fragments: ["value_statement", "256", "UTF-8 bytes"] },
  { name: "external reference byte limit", args: { ...bootstrapArgs, external_ref: "é".repeat(129) }, fragments: ["external_ref", "256", "UTF-8 bytes"] },
  { name: "task byte limit", args: { ...bootstrapArgs, task: "🙂".repeat(2049) }, fragments: ["task", "8192", "UTF-8 bytes"] },
  { name: "empty resume identity", args: { work_id: "" }, fragments: ["work_id", "Unicode code points", "minimum of 1"] },
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
    // The lane guard's caller check (CD-0196) is the one permitted host read;
    // a diagnostic refusal reaches no host write and no core transport.
    let hostWrites = 0
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
      post: async () => { hostWrites++; throw new Error("invalid input reached the host") },
      patch: async () => { hostWrites++; throw new Error("invalid input reached the host") },
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
    expect(hostWrites).toBe(0)
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
    const { moved } = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result = await rawHostResult(adapter.work_start.execute(args, landedContextFor()))
    expect(result).toMatchObject({ outcome: "ok", product_id: "product-1", project_id: "project-1", work_id: "work-1" })
    expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", ...args, session_ref: "session-1", host_pid: process.pid })
    expect(calls.filter(({ argv }) => argv[1] === "work-bootstrap")).toHaveLength(1)
    expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  }
})

test("work start refuses before any effect when the host handed the plugin no client", async () => {
  // Fail-closed lane boundary: with no control-plane client the caller cannot
  // be proven a coordinator session, so the lane guard refuses before work
  // start's own probe layer runs. Nothing is captured and nothing moves.
  bindRetargetRoute({ unbound: true })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(result.error.message).toContain("the host control plane is unbound")
  expect(calls).toEqual([])
  expect(result.work_id).toBeUndefined()
})

test("work start refuses before any effect when the host control plane cannot be reached", async () => {
  // Fail-closed lane boundary: an ancestry read that cannot reach the host
  // leaves the caller unproven, so the lane guard refuses and the unreachability
  // travels in the refusal detail. Nothing is captured and nothing moves.
  const unreachable = async () => {
    throw new Error("Unable to connect. Is the computer able to access the url?")
  }
  hostControlPlane().bind({ get: unreachable, post: unreachable })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("CD-0017 D4")
  expect(result.error.message).toContain("cannot resolve this session's managed Task scope")
  expect(result.error.message).toContain("Unable to connect")
  expect(calls).toEqual([])
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
// leaves no partial state: every step is idempotent on the derived key, so a
// replay under the same idempotency_key adopts what exists and runs the rest.
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
  const { moved } = bindRetargetRoute()
  const second: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(second, {
      "work-bootstrap": () => ({ exitCode: 0, stdout: JSON.stringify({ ...bootstrapSuccess(), replayed: true }), stderr: "" }),
    }),
  })
  const converged: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(converged).toMatchObject({ outcome: "ok", work_id: "work-1", worktree_path: WORKTREE, session_id: "session-1" })
  expect(second.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
  expect(JSON.parse(second[1].input).idempotency_key).toBe(bootstrapArgs.idempotency_key)
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
})

// The core reports a deterministic session-prepare refusal with its typed
// exit status. The adapter classifies by that status alone — never by stderr
// text — and maps it to contact_operator: a refusal that fails the same way
// until state changes is not repaired by replaying the request.
test("a session-prepare refusal status maps work_start recovery to contact_operator", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(calls, {
      "session-prepare": () => ({ exitCode: adapter.sessionPrepareRefusalExit, stdout: "", stderr: "concord session-prepare: current directory is not an active claimed worktree of this work item" }),
    }),
  })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("session_prepare_failure")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.retry_safe).toBe(false)
  expect(result.error.recovery_action.kind).toBe("contact_operator")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
})

// Any session-prepare exit other than the refusal status is not a refusal: a
// transient core failure stays retry_safe and keeps retry_same_request.
test("a non-refusal session-prepare failure keeps retry_same_request", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(calls, {
      "session-prepare": () => ({ exitCode: 1, stdout: "", stderr: "concord session-prepare: cannot read the authority database" }),
    }),
  })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("session_prepare_failure")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.retry_safe).toBe(true)
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
})

// The core reports a deterministic work-bootstrap refusal with its typed
// exit status. The adapter classifies by that status alone — never by
// stderr text — and maps it to contact_operator: an idempotency key bound
// to different input is not repaired by replaying the request.
test("a work-bootstrap refusal status maps work_start recovery to contact_operator", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(calls, {
      "work-bootstrap": () => ({ exitCode: adapter.workBootstrapRefusalExit, stdout: "", stderr: "concord work-bootstrap: invalid_operation: idempotency key is bound to different input" }),
    }),
  })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("bootstrap_failure")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.retry_safe).toBe(false)
  expect(result.error.recovery_action.kind).toBe("contact_operator")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap"])
})

// Any work-bootstrap exit other than the refusal status is not a refusal: a
// transient core failure stays retry_safe and keeps retry_same_request.
test("a non-refusal work-bootstrap failure keeps retry_same_request", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(calls, {
      "work-bootstrap": () => ({ exitCode: 1, stdout: "", stderr: "concord work-bootstrap: unavailable: cannot read the authority database" }),
    }),
  })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("bootstrap_failure")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.retry_safe).toBe(true)
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
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

// Worktree occupancy is recorded when Concord claims the worktree. Removal
// does not ask the host for a second, non-authoritative session population.
const bindSessionRoutes = (options: { sessions?: unknown; listStatus?: number; unbound?: boolean; onListRequest?: () => void } = {}) => {
  if (options.unbound) {
    hostControlPlane().bind(undefined)
    return
  }
  hostControlPlane().bind({
    post: async () => ({ response: new Response(null, { status: 404 }) }),
    get: async ({ url, path }) => {
      if (url === SESSION_ROUTE) return { data: { id: (path as { id?: string })?.id ?? "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }
      if (url !== SESSION_LIST_ROUTE) return { response: new Response(null, { status: 404 }) }
      options.onListRequest?.()
      const status = options.listStatus ?? 200
      if (status !== 200) return { response: new Response("host is unwell", { status }) }
      return { data: options.sessions ?? [], response: new Response(null, { status }) }
    },
  })
}

const removalRequest = (operation: string) => hostCall(operation, {
  work_id: "work-1", project_id: "project-1", expected_version: 2, idempotency_key: "remove-1",
})
const auditRemovalRequest = () => hostCall("worktree_audit_reclaim", { idempotency_key: "audit-remove-1" })

// A removal is a mutation, so an ok core answer carries the result, the
// changed refs, and the next intents the generated envelope contract requires.
const removalOk = (operation: string) => coreEnvelope("concord_work_transition", operation, "ok", {
  // The payload contract counts a version; the envelope carries it as a string.
  result: { changed_refs: [{ entity_kind: "work_item", id: "work-1", version: 3 }], next_valid_intents: [] },
  changed_refs: [{ entity_kind: "work_item", id: "work-1", version: "3" }],
  next_valid_intents: [],
})
const auditOk = () => coreEnvelope("concord_work_transition", "worktree_audit_reclaim", "ok", {
  result: { root: "/repo", rows: [], report_only: [], changed_refs: [], next_valid_intents: [] },
  changed_refs: [],
  next_valid_intents: [],
})

// The removal verbs share one report path, and no removal contract input
// carries a host session observation (CD-0178 D3).
test("worktree removal operations derive from contract inputs", () => {
  const observed = contractOperations
    .filter((operation: any) => operation.tool === "concord_work_transition" && operation.input_schema.startsWith("#/schemas/"))
    .filter((operation: any) => Object.hasOwn((payloadSchemas as any)[operation.input_schema.slice("#/schemas/".length)]?.properties ?? {}, "observed_session_directories"))
    .map((operation: any) => operation.id.slice("concord_work_transition.".length))
  expect(observed).toEqual([])
  expect([...adapter.WORKTREE_REMOVAL_OPERATIONS].sort()).toEqual(["worktree_audit_reclaim", "worktree_destroy", "worktree_reclaim"])
})

// The durable worktree_occupancy projection and the kernel's process
// liveness own the removal gate (CD-0178 D3). A removal input carries no session
// observation, and the adapter makes no host session-list round-trip.
test("a worktree removal attaches no host session observation", async () => {
  for (const operation of ["worktree_reclaim", "worktree_destroy", "worktree_audit_reclaim"]) {
    let hostSessionListCalls = 0
    bindSessionRoutes({
      sessions: [{ id: "ses_alpha", directory: "/worktrees/work-1" }, { id: "ses_beta", directory: "/elsewhere" }],
      onListRequest: () => { hostSessionListCalls++ },
    })
    const seen: string[] = []
    adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
      seen.push(input)
      return operation === "worktree_audit_reclaim" ? auditOk() : removalOk(operation)
    }) })
    const request = operation === "worktree_audit_reclaim" ? auditRemovalRequest() : removalRequest(operation)
    const envelope: any = await rawHostResult(adapter.work_transition.execute(request, contextFor()))
    expect(envelope.outcome, operation).toBe("ok")
    expect(JSON.parse(seen[0]).input.observed_session_directories, operation).toBeUndefined()
    expect(hostSessionListCalls, operation).toBe(0)
  }
})

// The adapter neither gathers nor strips a session observation. The transport stays transparent, and the
// core's generated contract owns rejecting the unknown field.
test("a caller-supplied session observation passes through untouched", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/worktrees/work-1" }] })
  let seen = ""
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    seen = input
    return removalOk("worktree_reclaim")
  }) })
  const request = removalRequest("worktree_reclaim")
  request.request.input.observed_session_directories = [{ session_ref: "ses_caller", directory: "/caller-sees" }]
  const envelope: any = await rawHostResult(adapter.work_transition.execute(request, contextFor()))
  expect(envelope.outcome).toBe("ok")
  expect(JSON.parse(seen).input.observed_session_directories).toEqual([{ session_ref: "ses_caller", directory: "/caller-sees" }])
})

test("audit reclaim refuses an occupied worktree through the core", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/worktrees/work-1" }] })
  let seen = ""
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    seen = input
    return coreEnvelope("concord_work_transition", "worktree_audit_reclaim", "error", {
      error: { kind: "unauthorized", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
    })
  }) })
  const envelope: any = await rawHostResult(adapter.work_transition.execute(auditRemovalRequest(), contextFor()))
  expect(envelope.outcome).toBe("error")
  expect(envelope.error.kind).toBe("unauthorized")
  expect(JSON.parse(seen).input.observed_session_directories).toBeUndefined()
})

test("a worktree removal does not depend on the host session list", async () => {
  for (const options of [{ listStatus: 500 }, { sessions: { not: "an array" } }, { sessions: [{ id: "ses_alpha" }] }]) {
    bindSessionRoutes(options)
    let coreCalls = 0
    adapter.configureConcordAdapter({ runner: runnerWithContext(() => {
      coreCalls++
      return removalOk("worktree_reclaim")
    }) })
    const envelope: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
    expect(envelope.outcome, JSON.stringify(options)).toBe("ok")
    expect(coreCalls, JSON.stringify(options)).toBe(1)
  }
})

test("a completed worktree removal queues its notice for the text-part channel", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/elsewhere" }] })
  adapter.configureConcordAdapter({ runner: runnerWithContext(removalOk("worktree_reclaim")) })
  const envelope: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(envelope.outcome).toBe("ok")
  const notices = adapter.takeWorkNotices("session-1")
  expect(notices).toHaveLength(1)
  expect(notices[0]).toContain("work-1")
})

test("a refused worktree removal queues nothing, and the notice makes no host round-trip", async () => {
  // Your rule: an unsafe removal does not happen, and needs no notice because
  // nothing was lost. A notice for a removal that did not happen would be a
  // lie.
  bindSessionRoutes({ sessions: [{ id: "ses_alpha", directory: "/elsewhere" }] })
  adapter.configureConcordAdapter({ runner: runnerWithContext(coreEnvelope(
    "concord_work_transition", "worktree_reclaim", "error",
    { error: { kind: "worktree_ownership_conflict", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" } },
  )) })
  const refusal: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(refusal.outcome).toBe("error")
  expect(adapter.takeWorkNotices("session-1")).toEqual([])

  // The old failure mode — a delivery the host refuses — cannot fail a
  // completed removal any more, because the notice queue is adapter process
  // state and the plugin drains it into the assistant text. A host whose
  // every write route throws still completes the removal and still queues
  // the notice.
  hostControlPlane().bind({
    post: async () => { throw new Error("the host serves no write route") },
    get: async ({ url }) => {
      if (url === SESSION_ROUTE) return { data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }
      return { response: new Response(null, { status: 404 }) }
    },
  })
  adapter.configureConcordAdapter({ runner: runnerWithContext(removalOk("worktree_reclaim")) })
  const delivered: any = await rawHostResult(adapter.work_transition.execute(removalRequest("worktree_reclaim"), contextFor()))
  expect(delivered.outcome).toBe("ok")
  expect(adapter.takeWorkNotices("session-1")).toEqual(["Concord removed the worktree of work-1."])
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

// A breaking-migration outage names the terminal behind each blocking pid, so
// the lease claim carries the session's directory and worktree alongside it.
test("the host lease claim names the session directory and worktree", async () => {
  const claimed: any[] = []
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: { async run(argv: string[], input: string) {
      claimed.push({ argv, input: JSON.parse(input) })
      return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory: "/home/operator/card-site", worktree: "/wt" }), stderr: "" }
    } },
  })
  await claimHostLease(4242, { directory: "/home/operator/card-site", worktree: "/wt" })
  expect(claimed).toHaveLength(1)
  expect(claimed[0].argv[1]).toBe("host-lease")
  expect(claimed[0].input.pid).toBe(4242)
  expect(claimed[0].input.directory).toBe("/home/operator/card-site")
  expect(claimed[0].input.worktree).toBe("/wt")
  configureHostLease({ reset: true })
})

// CON-807: while a maintenance boundary is open, the core refuses new session
// admission. A session that starts mid-boundary fails closed with the fence's
// own notice, so the operator reads the activation route in the session
// instead of debugging a vanished release.
test("a lease claim refused by the maintenance fence carries the boundary notice", async () => {
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: { async run() {
      return {
        exitCode: 1,
        stdout: "",
        stderr: "concord host-lease: hostlease: session admission is excluded by an open maintenance boundary: /releases/v11.0.1/bin/concord opened it at 2026-10-04T10:00:00Z; session admission reopens when the prepared release activates; activation command: python3 /downloads/concord-installer.py activate --version v11.0.1",
      }
    } },
  })
  await claimHostLease(4243)
  const fault = hostLeaseFault()
  expect(fault).toContain("host lease claim failed with exit 1")
  expect(fault).toContain("session admission is excluded by an open maintenance boundary")
  expect(fault).toContain("activation command: python3 /downloads/concord-installer.py activate --version v11.0.1")
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

test("worker_abandon routes through the signed worker-abandon command", async () => {
  const calls: Array<{ argv: string[]; input: any }> = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      calls.push({ argv, input: JSON.parse(input) })
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-operation-1" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(result.outcome).toBe("ok")
  expect(result.operation).toBe("worker_abandon")
  expect(calls).toHaveLength(3)
  expect(calls[0].argv).toEqual(["concord", "worker-abandon"])
  expect(calls[0].input.work_id).toBe("work-1")
  expect(calls[0].input.attempt_id).toBe("attempt-1")
  // worker-fail carries no host session observation; the core reads process
  // liveness from worktree_occupancy.
  expect(calls[0].input.observed_session_directories).toBeUndefined()
  expect(calls[0].input.assertion).toMatchObject({ verb: "worker-fail", failure_kind: "abandoned", readback_model: "" })
  expect(typeof calls[0].input.assertion.signature).toBe("string")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["worker-abandon", "project-resolve", "invoke"])
})

// The spawn-failure wedge: a terminal Task error with no child session identity
// retains the in-flight record, and no clearing path can consume it. The
// abandoned-attempt receipt is the named reconciliation route, so an ok abandon
// must release the retained record and both an abandon accepted now and a
// replay answered already terminal must land there.
test("an uncertain durable abandonment reports a possible effect and heals on retry", async () => {
  let abandonExit = 1
  const abandonStderr = "store: durable_commit: unavailable: synthetic commit IO error"
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "worker-abandon") return abandonExit === 1 ? { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: ["abandon-1"], error: { kind: "unavailable", operation: "durable_commit", retry_safe: true, effect_state: "possible", message: abandonStderr } }), stderr: abandonStderr } : { exitCode: 0, stdout: "", stderr: "" }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-durability-1" }
  const pending: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(pending.outcome).toBe("error")
  expect(pending.error.effect_state).toBe("possible")
  expect(pending.error.recovery_action.kind).toBe("retry_same_request")
  expect(pending.error.message).toContain("durable_commit")
  expect(pending.error.message).not.toContain("remains open")
  // The retry reaches a CLI exit 0, so the recorded envelope flows through
  // the receipt replay and the abandonment settles.
  abandonExit = 0
  const settled: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(settled.outcome).toBe("ok")
})

test.each([false, true])("an ok worker_abandon releases its exact authorization (Task consumed=%s)", async (consumed) => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  // The suite shares one window registry across files, so this test owns a
  // session identity no other test touches.
  const context = { ...contextFor(), sessionID: `session-abandon-release-${consumed}` }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-1",
    lane_id: "implement",
    lane_version: 1,
    lane_digest: "sha256:" + "a".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "c".repeat(64), process.cwd())
  if (consumed) {
    await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "x", prompt: "y", description: "z" }, "call-cancel", async () => process.cwd(), process.cwd())
    expect(windows.inFlight(context.sessionID, "call-cancel")).not.toBeNull()
  } else {
    expect(windows.has(context.sessionID)).toBe(true)
  }
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the host never spawned the worker", idempotency_key: "worker-abandon-release-1" }
  const accepted: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(accepted.outcome).toBe("ok")
  expect(windows.has(context.sessionID)).toBe(false)
  expect(windows.inFlight(context.sessionID, "call-cancel")).toBeNull()
  // The recovered session dispatches again without a host restart.
  expect(() => windows.open(context.sessionID, retained, "", process.cwd())).not.toThrow()
  windows.close(context.sessionID)
  // A replay of the same abandon is answered already terminal and stays ok.
  const replayed: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(replayed.outcome).toBe("ok")
  expect(windows.has(context.sessionID)).toBe(false)
})

// The stranding wedge with no durable counterpart: a host Task call
// interrupted before the worker starts leaves the record in-flight, and no
// worker attempt row is ever written. Both durable routes refuse — the CLI
// evidence write and the core receipt read the same missing worker attempt
// row — so the owning repair is the adapter-side nothing-durable release: the
// typed projection_not_found refusal drops the retained record and answers
// with the ok receipt that states nothing durable existed to abandon.
test("a worker_abandon refusal for a never-dispatched attempt releases the retained record", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  // The suite shares one window registry across files, so this test owns a
  // session identity no other test touches.
  const context = { ...contextFor(), sessionID: "session-abandon-nothing-durable" }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-stranded",
    lane_id: "implement",
    lane_version: 1,
    lane_digest: "sha256:" + "b".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "d".repeat(64), process.cwd())
  await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "x", prompt: "y", description: "z" }, "call-stranded", async () => process.cwd(), process.cwd())
  expect(windows.inFlight(context.sessionID, "call-stranded")).not.toBeNull()
  const calls: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      calls.push(argv[1])
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: "concord worker-abandon: store: worker_attempt_read: projection_not_found: worker dispatch row does not exist" }
      throw new Error(`no durable route may run for a never-dispatched attempt: ${argv[1]}`)
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-stranded", lane_id: "implement", detail: "the host Task call was interrupted before the worker started", idempotency_key: "worker-abandon-nothing-durable-1" }
  const released: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(released.outcome).toBe("ok")
  expect(validateGeneratedEnvelope(released), JSON.stringify(released)).toBe(true)
  expect(released.result.nothing_durable_to_abandon).toContain("attempt attempt-stranded")
  expect(released.result.nothing_durable_to_abandon).toContain("nothing durable existed to abandon")
  // The core receipt route never ran: the CLI refusal is the durable answer.
  expect(calls).toEqual(["worker-abandon"])
  // The retained record is released, so the session dispatches again without
  // a host restart.
  expect(windows.inFlight(context.sessionID, "call-stranded")).toBeNull()
  expect(() => windows.open(context.sessionID, retained, "", process.cwd())).not.toThrow()
  windows.close(context.sessionID)
})

// The release demands the exact attempt and lane identity the retained record
// holds, so a nothing-durable abandon naming a different attempt still answers
// ok — the core holds no row for that attempt either — while the retained
// record stays.
test("a nothing-durable abandon for a foreign attempt leaves the retained record", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  const context = { ...contextFor(), sessionID: "session-abandon-nothing-durable-foreign" }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-stranded",
    lane_id: "implement",
    lane_version: 1,
    lane_digest: "sha256:" + "b".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "e".repeat(64), process.cwd())
  await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "x", prompt: "y", description: "z" }, "call-foreign", async () => process.cwd(), process.cwd())
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: "concord worker-abandon: store: worker_attempt_read: projection_not_found: worker dispatch row does not exist" }
      throw new Error(`unexpected core call: ${argv[1]}`)
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-elsewhere", lane_id: "implement", detail: "a foreign attempt identity", idempotency_key: "worker-abandon-nothing-durable-foreign" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(result.outcome).toBe("ok")
  expect(result.result.nothing_durable_to_abandon).toContain("attempt-elsewhere")
  expect(windows.inFlight(context.sessionID, "call-foreign")).not.toBeNull()
  expect(windows.releaseRetained(context.sessionID, "attempt-stranded", "implement")).toBe(true)
})

// The live-occupancy refusal names the calling session's own row as the
// holder, and the release route it names was left to the operator. The
// adapter now runs the named route itself: session_vacate releases the row
// and moves the session to the registered main checkout, one abandon retry
// with the same event identity closes the attempt, work_start re-lands the
// session in the claimed worktree, and the durable receipt records the
// close from that linked worktree. Every step rides the answer as a
// recovery notice.
test("an own-row recovery reports a refused re-land without invoking a receipt from main", async () => {
  const MAIN_CHECKOUT = "/data/repo-main"
  const sessionID = "session-abandon-own-row"
  let sessionDirectory = WORKTREE
  let metadata: Record<string, unknown> = {}
  const moves: string[] = []
  const titles: string[] = []
  hostControlPlane().bind({
    post: async ({ body }) => {
      const destination = (body as { destination: { directory: string } }).destination.directory
      moves.push(destination)
      sessionDirectory = destination
      return { response: new Response(null, { status: 204 }) }
    },
    get: async ({ path }) => ({ data: { id: path?.id, directory: sessionDirectory, metadata }, response: new Response(null, { status: 200 }) }),
    patch: async ({ body }) => {
      const patchBody = body as { metadata?: Record<string, unknown>; title?: string }
      if (typeof patchBody.title === "string") {
        titles.push(patchBody.title)
        return { response: new Response(null, { status: 200 }) }
      }
      metadata = patchBody.metadata as Record<string, unknown>
      return { response: new Response(null, { status: 200 }) }
    },
  })
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live`
  const calls: Array<{ argv: string[]; input: string }> = []
  let abandonCalls = 0
  const windows = dispatchWindows()
  windows.open(sessionID, {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:" + "a".repeat(64), work_id: "work-1", step_id: "repair",
    inputs: { task: "the bounded task", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }, "sha256:" + "b".repeat(64), process.cwd())
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      const command = argv[1]
      calls.push({ argv, input })
      if (command === "worker-abandon") {
        abandonCalls += 1
        return abandonCalls === 1
          ? { exitCode: 1, stdout: "", stderr: ownRowRefusal }
          : { exitCode: 0, stdout: "", stderr: "" }
      }
      if (command === "invoke") {
        const operation = (JSON.parse(input) as { operation: string }).operation
        if (operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: sessionDirectory, destination_directory: MAIN_CHECKOUT }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
        if (operation === "worker_abandon") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      }
      if (command === "vacate-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") return { exitCode: 1, stdout: "", stderr: "work resume refused" }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    } },
  })
  const context = { ...landedContextFor(), sessionID }
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-own-row-1" }
  const toolResult = (await adapter.work_transition.execute(hostCall("worker_abandon", input), context)) as { output: string }
  const result: any = envelopeLine(toolResult.output)
  expect(result.outcome).toBe("error")
  expect(result.operation).toBe("worker_abandon")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.details.recovery_stopped_at).toBe("work_start")
  expect(result.error.details.recovery_steps).toEqual([
    "session_vacate: the session moved to the registered main checkout and the verified landing released its occupancy rows",
    "worker_abandon: the retry closed the dispatched attempt",
    expect.stringContaining("work_start re-land refused: work resume refused; replay work_start once the session's tool context runs in the claimed worktree"),
  ])
  expect(calls.filter(({ argv, input }) => argv[1] === "invoke" && JSON.parse(input).operation === "worker_abandon")).toHaveLength(0)
  expect(windows.has(sessionID)).toBe(false)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  // The re-land refused, so the session's final confirmed position is the
  // registered main checkout: the composed answer carries that notice, and
  // no worktree notice can ride it.
  expect(toolResult.output).toContain(`Concord moved this session to ${MAIN_CHECKOUT}`)
  expect(toolResult.output).not.toContain(`Concord moved this session to ${WORKTREE}`)
  // The session moved out for the vacate. The failed re-land names its replay remedy.
  expect(moves).toEqual([MAIN_CHECKOUT])
  // One refused CLI abandon, then the retry carrying the same event identity
  // the refused write never committed.
  const abandonInputs = calls.filter(({ argv }) => argv[1] === "worker-abandon").map(({ input }) => JSON.parse(input))
  expect(abandonInputs).toHaveLength(2)
  expect(abandonInputs[0].event_id).toBe(abandonInputs[1].event_id)
  expect(calls.map(({ argv }) => argv[1])).toEqual([
    "worker-abandon",
    "project-resolve", "invoke", "vacate-landing",
    "worker-abandon",
    "project-resolve", "work-resume",
  ])
})

test.each([false, true])("own-row abandonment records its receipt from the confirmed worktree (receipt refused=%s)", async (receiptRefused) => {
  const MAIN_CHECKOUT = "/data/repo-main"
  const sessionID = `session-abandon-receipt-landing-${receiptRefused}`
  let sessionDirectory = WORKTREE
  let metadata: Record<string, unknown> = {}
  const moves: string[] = []
  hostControlPlane().bind({
    post: async ({ body }) => {
      sessionDirectory = (body as { destination: { directory: string } }).destination.directory
      moves.push(sessionDirectory)
      return { response: new Response(null, { status: 204 }) }
    },
    get: async ({ path }) => ({ data: { id: path?.id, directory: sessionDirectory, metadata }, response: new Response(null, { status: 200 }) }),
    patch: async ({ body }) => {
      const patchBody = body as { metadata?: Record<string, unknown> }
      if (patchBody.metadata) metadata = patchBody.metadata
      return { response: new Response(null, { status: 200 }) }
    },
  })
  const windows = dispatchWindows()
  windows.open(sessionID, {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:" + "a".repeat(64), work_id: "work-1", step_id: "repair",
    inputs: { task: "the bounded task", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }, "sha256:" + "b".repeat(64), process.cwd())
  let abandonCalls = 0
  const receiptDirectories: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      const request = JSON.parse(input)
      switch (argv[1]) {
        case "worker-abandon":
          abandonCalls++
          return abandonCalls === 1
            ? { exitCode: 1, stdout: "", stderr: `store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree` }
            : { exitCode: 0, stdout: "", stderr: "" }
        case "project-resolve":
          return { exitCode: 0, stdout: JSON.stringify(contextResponse(sessionDirectory === MAIN_CHECKOUT)), stderr: "" }
        case "invoke":
          if (request.operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: sessionDirectory, destination_directory: MAIN_CHECKOUT }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
          receiptDirectories.push(request.call_envelope.directory)
          if (sessionDirectory === MAIN_CHECKOUT || receiptRefused) return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "error", { error: { kind: "unauthorized", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none", message: "x".repeat(1000) } })), stderr: "" }
          return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
        case "vacate-landing":
        case "claim-landing":
          return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }), stderr: "" }
        case "work-resume":
          return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
        case "session-prepare":
          return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
        default:
          throw new Error(`unexpected command ${argv.join(" ")}`)
      }
    } },
  })
  const context = { ...landedContextFor(), sessionID }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the host never started the worker", idempotency_key: `abandon-receipt-landing-${receiptRefused}` }), context))
  expect(result.outcome).toBe(receiptRefused ? "error" : "ok")
  expect(receiptDirectories).toEqual([WORKTREE])
  expect(moves).toEqual([MAIN_CHECKOUT, WORKTREE])
  expect(windows.has(sessionID)).toBe(false)
  if (receiptRefused) {
    expect(result.error.effect_state).toBe("possible")
    expect(result.error.adapter_reason).toBe("worker_abandon_receipt_failed")
    expect(result.error.details.receipt_error_kind).toBe("unauthorized")
    expect(result.error.details.receipt_error_message).toBe("x".repeat(1000))
  }
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
})

test.each([false, true])("a terminal abandon releases only its authorization when the receipt fails (foreign=%s)", async (foreign) => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const sessionID = `session-abandon-receipt-failure-${foreign}`
  const windows = dispatchWindows()
  windows.open(sessionID, {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement", lane_version: 1,
    lane_digest: "sha256:" + "a".repeat(64), work_id: "work-1", step_id: "repair",
    inputs: { task: "the bounded task", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }, "sha256:" + "b".repeat(64), process.cwd())
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "worker-abandon") return { exitCode: 0, stdout: "", stderr: "" }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }
      return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "error", { error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "x".repeat(1000) } })), stderr: "" }
    } },
  })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", { work_id: "work-1", attempt_id: foreign ? "attempt-foreign" : "attempt-1", lane_id: "implement", detail: "the host never started the worker", idempotency_key: `abandon-receipt-failure-${foreign}` }), { ...landedContextFor(), sessionID }))
  expect(result.error.adapter_reason).toBe("worker_abandon_receipt_failed")
  expect(result.error.effect_state).toBe("possible")
  expect(windows.has(sessionID)).toBe(foreign)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  expect(result.error.details.receipt_error_kind).toBe("operation_conflict")
  expect(result.error.details.receipt_error_message).toBe("x".repeat(1000))
  if (foreign) windows.releaseRetained(sessionID, "attempt-1", "implement")
})

// A foreign holder is outside the adapter's authority: the recovery must not
// vacate another session's row, so the refusal stands unchanged and no
// recovery route runs.
test("an occupancy refusal naming a foreign holder keeps the unchanged refusal", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const foreignRefusal = "concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ses-foreign still holds the worker attempt worktree; its host process 4242 is still live: " + "x".repeat(1000)
  const calls: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      calls.push(argv[1])
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: foreignRefusal }
      throw new Error(`no recovery route may run for a foreign holder: ${argv[1]}`)
    } },
  })
  const context = { ...contextFor(), sessionID: "session-abandon-foreign-holder" }
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-foreign-holder-1" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("operation_conflict")
  expect(result.error.adapter_reason).toBe("worker_abandon_refused")
  expect(result.error.details.abandon_error_message).toBe(foreignRefusal)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  // Only the one refused CLI abandon ran: no vacate, no retry, no receipt.
  expect(calls).toEqual(["worker-abandon"])
})

// A vacate refusal keeps its own effect state instead of assuming no write.
test("an own-row recovery preserves the effect state on a vacate refusal", async () => {
  const MAIN_CHECKOUT = "/data/repo-main"
  const vacateRefusal = "vacate may have released occupancy: " + "x".repeat(950)
  const sessionID = "session-abandon-own-row-vacate-fails"
  let sessionDirectory = WORKTREE
  let metadata: Record<string, unknown> = {}
  hostControlPlane().bind({
    post: async ({ body }) => {
      // The host performs the relocation but the readback never confirms it.
      sessionDirectory = (body as { destination: { directory: string } }).destination.directory === MAIN_CHECKOUT ? "/stuck-elsewhere" : (body as { destination: { directory: string } }).destination.directory
      return { response: new Response(null, { status: 204 }) }
    },
    get: async ({ path }) => ({ data: { id: path?.id, directory: sessionDirectory, metadata }, response: new Response(null, { status: 200 }) }),
    patch: async ({ body }) => {
      metadata = (body as { metadata?: Record<string, unknown> }).metadata as Record<string, unknown>
      return { response: new Response(null, { status: 200 }) }
    },
  })
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live`
  const calls: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      calls.push(argv[1])
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: ownRowRefusal }
      if (argv[1] === "invoke") {
        const operation = (JSON.parse(input) as { operation: string }).operation
        if (operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "error", { error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "possible", message: vacateRefusal } })), stderr: "" }
      }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      throw new Error(`no route may run past a failed vacate: ${argv[1]}`)
    } },
  })
  const context = { ...landedContextFor(), sessionID }
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-own-row-vacate-fails-1" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(result.outcome).toBe("error")
  expect(result.error.adapter_reason).toBe("worker_abandon_refused")
  expect(result.error.message).toContain("stopped at session_vacate")
  expect(result.error.details.recovery_error_message).toBe(vacateRefusal)
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.details.recovery_stopped_at).toBe("session_vacate")
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  // The abandon retry never ran.
  expect(calls.filter((command) => command === "worker-abandon")).toHaveLength(1)
})

test("an own-row recovery reports a committed vacate when the host move fails", async () => {
  const sessionID = "session-abandon-own-row-move-fails"
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live`
  hostControlPlane().bind({
    post: async () => { throw new Error("host move failed") },
    get: async ({ path }) => ({ data: { id: path?.id, directory: WORKTREE, metadata: {} }, response: new Response(null, { status: 200 }) }),
  })
  const calls: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      calls.push(argv[1])
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: ownRowRefusal }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke" && (JSON.parse(input) as { operation: string }).operation === "session_vacate") {
        return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: WORKTREE, destination_directory: "/data/repo-main" }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    } },
  })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", {
    work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-move-failed-1",
  }), { ...landedContextFor(), sessionID }))
  expect(result.outcome).toBe("error")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.details.recovery_stopped_at).toBe("session_vacate")
  // The committed vacate records the relocation request; the occupancy rows
  // stand until the landing is recorded, so a refused move never leaves a
  // live session in a worktree recorded as empty.
  expect(result.error.details.recovery_steps).toContain("session_vacate: the relocation request is recorded at the core; the occupancy rows stand until the landing is recorded")
  expect(result.error.details.recovery_error_message).toContain("host move failed")
  expect(result.error.details.recovery_error_message).toContain("host version synthetic-test-host")
  expect(result.error.message).toContain("stopped at session_vacate")
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  expect(calls).toEqual(["worker-abandon", "project-resolve", "invoke"])
})

test("an own-row recovery re-lands before it stops on an abandon retry refusal", async () => {
  const MAIN_CHECKOUT = "/data/repo-main"
  const sessionID = "session-abandon-own-row-retry-refuses"
  let sessionDirectory = WORKTREE
  let metadata: Record<string, unknown> = {}
  const moves: string[] = []
  hostControlPlane().bind({
    post: async ({ body }) => {
      const destination = (body as { destination: { directory: string } }).destination.directory
      moves.push(destination)
      sessionDirectory = destination
      return { response: new Response(null, { status: 204 }) }
    },
    get: async ({ path }) => ({ data: { id: path?.id, directory: sessionDirectory, metadata }, response: new Response(null, { status: 200 }) }),
    patch: async ({ body }) => {
      metadata = (body as { metadata?: Record<string, unknown> }).metadata as Record<string, unknown>
      return { response: new Response(null, { status: 200 }) }
    },
  })
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live: ` + "x".repeat(1000)
  const calls: string[] = []
  let abandonCalls = 0
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      const command = argv[1]
      calls.push(command)
      if (command === "worker-abandon") {
        abandonCalls++
        return { exitCode: 1, stdout: "", stderr: ownRowRefusal }
      }
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "invoke") {
        const operation = (JSON.parse(input) as { operation: string }).operation
        if (operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: WORKTREE, destination_directory: MAIN_CHECKOUT }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
        if (operation === "project_handoff_consume") return { exitCode: 0, stdout: consumeNoHandoffAnswer(), stderr: "" }
      }
      if (command === "work-resume") return { exitCode: 0, stdout: JSON.stringify(resumeSuccess()), stderr: "" }
      if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify(preparedContract()), stderr: "" }
      if (command === "claim-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      if (command === "vacate-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    } },
  })
  const toolResult = (await adapter.work_transition.execute(hostCall("worker_abandon", {
    work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-retry-refuses-1",
  }), { ...landedContextFor(), sessionID })) as { output: string }
  const result: any = envelopeLine(toolResult.output)
  expect(result.outcome).toBe("error")
  expect(result.error.effect_state).toBe("possible")
  expect(result.error.details.recovery_stopped_at).toBe("worker_abandon_retry")
  expect(result.error.details.recovery_error_message).toBe(ownRowRefusal)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  const steps = result.error.details.recovery_steps as string[]
  expect(steps[0]).toContain("session_vacate:")
  expect(steps.some((step) => step.startsWith("work_start: the session re-landed in "))).toBe(true)
  expect(steps[steps.length - 1]).toMatch(/^work_start:/)
  // The confirmed re-land supersedes the vacate's intermediate move notice:
  // the composed answer points the agent at the claimed worktree the session
  // finally occupies, never at the main checkout the vacate passed through.
  expect(toolResult.output).toContain(`Concord moved this session to ${WORKTREE}`)
  expect(toolResult.output).not.toContain(`Concord moved this session to ${MAIN_CHECKOUT}`)
  expect(moves).toEqual([MAIN_CHECKOUT, WORKTREE])
  expect(abandonCalls).toBe(2)
  // The re-land's work_start ends with the post-landing handoff re-read
  // (CD-0182 D5 amendment): the recovery resume renders no handoff, so the
  // boot re-reads once after the landing records placement and still
  // consumes nothing.
  expect(calls).toEqual([
    "worker-abandon", "project-resolve", "invoke", "vacate-landing", "worker-abandon",
    "project-resolve", "work-resume", "session-prepare", "claim-landing", "work-resume",
  ])
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
  expect(continuationSource).toContain("Do not end a turn to report progress.")
  expect(continuationSource).toContain("A wait is not a stop.")
  expect(askingSource).toContain("Do not ask for permission to continue work already agreed")
  expect(continuationSource).not.toContain("Do not ask for general permission to continue")
  expect(continuationSource.match(/When you stop,/g)?.length).toBe(1)
})

// CD-0191: installed-versus-session release staleness. The installer
// repoints the `current` symlink beside the pinned releaseRoot on every
// install, and OpenCode never hot-reloads lane definitions, so a session
// whose pinned release differs from the installed release holds replaced
// lane text. Staleness is visible on every result, lane dispatch refuses
// before any core call, and every other surface keeps working (CD-0111 D1
// and D2 stand unchanged).

// withReleaseLayout stamps a pinned release the way the installer does and
// optionally lays a `current` symlink beside it (null lays none). The seam
// and the directory restore in every path, so no other test file observes
// the stamp or the staleness.
async function withReleaseLayout(current: string | null, run: (pinned: string) => Promise<void>, options: { relative?: boolean; pinnedVersion?: string } = {}) {
  const dataRoot = await mkdtemp(join(tmpdir(), "concord-stale-"))
  const pinned = join(dataRoot, options.pinnedVersion ?? "v11.40.3")
  await mkdir(pinned, { recursive: true })
  if (current !== null) {
    const target = options.relative ? current : join(dataRoot, current)
    await symlink(target, join(dataRoot, "current"))
  }
  try {
    configureHostLease({ release: { coreBinary: join(pinned, "bin", "concord"), releaseRoot: pinned } })
    await run(pinned)
  } finally {
    configureHostLease({ reset: true })
    await rm(dataRoot, { recursive: true, force: true })
  }
}

test("the installed release resolves through the current symlink beside the pinned root", async () => {
  configureHostLease({ reset: true })
  // The unstamped repository placeholder carries no release to compare.
  expect(resolveInstalledReleaseRoot()).toBeNull()
  expect(releaseStaleness()).toBeNull()
  await withReleaseLayout(null, async (pinned) => {
    // No current symlink: staleness is never guessed from a missing
    // observation.
    expect(resolveInstalledReleaseRoot()).toBeNull()
    expect(releaseStaleness()).toBeNull()
    expect(pinned).toBeTruthy()
  })
  await withReleaseLayout("v11.40.3", async (pinned) => {
    expect(resolveInstalledReleaseRoot()).toBe(pinned)
    expect(releaseStaleness()).toBeNull()
  })
  await withReleaseLayout("v11.40.6", async () => {
    expect(releaseStaleness()).toMatchObject({ pinnedRelease: "v11.40.3", installedRelease: "v11.40.6" })
  })
  // A rollback repoints current the same way: any difference is stale.
  await withReleaseLayout("v11.40.3", async () => {
    expect(releaseStaleness()).toMatchObject({ pinnedRelease: "v11.40.6", installedRelease: "v11.40.3" })
  }, { pinnedVersion: "v11.40.6" })
  // The installer writes absolute targets, and a relative target resolves
  // against the data root either way.
  await withReleaseLayout("v11.40.6", async () => {
    expect(releaseStaleness()).toMatchObject({ pinnedRelease: "v11.40.3", installedRelease: "v11.40.6" })
  }, { relative: true })
})

test("the dispatch gate refuses a stale dispatch_worker action before any core call", async () => {
  const refusal = staleReleaseDispatchRefusal({ pinnedReleaseRoot: "/d/v1", installedReleaseRoot: "/d/v2", pinnedRelease: "v1", installedRelease: "v2" })
  expect(refusal).toContain("v1")
  expect(refusal).toContain("v2")
  expect(refusal).toContain("restart this session")
  await withReleaseLayout("v11.40.6", async () => {
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run(...args: unknown[]) { calls++; void args; throw new Error("no core call may run for a stale dispatch") } } })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", {
      work_id: "work-1", expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idem-stale-1", fields: { lane_id: "implement" },
    }), contextFor()))
    // The refusal rides the dispatch surface's adapter-gate envelope: the
    // turn-move gate's unauthorized_dispatch shape, with the boundary
    // discriminator and the mandated stale_context refusal kind in the
    // details, because TS7 pins adapter-origin tool-envelope error kinds to
    // the transport set.
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("unauthorized_dispatch")
    expect(result.error.recovery_action).toBe("contact_operator")
    expect(result.error.details).toMatchObject({
      boundary: "release_stale", refusal_kind: "stale_context",
      pinned_release: "v11.40.3", installed_release: "v11.40.6", remedy: "restart this session to load the installed release",
    })
    expect(result.error.message).toContain("v11.40.3")
    expect(result.error.message).toContain("v11.40.6")
    expect(result.error.message).toContain("restart this session")
    expect(calls).toBe(0)
    // A dispatch_worker action that cannot name its lane refuses the same
    // way: the gate fires before routing, not after it.
    const shapeless: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", {
      work_id: "work-1", expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idem-stale-2",
    }), contextFor()))
    expect(shapeless.error.kind).toBe("unauthorized_dispatch")
    expect(shapeless.error.details.refusal_kind).toBe("stale_context")
    expect(calls).toBe(0)
  })
})

test("a fresh session's dispatch_worker still reaches the core", async () => {
  await withReleaseLayout("v11.40.3", async () => {
    // An unknown lane falls through to the generic transport, so the core
    // answers and no stale refusal appears.
    const runner = runnerWithContext(coreEnvelope("concord_work_transition", "workflow_action", "error", {
      error: { kind: "invalid_input", retry_safe: true, recovery_action: { kind: "correct_request" }, effect_state: "none" },
    }))
    adapter.configureConcordAdapter({ runner })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", {
      work_id: "work-1", expected_version: 3, action_id: "dispatch_worker", idempotency_key: "idem-fresh-1", fields: { lane_id: "no-such-lane" },
    }), contextFor()))
    expect(runner.calls()).toBe(2)
    expect(result.error?.kind ?? result.outcome).not.toBe("stale_context")
  })
})

test("every result from a stale session carries one bounded staleness notice", async () => {
  const okRead = coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } })
  await withReleaseLayout("v11.40.6", async () => {
    adapter.configureConcordAdapter({ runner: runnerWithContext(okRead) })
    const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor()))
    expect(result.outcome).toBe("ok")
    expect(result.warnings).toHaveLength(1)
    expect(result.warnings[0]).toMatchObject({
      kind: "release_stale", source_id: "adapter",
      details: { pinned_release: "v11.40.3", installed_release: "v11.40.6", remedy: "restart this session to load the installed release" },
    })
    expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
    // A fresh session gains no notice.
    await withReleaseLayout("v11.40.3", async () => {
      adapter.configureConcordAdapter({ runner: runnerWithContext(okRead) })
      const fresh: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), contextFor()))
      expect(fresh.warnings).toEqual([])
    })
  })
  // A core envelope whose warnings array is already full keeps its notices
  // and carries the staleness notice on the output layer instead, so the
  // envelope never exceeds the bound this adapter itself enforces.
  await withReleaseLayout("v11.40.6", async () => {
    const full = coreEnvelope("concord_product_view", "resolve", "ok", {
      result: { product_id: "product-1", projects: [], stage: "prototype" },
      warnings: Array.from({ length: 16 }, (_, index) => ({ kind: `core_note_${index}` })),
    })
    expect(validateGeneratedEnvelope(full)).toBe(true)
    adapter.configureConcordAdapter({ runner: runnerWithContext(full) })
    const result: any = await adapter.product_view.execute(hostCall("resolve", {}), contextFor())
    const envelope = JSON.parse(result.output.slice(0, result.output.indexOf("\n")))
    expect(envelope.warnings).toHaveLength(16)
    expect(typeof result.output).toBe("string")
    expect(result.output).toContain("restart this session to load the installed release")
  })
})

test("a notice that would push a fitting envelope over the byte cap falls to the output layer", () => {
  const staleness = { pinnedReleaseRoot: "/d/v11.40.3", installedReleaseRoot: "/d/v11.40.6", pinnedRelease: "v11.40.3", installedRelease: "v11.40.6" }
  const bare = { schema_version: "1.0", outcome: "ok", warnings: [], filler: "" }
  const envelope = { ...bare, filler: "x".repeat(51200 - Buffer.byteLength(JSON.stringify(bare)) - 20) }
  expect(Buffer.byteLength(JSON.stringify(envelope))).toBeLessThan(51200)
  const settled = adapter.withReleaseStaleness(envelope, staleness)
  // The carried result stands unchanged and the notice rides the output
  // layer, so the encoder never degrades a successful result to
  // malformed_response because of the notice it added.
  expect(settled.envelope).toBe(envelope)
  expect((settled.envelope as Record<string, unknown>).warnings).toEqual([])
  expect(settled.extraWarnings).toHaveLength(1)
  expect(settled.extraWarnings[0]).toContain("restart this session to load the installed release")
  // The same envelope with room for the notice carries it in-envelope.
  const roomy = { ...bare, filler: "x".repeat(1000) }
  const carried = adapter.withReleaseStaleness(roomy, staleness)
  expect(carried.extraWarnings).toEqual([])
  expect((carried.envelope as Record<string, unknown>).warnings).toHaveLength(1)
  expect(((carried.envelope as Record<string, unknown>).warnings as any[])[0].kind).toBe("release_stale")
})

test("a stale session's non-dispatch mutation still runs and carries the notice", async () => {
  await withReleaseLayout("v11.40.6", async () => {
    adapter.configureConcordAdapter({ runner: runnerWithContext(approvalSuccess()) })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
      work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-stale-3",
    }), contextFor()))
    expect(result.outcome).toBe("ok")
    expect(result.warnings).toHaveLength(1)
    expect(result.warnings[0].kind).toBe("release_stale")
    expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  })
})

test("a stale work_start carries the staleness notice on its output layer", async () => {
  await withReleaseLayout("v11.40.6", async () => {
    adapter.configureConcordAdapter({ runner: { async run() { throw new Error("no core call may run before work_start validation") } } })
    const result: any = await adapter.work_start.execute({}, contextFor())
    expect(result.title).toBe("concord_work_start")
    const envelope = JSON.parse(result.output.slice(0, result.output.indexOf("\n")))
    expect(envelope.outcome).toBe("error")
    expect(envelope.error.kind).toBe("invalid_input")
    expect(result.output).toContain("restart this session to load the installed release")
  })
  expect(releaseDisplayName("/data/v11.40.3")).toBe("v11.40.3")
})
