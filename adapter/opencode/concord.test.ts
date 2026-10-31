import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { chmod, mkdir, mkdtemp as createDirectory, rm, symlink } from "node:fs/promises"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, hostToolSchemas, manifestDigest, maxEnvelopeBytes, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { configureCoreBinary } from "./core-runtime"
import { claimHostLease, configureHostLease, hostLeaseFault, releaseDisplayName, releaseStaleness, resolveInstalledReleaseRoot } from "./host-lease"
import { armedClaimedWorktree, clearClaimedWorktree, resetClaimedWorktrees, unlandedClaimedWorktree } from "./claimed-worktree"
import { validateGeneratedEnvelope, envelopeFailurePath, expandedPublishedRequestSchema, actionFieldsBranches } from "./generated-contract-tests"
import { hostControlPlane, SESSION_LIST_ROUTE, SESSION_ROUTE, type RouteResult } from "./move-session"
import { adoptManifestDigest, resetManifestPinForTesting } from "./manifest-pin"
import { resetMoveNotices, takeMoveNotice } from "./move-notice"
import { armTurnMoveBoundary, clearTurnMoveBoundary, questionRequiresNormalChat } from "./turn-move-boundary"
import { syntheticHostVersionFixture } from "./host-version.test-support"
import { mutationProjectionFixture } from "./mutation-projection.fixture"
import { fakeTool } from "./plugin-tool.fixture"
const testDirectories: string[] = []
async function mkdtemp(prefix: string): Promise<string> {
  const directory = await createDirectory(prefix)
  testDirectories.push(directory)
  return directory
}

syntheticHostVersionFixture()

mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const source = await Bun.file(new URL("./concord.ts", import.meta.url)).text()
const askingSource = await Bun.file(new URL("../../.concord/instructions/asking.md", import.meta.url)).text()
const continuationSource = await Bun.file(new URL("../../.concord/instructions/continuation.md", import.meta.url)).text()
const recordsSource = await Bun.file(new URL("../../.concord/instructions/records.md", import.meta.url)).text()
// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

const adapter = await import("./concord")

// The suite never touches a terminal surface: work_start owns no terminal
// command, so no test needs to simulate or clear terminal environment state.
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
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
})
afterEach(async () => {
  resetClaimedWorktrees()
  if (outerSelectedProductID === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
  else process.env.CONCORD_SELECTED_PRODUCT_ID = outerSelectedProductID
  await Promise.all(testDirectories.splice(0).map(directory => rm(directory, { recursive: true, force: true })))
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
    // The published input is compact: the request parent keeps its
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
  const fieldsBranches = actionFieldsBranches(actionBranch)
  expect(fieldsBranches, "approve_contract fields branches").toHaveLength(1)
  expect(fieldsBranches[0].required).toContain("decision_domains")
  expect(fieldsBranches[0].properties.decision_domains).toBeObject()
  expect(fieldsBranches[0].additionalProperties).toBe(false)
  // CON-412: the items close per kind; each branch binds outcome_kind to
  // outcome_payload.kind and carries that branch's payload inline.
  for (const fields of fieldsBranches) {
    const itemVariants = fields.properties.outcome_predicates.items.oneOf
    expect(itemVariants.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
    expect(itemVariants.map((branch: any) => branch.properties.outcome_payload.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  }
  // The branch names the conditional action fields, so a calling agent can
  // read them before calling.
  for (const field of ["action_id"]) {
    expect(actionBranch.properties[field], field).toBeObject()
  }
  // The approval premise an author writes is the published action field, so
  // it carries the unit guidance: code points, the UTF-8 byte admission, and
  // its distinction from packet and model-token limits.
  for (const fields of fieldsBranches) {
    const premise = fields.properties.premise
    expect(premise.maxLength).toBe(4_096)
    expect(premise.description).toContain("Unicode code points")
    expect(premise.description).toContain("UTF-8 bytes")
    expect(premise.description).toContain("model-token limit")
    expect(premise.description).toContain("Do not truncate an approved objective")
  }
  // Every generated field reaches the host through the registration map,
  // which derives its field set from the generated capture/resume branches —
  // no handwritten parity list. The resume branch carries exactly one of
  // work_id or issue_key: the resume claims in the calling session's Project,
  // and no Project selector is published on either mode.
  const [generatedCapture, generatedResume] = hostToolSchemas.concord_work_start.oneOf
  const expectedWorkStartFields = Object.keys({ ...generatedCapture.properties, ...generatedResume.properties })
  expect(Object.keys((adapter.work_start as any).args).sort(), "work_start registration fields").toEqual(expectedWorkStartFields.sort())
  for (const value of Object.values((adapter.work_start as any).args)) expect(value).toBeObject()
  expect((adapter.work_start as any).args.product_id).toBeUndefined()
  expect((adapter.work_start as any).args.project_id).toBeUndefined()
  expect(Object.keys(generatedResume.properties)).toEqual(["issue_key", "work_id"])
  expect((generatedResume as any).oneOf).toEqual([{ required: ["work_id"] }, { required: ["issue_key"] }])
  // Conditional branches preserve both generated closed modes without a
  // root union that Anthropic's tool-schema validator would reject. The
  // published branches may share repeated field schemas through local
  // definitions, so the mode comparison runs on the resolved view.
  const workStartDefinition = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
  const publishedWorkStart = workStartDefinition.jsonSchema as any
  expect(publishedWorkStart.if).toEqual({ required: ["work_id"] })
  expect(publishedWorkStart.else.if).toEqual({ required: ["issue_key"] })
  const expandedWorkStart: any = expandedPublishedRequestSchema(publishedWorkStart)
  expect(expandedWorkStart.else.else).toEqual(generatedCapture)
  expect(expandedWorkStart.then).toEqual(expandedPublishedRequestSchema(generatedResume))
  expect(expandedWorkStart.else.then).toEqual(expandedPublishedRequestSchema(generatedResume))
})

test("work define publishes the record-only issue identity contract", () => {
  const published = adapter.publishedRequestSchema("concord_work_define") as any
  expect(published.properties.operation.enum).toContain("issue_link_record")
  expect(published.properties.operation.enum).not.toContain("issue_adopt")
  const branch = published.oneOf.find((item: any) => item.properties.operation.const === "issue_link_record")
  const input = branch.properties.input
  expect(input.required).toEqual(["work_id", "human_key", "remote_issue_uuid", "url", "idempotency_key"])
  expect(input.additionalProperties).toBe(false)
  expect(Object.keys(input.properties).sort()).toEqual(["human_key", "idempotency_key", "remote_issue_uuid", "requested_budget_seconds", "url", "work_id"])
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
      expect(envelope.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "correct_request" } })
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
    expect(envelope.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "correct_request" } })
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

test("transport boundaries stay fail-closed", () => {
  expect(source).toContain("concordBinaryPath(), \"invoke\"")
  expect(source).toContain("malformed_core_response")
  expect(source).toContain("operation_conflict")
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
  const result: any = await runProduct(runnerWithContext((_argv: string[]) => foreignDigest(coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } }))))
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
    expect(result.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "correct_request" } })
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
  expect(malformedResponse.error.kind).toBe("operation_conflict")
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

test("malformed mutation successes use the same reconciliation wrapper", async () => {
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
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? invalid : readback), stderr: "" }
  } })
  expect(calls).toBe(4)
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

test("runner exceptions reconcile the request work ID", async () => {
  const readback = coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) throw new Error("runner failed during mutation")
    return { exitCode: 0, stdout: JSON.stringify(readback), stderr: "" }
  } })
  expect(calls).toBe(4)
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
  expect(result.error.kind).toBe("operation_conflict")
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
test("a native subagent call preserves ordinary caller grants without an ancestry read", async () => {
  hostControlPlane().bind({
    get: async ({ path }) => {
      expect(path?.id).toBe("session-1")
      return { data: { id: "session-1", directory: "/worktree", parentID: "native-parent" }, response: new Response(null, { status: 200 }) }
    },
    post: async () => { throw new Error("a read creates no session") },
  })
  let seen: any
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], input: string) => {
    seen = JSON.parse(input)
    return coreEnvelope("concord_product_view", "resolve", "error", {
      error: { kind: "unauthorized", retry_safe: false, effect_state: "none", recovery_action: { kind: "contact_operator" } },
    })
  }) })
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", {}), { ...contextFor(), agent: "native-helper" }))
  expect(seen.call_envelope).toMatchObject({ session_ref: "session-1", agent_ref: "native-helper", client_ref: "opencode", directory: "/worktree" })
  expect(result.origin).toBe("core")
  expect(result.error.kind).toBe("unauthorized")
  expect(result.error.effect_state).toBe("none")
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
})

test("an unreadable live session directory refuses before core transport", async () => {
  hostControlPlane().bind({
    get: async () => ({ data: null, response: new Response(null, { status: 404 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.knowledge.execute(hostCall("search", {}), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("transport_failure")
  expect(result.error.adapter_reason).toBe("session_directory_unreadable")
  expect(result.error.effect_state).toBe("none")
  expect(runner.calls()).toEqual([])
})

test("an unbound control plane refuses a concord call closed", async () => {
  hostControlPlane().bind(undefined)
  const runner = recordingRunner()
  adapter.configureConcordAdapter({ runner })
  const result: any = await rawHostResult(adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor()))
  assertAdapterEnvelope(result)
  expect(result.error.kind).toBe("transport_failure")
  expect(result.error.adapter_reason).toBe("session_directory_unreadable")
  expect(result.error.effect_state).toBe("none")
  expect(runner.calls()).toEqual([])
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

const runTransition = (runner: any, ask?: () => Promise<void>) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "idem-1" }), contextFor(ask)))
}

const runWorkflowAction = (runner: any, input: Record<string, unknown>, ask?: () => Promise<void>) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", input), contextFor(ask)))
}
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
  // outcome_payload.kind and carries that branch's payload inline. The
  // check runs on each branch of the fields union, so a branch-only drop
  // cannot hide behind the factored parent.
  for (const fields of actionFieldsBranches(actionBranch)) {
    const itemVariants = fields.properties.outcome_predicates.items.oneOf
    expect(itemVariants.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
    expect(itemVariants.map((branch: any) => branch.properties.outcome_payload.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  }
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

test("confirm_premise refusals come from the core, not the adapter", async () => {
  // The core owns the investigation-artifact gate and its refusal.
  const artifactRefusal = coreEnvelope("concord_work_transition", "workflow_action", "error", {
    error: { kind: "missing_evidence", retry_safe: false, recovery_action: { kind: "provide_evidence" }, effect_state: "none", message: "premise confirmation requires a recorded investigation artifact naming a current Domain of the Product and another work item; its refs must hold the bare domain_id of the current Domain and the id of another work item" },
  })
  const first = runnerWithContext(artifactRefusal)
  const result: any = await runWorkflowAction(first,
    { work_id: "work-1", expected_version: 2, action_id: "confirm_premise", idempotency_key: "idem-gate" })
  expect(first.calls()).toBe(2)
  expect(result.outcome).toBe("error")
  expect(result.origin).toBe("core")
  expect(result.error.message).toContain("investigation artifact")

})

const coreEnvelope = <T extends Record<string, unknown>>(tool: string, operation: string, outcome: string, fields: T) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool, operation, ...((contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any)?.query_id ? { query_id: (contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any).query_id } : {}), outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

for (const tampered of [false, true]) {
  test(`host mutation privately reconstructs projected pins before status; tampered=${tampered}`, async () => {
    const fixture = mutationProjectionFixture()
    if (tampered) fixture.pages[1].result.offset++
    const titles: unknown[] = []
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
      patch: async options => { titles.push(options.body); return { response: new Response(null, { status: 200 }) } },
    })
    let reads = 0
    let writes = 0
    adapter.configureConcordAdapter({ runner: { async run(argv, input) {
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      const request = JSON.parse(input)
      if (request.operation === "message_send") {
        writes++
        return { exitCode: 0, stdout: JSON.stringify(coreEnvelope(request.tool, request.operation, "ok", fixture.envelope)), stderr: "" }
      }
      expect([request.tool, request.operation]).toEqual(["concord_work_trace", "mutation_result"])
      expect(request.input).toEqual({ ...fixture.pointer, page: { cursor: reads === 0 ? null : String(reads), limit: 1 } })
      const page = fixture.pages[reads++]
      return { exitCode: 0, stdout: JSON.stringify(coreEnvelope(request.tool, request.operation, "ok", page)), stderr: "" }
    } } })
    const raw = await adapter.work_relate.execute(hostCall("message_send", { work_id: "work-projection-0", recipient_work_id: "work-projection-1", body: "A complete message", expected_version: 2, idempotency_key: "projection-key" }), contextFor())
    const output = typeof raw === "string" ? raw : raw.output
    const result = JSON.parse(output.split("\n")[0])
    expect(result.outcome).toBe("ok")
    expect(result.result).toEqual(fixture.envelope.result)
    expect(result.changed_refs).toEqual(fixture.envelope.changed_refs)
    expect(result.next_valid_intents).toEqual([])
    expect(Buffer.byteLength(output)).toBeLessThan(maxEnvelopeBytes)
    expect(result.result.work_pins).toBeUndefined()
    expect(writes).toBe(1)
    if (tampered) {
      expect(titles).toEqual([])
      expect(reads).toBe(2)
      expect(result.warnings.some((notice: any) => notice.kind === "mutation_result_unavailable")).toBe(true)
    } else {
      expect(titles).toHaveLength(16)
      expect(titles[0]).toEqual({ title: "Goal: Lossless result 😀 0" })
      expect(reads).toBe(fixture.pages.length)
    }
  })
}

for (const blockedVerb of ["project-resolve", "invoke"] as const) {
    test(`mutation cancellation during ${blockedVerb} preserves its effect boundary`, async () => {
      const controller = new AbortController()
      const calls: string[] = []
      const signals: AbortSignal[] = []
      let abortedAt: number | null = null
      adapter.configureConcordAdapter({ runner: { async run(argv, input, signal) {
        calls.push(argv[1])
        signals.push(signal)
        if (signal.aborted) throw signal.reason
        if (argv[1] === "invoke") expect(JSON.parse(input).input.requested_budget_seconds).toBe(1)
        if (argv[1] !== blockedVerb) return { exitCode: 0, stdout: JSON.stringify(contextResponse(false)), stderr: "" }
        return await new Promise<{ exitCode: number; stdout: string; stderr: string }>((resolve, reject) => {
          const abort = () => {
            abortedAt = performance.now()
            clearTimeout(timer)
            reject(signal.reason)
          }
          const timer = setTimeout(() => {
            signal.removeEventListener("abort", abort)
            resolve({ exitCode: 0, stdout: "{}", stderr: "" })
          }, 2_000)
          signal.addEventListener("abort", abort, { once: true })
          controller.abort()
        })
      } } })
      const started = performance.now()
      try {
        const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
          work_id: "work-1", expected_version: 2, target: "in_progress", reason: "Start work",
          idempotency_key: "recover-interruption", requested_budget_seconds: 1,
        }), contextFor(() => {}, controller)))
        assertAdapterEnvelope(result)
        expect(result.outcome).toBe("error")
        // An uncertain mutation attempts readback with the same cancelled
        // signal; it cannot start another invocation.
        expect(calls).toEqual(blockedVerb === "project-resolve" ? ["project-resolve"] : ["project-resolve", "invoke", "project-resolve"])
        if (blockedVerb === "project-resolve") {
          expect(result.error.kind, JSON.stringify(result)).toBe("cancelled")
          expect(result.error.adapter_reason).toBe("cancelled_no_effect")
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
        expect(signals[0]).toBe(controller.signal)
        expect(signals[0].reason.name).toBe("AbortError")
      } finally {
        adapter.configureConcordAdapter({ reset: true })
      }
    })
}

    test("mutation cancellation during live directory resolution preserves its cause", async () => {
      const controller = new AbortController()
      const signals: AbortSignal[] = []
      const calls: string[] = []
      let abortedAt: number | null = null
      hostControlPlane().bind({
        get: async ({ signal }) => {
          signals.push(signal!)
          signal!.throwIfAborted()
          return await new Promise<RouteResult>((resolve, reject) => {
            const abort = () => { abortedAt = performance.now(); clearTimeout(timer); reject(signal!.reason) }
            const timer = setTimeout(() => {
              signal!.removeEventListener("abort", abort)
              resolve({ data: { id: "session-1", directory: "/worktree" }, response: new Response("{}") })
            }, 2_000)
            signal!.addEventListener("abort", abort, { once: true })
            controller.abort()
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
        const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
          work_id: "work-1", expected_version: 2, target: "in_progress", reason: "Start work",
          idempotency_key: "recover-context-read", requested_budget_seconds: 1,
        }), contextFor(() => {}, controller)))
        assertAdapterEnvelope(result)
        expect(result.error.kind, JSON.stringify(result)).toBe("cancelled")
        expect(result.error.effect_state).toBe("none")
        expect(result.error.recovery_action.kind).toBe("retry_same_request")
        expect(calls).toEqual([])
        expect(new Set(signals).size).toBe(1)
        expect(signals[0]).toBe(controller.signal)
        expect(abortedAt).not.toBeNull()
        expect(abortedAt! - started).toBeLessThan(1_600)
      } finally {
        adapter.configureConcordAdapter({ reset: true })
      }
    })

test("mutation cancellation kills the default runner's context child", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-context-deadline-"))
  const binary = join(root, "synthetic-core")
  await Bun.write(binary, "#!/usr/bin/env bun\nawait new Promise(resolve => setTimeout(resolve, 2_000))\n")
  await chmod(binary, 0o700)
  configureCoreBinary(binary)
  adapter.configureConcordAdapter({ reset: true })
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 100)
  const started = performance.now()
  try {
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
      work_id: "work-1", expected_version: 2, target: "in_progress", reason: "Start work",
      idempotency_key: "recover-default-runner", requested_budget_seconds: 1,
    }), contextFor(() => {}, controller)))
    assertAdapterEnvelope(result)
    expect(result.error.kind, JSON.stringify(result)).toBe("cancelled")
    expect(result.error.effect_state).toBe("none")
    expect(result.error.recovery_action.kind).toBe("retry_same_request")
    expect(performance.now() - started).toBeLessThan(1_600)
  } finally {
    clearTimeout(timer)
    configureCoreBinary("concord")
    adapter.configureConcordAdapter({ reset: true })
    await rm(root, { recursive: true, force: true })
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

test("overlap resolution and stale-law refusal pass the generated adapter boundary", async () => {
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
    resolution_kind: "depends_on", reason: "The second item lands after the first.",
    idempotency_key: "resolve-overlap-1",
  }), contextFor()))
  expect(resolved.outcome).toBe("ok")

  const staleLaw = coreEnvelope("concord_work_transition", "lifecycle", "error", {
    error: {
      kind: "stale_law_revision", retry_safe: false,
      recovery_action: { kind: "use_declared_route", required_refs: ["supersede_contract", "terminal_work"] }, effect_state: "none",
      stale_law_revision: {
        old_law_id: "law-old", old_content_hash: `sha256:${"a".repeat(64)}`,
        recovery_actions: ["supersede_contract", "terminal_work"],
      },
    },
  })
  expect(validateGeneratedEnvelope(staleLaw)).toBe(true)
  const stale: any = await runTransition(runnerWithContext(staleLaw))
  expect(stale.error.kind).toBe("stale_law_revision")
  expect(stale.error.stale_law_revision).toEqual({
    old_law_id: "law-old", old_content_hash: `sha256:${"a".repeat(64)}`,
    recovery_actions: ["supersede_contract", "terminal_work"],
  })
  const withRevision = (revision: unknown) => ({ ...staleLaw, error: { ...staleLaw.error, stale_law_revision: revision } })
  for (const extra of [
    { accepted_successor_law_id: "law-current" },
    { accepted_successor_content_hash: `sha256:${"b".repeat(64)}` },
  ]) {
    expect(validateGeneratedEnvelope(withRevision({ ...stale.error.stale_law_revision, ...extra }))).toBe(false)
  }
  expect(validateGeneratedEnvelope(withRevision({ ...stale.error.stale_law_revision, old_content_hash: "sha256:not-a-proof" }))).toBe(false)
})

test("cross-repository placement passes the generated adapter boundary", async () => {
  bindRetargetRoute()
  const error = {
    kind: "cross_repository_claim", retry_safe: false,
    recovery_action: { kind: "use_declared_route", required_refs: ["concord_work_start"] }, effect_state: "none",
    session_placement: { work_id: "work-1", project_id: "project-2", directory: "/example/second-repository" },
  }
  const refusal = coreEnvelope("concord_work_transition", "worktree_claim", "error", { error })
  expect(validateGeneratedEnvelope(refusal)).toBe(true)
  adapter.configureConcordAdapter({ runner: runnerWithContext(refusal) })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worktree_claim", { work_id: "work-1", project_id: "project-2", base_sha: "a".repeat(40), expected_version: 1, idempotency_key: "cross-repository-claim" }), contextFor()))
  expect(result.error).toMatchObject({ kind: "cross_repository_claim", session_placement: error.session_placement })
  expect(validateGeneratedEnvelope({ ...refusal, error: { ...error, session_placement: undefined } })).toBe(false)
  expect(validateGeneratedEnvelope({ ...refusal, error: { ...error, kind: "invalid_input" } })).toBe(false)
  expect(validateGeneratedEnvelope({ ...refusal, error: { ...error, session_placement: { ...error.session_placement, directory: ".git" } } })).toBe(false)
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
    expect(result.error.effect_state).toBe("possible")
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
  expect(mismatch.error.effect_state).toBe("partial")
  expect(mismatch.error.recovery_action.kind).toBe("retry_same_request")
  // The work item and worktree that exist ride along so the replay's target is visible.
  expect(mismatch.work_id).toBe("work-1")
  // No rollback ran: nothing recorded intent, so nothing needs compensating.
  expect(calls).toBe(3)
})

test("child stdout keeps a bounded diagnostic tail", async () => {
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
  expect(captured.stdout).toBe(source.slice(-65_536))
  expect(captured.stdout).toContain('"reason":"stop"')
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
// worktree. A failed start retains the capture and claim; replay under the
// same key converges without compensation.
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

const bootstrapRetainedIdentity = () => ({
  operation_id: "bootstrap-1", product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: WORKTREE,
})

for (const effect of ["none", "possible", "partial"] as const) {
  for (const exitCode of [1, 2]) {
    test(`work start preserves structured bootstrap ${effect} at exit ${exitCode}`, async () => {
      const { moved } = bindRetargetRoute()
      const calls: RetargetCall[] = []
      const identity = effect === "partial" ? bootstrapRetainedIdentity() : {}
      adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
        "work-bootstrap": () => ({ exitCode, stdout: JSON.stringify({ schema_version: "1.0", outcome: "error", effect_state: effect, ...identity }), stderr: "synthetic bootstrap diagnostic" }),
      }) })
      const result = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
      expect(result).toMatchObject({ outcome: "error", error: {
        kind: "bootstrap_failure", effect_state: effect, retry_safe: exitCode === 1,
        recovery_action: { kind: exitCode === 1 ? "retry_same_request" : "contact_operator" },
      } })
      expect(result.error.message).toContain("synthetic bootstrap diagnostic")
      if (effect === "partial") {
        expect(result).toMatchObject({ product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: WORKTREE })
        if (exitCode === 1) expect(result.error.message).toContain("Do not submit a new capture key")
        else expect(result.error.message).toBe("synthetic bootstrap diagnostic")
      } else {
        expect(result.work_id).toBeUndefined()
        expect(result.worktree_path).toBeUndefined()
      }
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap"])
      expect(moved).toEqual([])
      expect(armedClaimedWorktree("session-1")).toBeNull()
    })
  }
}

for (const [label, body] of [
  ["unsupported schema", { schema_version: "2.0", outcome: "error", effect_state: "none" }],
  ["wrong outcome", { schema_version: "1.0", outcome: "ok", effect_state: "none" }],
  ["unknown effect", { schema_version: "1.0", outcome: "error", effect_state: "complete" }],
  ["extra field", { schema_version: "1.0", outcome: "error", effect_state: "none", extra: true }],
  ["no-effect identity", { schema_version: "1.0", outcome: "error", effect_state: "none", ...bootstrapRetainedIdentity() }],
  ["invalid work identity", { schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity(), work_id: "invalid identity" }],
  ["relative path", { schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity(), worktree_path: "relative" }],
  ["non-string identity", { schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity(), operation_id: [] }],
  ["wrong Product", { schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity(), product_id: "product-other" }],
  ["wrong Project", { schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity(), project_id: "project-other" }],
  ["success shape on failure exit", bootstrapSuccess()],
] as const) {
  for (const exitCode of [1, 2]) {
    test(`work start distrusts ${label} bootstrap failure at exit ${exitCode}`, async () => {
      const { moved } = bindRetargetRoute()
      const calls: RetargetCall[] = []
      adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
        "work-bootstrap": () => ({ exitCode, stdout: JSON.stringify(body), stderr: "synthetic bootstrap diagnostic" }),
      }) })
      const result = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
      expect(result).toMatchObject({ outcome: "error", error: {
        effect_state: "possible", retry_safe: exitCode === 1,
        recovery_action: { kind: exitCode === 1 ? "retry_same_request" : "contact_operator" },
      } })
      expect(result.work_id).toBeUndefined()
      expect(result.worktree_path).toBeUndefined()
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap"])
      expect(moved).toEqual([])
      expect(armedClaimedWorktree("session-1")).toBeNull()
    })
  }
}

for (const scope of ["product_id", "project_id"] as const) {
  test(`work start distrusts a successful bootstrap from another ${scope}`, async () => {
    const { moved } = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
      "work-bootstrap": () => ({ exitCode: 0, stdout: JSON.stringify({ ...bootstrapSuccess(), [scope]: "foreign" }), stderr: "" }),
    }) })
    const result = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
    expect(result).toMatchObject({ outcome: "error", error: { kind: "malformed_response", effect_state: "possible" } })
    expect(result.work_id).toBeUndefined()
    expect(result.worktree_path).toBeUndefined()
    expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap"])
    expect(moved).toEqual([])
  })
}

for (const [label, failure] of [
  ["transport exception", () => { throw new Error("synthetic lost child acknowledgement") }],
  ["empty failed response", () => ({ exitCode: 1, stdout: "", stderr: "synthetic child failure" })],
  ["malformed failed response", () => ({ exitCode: 1, stdout: "not-json", stderr: "synthetic child failure" })],
  ["multiple failed responses", () => ({ exitCode: 1, stdout: '{}\n{}\n', stderr: "synthetic child failure" })],
  ["non-object failed response", () => ({ exitCode: 1, stdout: '[]', stderr: "synthetic child failure" })],
  ["empty successful response", () => ({ exitCode: 0, stdout: "", stderr: "" })],
] as const) {
  for (const resume of [false, true]) {
    test(`work start ${resume ? "resume" : "capture"} reports possible effect after ${label}`, async () => {
      const { moved } = bindRetargetRoute()
      const calls: RetargetCall[] = []
      const command = resume ? "work-resume" : "work-bootstrap"
      adapter.configureConcordAdapter({ runner: (resume ? resumeRunner : retargetRunner)(calls, { [command]: failure }) })
      const result = await rawHostResult(adapter.work_start.execute(resume ? { work_id: "work-1" } : bootstrapArgs, contextFor()))
      const retrySafe = label !== "transport exception"
      expect(result).toMatchObject({ outcome: "error", error: { effect_state: "possible", retry_safe: retrySafe, recovery_action: { kind: retrySafe ? "retry_same_request" : "contact_operator" } } })
      expect(result.work_id).toBeUndefined()
      expect(result.worktree_path).toBeUndefined()
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", command])
      expect(moved).toEqual([])
    })
  }
}

for (const resume of [false, true]) {
  test(`work start ${resume ? "resume" : "capture"} keeps partial effect after a post-success transport failure`, async () => {
    const { moved } = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: (resume ? resumeRunner : retargetRunner)(calls, {
      "session-prepare": () => { throw new Error("synthetic prepare transport failure") },
    }) })
    const result = await rawHostResult(adapter.work_start.execute(resume ? { work_id: "work-1" } : bootstrapArgs, contextFor()))
    expect(result).toMatchObject({ outcome: "error", product_id: "product-1", project_id: "project-1", work_id: "work-1", worktree_path: WORKTREE, error: { kind: "transport_failure", effect_state: "partial", retry_safe: false, recovery_action: { kind: "contact_operator" } } })
    expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", resume ? "work-resume" : "work-bootstrap", "session-prepare"])
    expect(moved).toEqual([])
  })
}

test("work start replays the same key and retained identity after structured partial bootstrap failure", async () => {
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  let attempts = 0
  adapter.configureConcordAdapter({ runner: retargetRunner(calls, {
    "work-bootstrap": () => ++attempts === 1
      ? { exitCode: 1, stdout: JSON.stringify({ schema_version: "1.0", outcome: "error", effect_state: "partial", ...bootstrapRetainedIdentity() }), stderr: "synthetic finalize failure" }
      : { exitCode: 0, stdout: JSON.stringify({ ...bootstrapSuccess(), replayed: true }), stderr: "" },
  }) })
  const refused = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(refused).toMatchObject({ outcome: "error", work_id: "work-1", worktree_path: WORKTREE, error: { effect_state: "partial" } })
  const replay = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
  expect(replay).toMatchObject({ outcome: "ok", work_id: refused.work_id, worktree_path: refused.worktree_path })
  const inputs = calls.filter(({ argv }) => argv[1] === "work-bootstrap").map(({ input }) => JSON.parse(input))
  expect(inputs).toHaveLength(2)
  expect(inputs[0]).toEqual(inputs[1])
  expect(inputs[0].idempotency_key).toBe(bootstrapArgs.idempotency_key)
  expect(armedClaimedWorktree("session-1")).toBe(WORKTREE)
})

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
    expect(result.error.message).toContain("this refusal arms no turn-move boundary")
    expect(result.error.message).not.toContain("A turn-move boundary is active")
    // A native wake does not replace the target-context confirmation.
    expect(result.error.message).toContain("one automatic continuation")
    expect(result.error.message).toContain("not placement proof")
    expect(result.error.message).toContain(`actually resolves in ${JSON.stringify(WORKTREE)}`)
    expect(result.error.message).toContain("replay this same work_start request")
    expect(result.error.effect_state).toBe("partial")
    expect(result.error.recovery_action.kind).toBe("retry_same_request")
    // The work item and worktree that exist ride along so the replay's target is visible.
    expect(result.work_id).toBe("work-1")
    expect(result.worktree_path).toBe(WORKTREE)
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
    expect(questionRequiresNormalChat("session-1")).toBe(true)
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

const resumeCallEnvelope = () => ({
  schema_version: "1.0", request_id: "session-1-message-1", client_ref: expect.any(String), principal_ref: "",
  session_ref: "session-1", agent_ref: "agent-1", directory: WORKTREE, worktree: WORKTREE,
  ambient_project_id: "project-1", selected_product_id: "product-1", scope_version: "1", manifest_digest: manifestDigest,
})

const resumeRunner = (calls: RetargetCall[], overrides: Record<string, () => { exitCode: number; stdout: string; stderr: string }> = {}) => ({
  async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
    calls.push({ argv, input, options })
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
  expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", work_id: "work-1", session_ref: "session-1", call_envelope: resumeCallEnvelope() })
  expect(JSON.parse(calls[4].input)).toEqual(JSON.parse(calls[1].input))
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
test("work start resume refuses incomplete restoration facts before movement", async () => {
  for (const fields of [
    { restored: true },
    { restored_tip: "b".repeat(40), restoration_warning: "loss warning" },
    { restored: false, restored_tip: "b".repeat(40), restoration_warning: "loss warning" },
    { restored: true, restored_tip: "not-a-commit", restoration_warning: "loss warning" },
    { restored: true, restored_tip: "b".repeat(40), restoration_warning: "" },
  ]) {
    const { moved } = bindRetargetRoute()
    const response = resumeSuccess()
    adapter.configureConcordAdapter({ runner: resumeRunner([], {
      "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...response, worktree: { ...response.worktree, ...fields } }), stderr: "" }),
    }) })
    const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
    expect(result.error.kind).toBe("malformed_response")
    expect(moved).toEqual([])
  }
})

test("work start resume reports restored work and the lost uncommitted changes", async () => {
  bindRetargetRoute()
  const response = resumeSuccess()
  const warning = "The recorded worktree was restored from its surviving branch. Uncommitted changes in the lost directory are not recoverable."
  adapter.configureConcordAdapter({ runner: resumeRunner([], {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...response, worktree: { ...response.worktree, restored: true, restored_tip: "b".repeat(40), restoration_warning: warning } }), stderr: "" }),
  }) })
  const raw: any = await adapter.work_start.execute({ work_id: "work-1" }, landedContextFor())
  expect(envelopeLine(raw.output)).toMatchObject({ outcome: "ok", work_id: "work-1" })
  expect(raw.output).toContain(warning)
  expect(raw.output).toContain("b".repeat(40))
})

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
  expect(refused.error.effect_state).toBe("possible")
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
  bindRetargetRoute()
  const calls: RetargetCall[] = []
  let resumeReads = 0
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], input: string, _signal: AbortSignal, options?: any) {
      calls.push({ argv, input, options })
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
  expect(refused.error.effect_state).toBe("partial")
  expect(refused.error.recovery_action.kind).toBe("contact_operator")
  expect(refused.error.retry_safe).toBe(false)
  expect(resumeReads).toBe(2)
  // Unconfirmed placement cannot consume the handoff.
  expect(calls.some(({ argv }) => argv[1] === "invoke")).toBe(false)
  expect(armedClaimedWorktree("session-1")).toBeNull()
})

test("work start resume keeps a retryable post-landing re-read failure on the replay route", async () => {
  bindRetargetRoute()
  let resumeReads = 0
  adapter.configureConcordAdapter({ runner: {
    async run(argv: string[], _input: string, _signal: AbortSignal, _options?: any) {
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
  expect(failed.error.effect_state).toBe("partial")
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
  changes: ["adapter/opencode: resume route"],
  verification: ["bun test adapter/opencode/ pass"],
  artifact_refs: null,
  blockers: [],
  next_action: "consume the handoff and verify the receiving route",
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
  expect(result.error.effect_state).toBe("partial")
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
  expect(result.error.effect_state).toBe("partial")
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

test("work start issue-key resume uses the CLI identity through verified landing and readback", async () => {
  const { moved } = bindRetargetRoute()
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
    "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), linear_issue: recordedLinearIssue() }), stderr: "" }),
  }) })
  const result: any = await rawHostResult(adapter.work_start.execute({ issue_key: "EX-3" }, landedContextFor()))
  expect(result).toMatchObject({ outcome: "ok", work_id: "work-1", session_id: "session-1", linear_issue: recordedLinearIssue() })
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume", "session-prepare", "claim-landing", "work-resume"])
  expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", issue_key: "EX-3", session_ref: "session-1", call_envelope: resumeCallEnvelope() })
  expect(JSON.parse(calls[2].input)).toEqual({ product_id: "product-1", work_id: "work-1", task: "", agent: "agent-1" })
  expect(JSON.parse(calls[3].input)).toMatchObject({ work_id: "work-1", session_ref: "session-1", landed_directory: WORKTREE, host_pid: process.pid })
  expect(JSON.parse(calls[4].input)).toEqual({ product_id: "product-1", project_id: "project-1", work_id: "work-1", session_ref: "session-1", call_envelope: resumeCallEnvelope() })
  expect(moved).toEqual([{ sessionID: "session-1", destination: { directory: WORKTREE } }])
  expect(armedClaimedWorktree("session-1")).toBe(WORKTREE)
})

test("work start accepts exact issue-key bounds through the same resume route", async () => {
  for (const issue_key of ["A-1", `E${"X".repeat(61)}-3`]) {
    clearClaimedWorktree("session-1")
    bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
      "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify({ ...resumeSuccess(), linear_issue: { ...recordedLinearIssue(), human_key: issue_key } }), stderr: "" }),
    }) })
    const result: any = await rawHostResult(adapter.work_start.execute({ issue_key }, landedContextFor()))
    expect(result).toMatchObject({ outcome: "ok", work_id: "work-1" })
    expect(JSON.parse(calls[1].input).issue_key).toBe(issue_key)
    expect(JSON.parse(calls[4].input).work_id).toBe("work-1")
  }
})

test("work start issue-key resume forwards core resolution refusals before claim or movement", async () => {
  for (const stderr of [
    "concord work-resume: store: linear_link_resolve: unknown_scope: no work item records that Linear issue key\nrecovery: supply a recorded Linear issue key or an explicit work_id",
    "concord work-resume: store: linear_link_resolve: ambiguous_scope: multiple work items record that Linear issue key\nrecovery: supply an explicit work_id",
    "concord work-resume: store: work_resume: unknown_scope: work item is outside the requested Product and Project",
  ]) {
    clearClaimedWorktree("session-1")
    const { moved } = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
      "work-resume": () => ({ exitCode: 2, stdout: "", stderr }),
    }) })
    const result: any = await rawHostResult(adapter.work_start.execute({ issue_key: "EX-3" }, landedContextFor()))
    expect(result.outcome).toBe("error")
    expect(result.error).toMatchObject({ kind: "resume_failure", effect_state: "possible", retry_safe: false, recovery_action: { kind: "contact_operator" } })
    expect(result.error.message).toBe(stderr)
    expect(result.work_id).toBeUndefined()
    expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
    expect(JSON.parse(calls[1].input)).toEqual({ product_id: "product-1", project_id: "project-1", issue_key: "EX-3", session_ref: "session-1", call_envelope: resumeCallEnvelope() })
    expect(moved).toEqual([])
    expect(armedClaimedWorktree("session-1")).toBeNull()
  }
})

test("work start issue-key resume refuses an unbound or invalid returned identity before movement", async () => {
  const response = resumeSuccess()
  for (const body of [
    response,
    { ...response, linear_issue: { ...recordedLinearIssue(), human_key: "EX-4" } },
    { ...response, work_id: "invalid identity", linear_issue: recordedLinearIssue() },
    { ...response, work_id: "", linear_issue: recordedLinearIssue() },
    { ...response, project_id: "project-2", linear_issue: recordedLinearIssue() },
  ]) {
    clearClaimedWorktree("session-1")
    const { moved } = bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: resumeRunner(calls, {
      "work-resume": () => ({ exitCode: 0, stdout: JSON.stringify(body), stderr: "" }),
    }) })
    const result: any = await rawHostResult(adapter.work_start.execute({ issue_key: "EX-3" }, landedContextFor()))
    expect(result.outcome, JSON.stringify(body)).toBe("error")
    expect(result.error.kind).toBe("malformed_response")
    expect(result.error.message).toContain("strict resume contract")
    expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-resume"])
    expect(moved).toEqual([])
    expect(armedClaimedWorktree("session-1")).toBeNull()
  }
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

test("work start resume rejects mixed and malformed argument shapes", async () => {
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

// The resume contract is exactly { work_id }. A Project selector is
// an undeclared argument on the published resume branch, so the shape refusal
// sits in front of every effect: no canonical-path read, no second-session
// route, and no move. Resuming work that lives in another repository is the
// host-started session's job, through this same work_id-only shape.
test("work start resume rejects a project_id selector before any effect", async () => {
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], input: string) {
    calls.push({ argv, input })
    throw new Error("the resume shape refusal must precede every child")
  } } })
  const result: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1", project_id: "project-2" }, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("invalid_input")
  expect(result.error.message).toContain("host-tool contract")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.retry_safe).toBe(false)
  expect(result.error.recovery_action.kind).toBe("correct_request")
  expect(calls).toEqual([])
})

// A successful work start forks no terminal command, on capture or
// on resume. The terminal surface belongs to the host; the start's children
// are only the core reads and writes the boot flow owns.
test("a successful work start forks no terminal command", async () => {
  const terminalRoot = "zellij"
  process.env.ZELLIJ_PANE_ID = "402"
  try {
    bindRetargetRoute()
    const calls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
    const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, landedContextFor()))
    expect(result.outcome).toBe("ok")
    expect(calls.filter(({ argv }) => argv[0] === terminalRoot)).toEqual([])

    const resumeCalls: RetargetCall[] = []
    adapter.configureConcordAdapter({ runner: resumeRunner(resumeCalls) })
    const resumed: any = await rawHostResult(adapter.work_start.execute({ work_id: "work-1" }, landedContextFor()))
    expect(resumed.outcome).toBe("ok")
    expect(resumeCalls.filter(({ argv }) => argv[0] === terminalRoot)).toEqual([])
  } finally {
    delete process.env.ZELLIJ_PANE_ID
  }
})

// Missing capture fields must name the caller's correction without host effects.
test("work start names the missing capture fields and admits a corrected request", async () => {
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
      expect(result.error.effect_state).toBe("none")
      expect(result.work_id).toBeUndefined()
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
      expect(updates).toBe(supportsPatch ? 1 : 0)
    }
  }
})

test("invalid work start input cannot enroll a host session", async () => {
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
  expect(hostReads).toBe(0)
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
  { name: "both resume selectors", args: { work_id: "work-1", issue_key: "EX-3" }, fragments: ["matches 2 oneOf branches"] },
  { name: "empty issue key", args: { issue_key: "" }, fragments: ["issue_key", "minimum of 3"] },
  { name: "short issue key", args: { issue_key: "A-" }, fragments: ["issue_key", "minimum of 3"] },
  { name: "numeric issue key", args: { issue_key: 3 }, fragments: ["issue_key", "string"] },
  { name: "lowercase issue key", args: { issue_key: "ex-3" }, fragments: ["issue_key", "match"] },
  { name: "padded issue key", args: { issue_key: " EX-3" }, fragments: ["issue_key", "match"] },
  { name: "leading zero issue number", args: { issue_key: "EX-03" }, fragments: ["issue_key", "match"] },
  { name: "zero issue number", args: { issue_key: "EX-0" }, fragments: ["issue_key", "match"] },
  { name: "oversize issue key", args: { issue_key: `E${"X".repeat(62)}-3` }, fragments: ["issue_key", "64"] },
  { name: "null issue key", args: { issue_key: null }, fragments: ["issue_key", "string"] },
  { name: "issue key with Project override", args: { issue_key: "EX-3", project_id: "sensitive-input-value" }, fragments: ["undeclared", "project_id"] },
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
  workStartDiagnosticCases.push({ name: `issue-key resume mixed with ${field}`, args: { issue_key: "EX-3", [field]: "sensitive-input-value" }, fragments: ["undeclared", field] })
}
for (const field of ["constructor", "toString", "__proto__"]) {
  for (const mode of ["capture", "resume"]) {
    const base = mode === "capture" ? bootstrapArgs : { work_id: "work-1" }
    workStartDiagnosticCases.push({ name: `${mode} with ${field}`, args: { ...base, [field]: "sensitive-input-value" }, fragments: ["undeclared", field] })
  }
}
for (const { name, args, fragments } of workStartDiagnosticCases) {
  test(`work start diagnostic: ${name}`, async () => {
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
    expect(result.error.message).toContain("Resume requires exactly one of work_id or issue_key")
    expect(result.error.message).not.toContain("sensitive-input-value")
    expect(result.work_id).toBeUndefined()
    expect(hostWrites).toBe(0)
    expect(coreCalls).toBe(0)
  })
}

test("work start description explains the generated capture and resume requirements", async () => {
  const definition = { description: adapter.work_start.description, parameters: {}, jsonSchema: {} }
  await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, definition)
  const [capture] = hostToolSchemas.concord_work_start.oneOf
  expect(definition.description).toContain(`Capture requires ${capture.required.join(", ")}`)
  expect(definition.description).toContain("Resume requires exactly one of work_id or issue_key")
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
  bindRetargetRoute({ unbound: true })
  const calls: RetargetCall[] = []
  adapter.configureConcordAdapter({ runner: retargetRunner(calls) })
  const result: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("unreachable")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("the host control plane is unreachable")
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
  expect(result.outcome).toBe("error")
  expect(result.error.kind).toBe("unreachable")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toContain("Unable to connect")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
  expect(result.work_id).toBeUndefined()
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
  // The capture and claim stand even though the host did not confirm placement.
  expect(result.error.effect_state).toBe("partial")
  expect(result.error.recovery_action.kind).toBe("retry_same_request")
  expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve", "work-bootstrap", "session-prepare"])
})

// A replay under the same idempotency_key adopts the retained work and claim
// after an interrupted step.
test("work start replays to convergence after an interrupted step", async () => {
  // First attempt: session-prepare fails after bootstrap created the work item.
  bindRetargetRoute()
  const first: RetargetCall[] = []
  adapter.configureConcordAdapter({
    runner: retargetRunner(first, {
      "session-prepare": () => ({ exitCode: 1, stdout: "", stderr: "concord session-prepare: session identity refused" }),
    }),
  })
  const interrupted: any = await rawHostResult(adapter.work_start.execute(bootstrapArgs, contextFor()))
  expect(interrupted.outcome).toBe("error")
  expect(interrupted.error.effect_state).toBe("partial")
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
  expect(result.error.effect_state).toBe("partial")
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
  expect(result.error.effect_state).toBe("partial")
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
  expect(result.error.effect_state).toBe("possible")
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
  expect(result.error.effect_state).toBe("possible")
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
  expect(result.error.effect_state).toBe("partial")
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
      return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory: "/workspace/project", worktree: "/wt" }), stderr: "" }
    } },
  })
  await claimHostLease(4242, { directory: "/workspace/project", worktree: "/wt" })
  expect(claimed).toHaveLength(1)
  expect(claimed[0].argv[1]).toBe("host-lease")
  expect(claimed[0].input.pid).toBe(4242)
  expect(claimed[0].input.directory).toBe("/workspace/project")
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


test("portable continuation posture leaves host protocol names to the host surface", () => {
  for (const hostTerm of [
    "Concord",
    "concord_work_transition",
    "workflow_action",
    "action_id",
    "fields.review",
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
  expect(continuationSource).toContain("Context size alone does not justify a stop, handoff, or restart.")
  expect(continuationSource).toContain("through commits, checkpoints, reports, and logs")
  expect(continuationSource).toContain("read the work's durable records, including any assignment")
  expect(continuationSource).not.toContain("receipt")
  expect(continuationSource).toContain("A summary does not replace durable records or carry authority")
  expect(askingSource).toContain("When a standing rule or approved plan already settles a case, apply it.")
  expect(askingSource).toContain("the decision and its evidence in the existing work record")
  expect(askingSource).toContain("Changing the\noperator's approval boundary always requires approval of its exact wording.")
  expect(askingSource).not.toContain("unsettled case")
  expect(askingSource).toContain("does not waive an approval, safety rule, scope limit, or mandatory")
  expect(askingSource).toContain("Ask the operator directly for the six approval classes")
  expect(askingSource).toContain("Every operator approval question names the applicable class above or the existing")
  expect(askingSource).toContain("operator-only reservation, and the concrete consequence that requires it.")
  expect(askingSource).toContain("reservations include decisions accepted law expressly leaves to")
  expect(askingSource).toContain("Without an applicable class or operator-only reservation, decide within your scope")
  expect(askingSource).toContain("and record the reason instead of asking for discretionary approval.")
  expect(askingSource).toContain("neither an RTE nor a Planner supplies permission or operator approval.")
  expect(askingSource).toContain("Never risk irreversible customer-data loss without informed operator approval.")
  expect(askingSource).toContain("follow admitted recovery routes, never bypass a gate.")
  expect(recordsSource).toContain("Keep standing rules in the files that own")
  expect(recordsSource).toContain("link those files instead of repeating their rules in an assignment")
  expect(recordsSource).toContain("Put the task identity, scope, and stop conditions in the assignment.")
  expect(recordsSource).toContain("prompt names the agent's role and points to that assignment instead of restating")
  expect(recordsSource).not.toContain("carries only the agent's role")
  expect(recordsSource).toContain("Every Linear issue you create or rewrite, and every work item you capture,")
  expect(recordsSource).toContain("the problem, with the evidence you observed")
  expect(recordsSource).toContain("who or what the problem affects;")
  expect(recordsSource).toContain("decide it when the change is delivered.")
})

// The installer repoints `current` beside the pinned releaseRoot. Every
// result exposes staleness while calls keep using the session's pinned pair.

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

test("fresh and stale sessions forward the coordinator's typed Review without launch authorization", async () => {
  const candidate = { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) }
  const review = {
    disposition: "ready_for_final_proof", input_candidate: candidate, output_candidate: candidate,
    dependencies: { criteria: [{ predicate_id: "predicate:one", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true },
    assessments: [], findings: [], remaining_obligations: [],
  }
  for (const current of ["v11.40.3", "v11.40.6"]) {
    await withReleaseLayout(current, async () => {
      let input: any
      const runner = runnerWithContext((_argv: string[], body: string) => {
        input = JSON.parse(body)
        return coreEnvelope("concord_work_transition", "workflow_action", "ok", {
          result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
        })
      })
      adapter.configureConcordAdapter({ runner })
      const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("workflow_action", {
        work_id: "work-1", expected_version: 3, action_id: "record_review", idempotency_key: "record-native-review", fields: { review },
      }), contextFor()))
      expect(result.outcome, JSON.stringify(result)).toBe("ok")
      expect(input.input.fields).toEqual({ review })
      expect(input.input.action_id).toBe("record_review")
      expect(runner.calls()).toBe(2)
      expect(result.warnings).toHaveLength(current === "v11.40.3" ? 0 : 1)
      expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
    })
  }
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

test("a stale session's mutation still runs and carries the notice", async () => {
  await withReleaseLayout("v11.40.6", async () => {
    adapter.configureConcordAdapter({ runner: runnerWithContext(coreEnvelope("concord_work_transition", "lifecycle", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })) })
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

// ===========================================================================
// Semantic tests: the decided closed tuples (design §3 M/V/VO/I) through the
// real producers, plus deterministic contract-defect propagation.
// Every test name in this block carries one shared prefix, so a
// `bun test -t` filter on that prefix selects the block.
const { MoveSessionUnavailable } = await import("./move-session")
const { pendingVacateDestination, recordPendingVacateDestination } = await import("./claimed-worktree")

const expectTuple = (envelope: any, tuple: { kind: string; reason: string; effect: string; recovery: string; retrySafe: boolean }, label = "") => {
  expect(envelope.error.kind, label).toBe(tuple.kind)
  expect(envelope.error.adapter_reason, label).toBe(tuple.reason)
  expect(envelope.error.effect_state, label).toBe(tuple.effect)
  expect(envelope.error.recovery_action.kind, label).toBe(tuple.recovery)
  expect(envelope.error.retry_safe, label).toBe(tuple.retrySafe)
  expect(validateGeneratedEnvelope(envelope), `${label}: ${envelopeFailurePath(envelope)} ${JSON.stringify(envelope)}`).toBe(true)
}
const M = { kind: "operation_conflict", reason: "unknown_effect", effect: "possible", recovery: "reconcile_operation", retrySafe: false }
const V = { kind: "operation_conflict", reason: "unknown_effect", effect: "possible", recovery: "retry_same_request", retrySafe: false }
const VO = { kind: "operation_conflict", reason: "unknown_effect", effect: "possible", recovery: "contact_operator", retrySafe: false }
const I = { kind: "invalid_input", reason: "invalid_tool_input", effect: "none", recovery: "correct_request", retrySafe: false }

const runVacate = async (runner: any) => {
  adapter.configureConcordAdapter({ runner })
  return rawHostResult(adapter.work_transition.execute(hostCall("session_vacate", { idempotency_key: "vacate-concord-913" }), contextFor()))
}

test("CON-913 row 5: a vacate refusal with an unreadable exit keeps the state-driven replay recovery", async () => {
  const result: any = await runVacate(runnerWithContext({ exitCode: 1, stdout: "", stderr: "core died mid-vacate" }))
  expectTuple(result, V, "row 5")
  expect(result.error.message).toContain("replay session_vacate")
})

test("CON-913 row 6: a malformed mutation answer takes the whole conflict tuple, and vacate keeps the replay", async () => {
  const lifecycle: any = await runTransition(runnerWithContext({ exitCode: 0, stdout: "not-json", stderr: "" }))
  expectTuple(lifecycle, M, "row 6 lifecycle")

  const vacate: any = await runVacate(runnerWithContext({ exitCode: 0, stdout: "not-json", stderr: "" }))
  expectTuple(vacate, V, "row 6 vacate")
  expect(vacate.error.message).toContain("replay session_vacate")
})

test("CON-913 row 10: a contract-failing vacate answer keeps the replay tuple with salvaged identity", async () => {
  const drifted = coreEnvelope("concord_work_transition", "session_vacate", "ok", {
    result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" },
    changed_refs: [], next_valid_intents: [], work_id: "work-1", worktree_path: "/worktrees/work-1", unexpected_field: true,
  })
  const result: any = await runVacate(runnerWithContext(drifted))
  expectTuple(result, V, "row 10")
  expect(result.error.message).toContain("replay session_vacate")
  expect(result.error.details.salvaged).toMatchObject({ work_id: "work-1" })
})

test("vacate refusals keep the replay recovery", async () => {
  const vacateRunner = (result: () => { exitCode: number; stdout: string; stderr: string }) => {
    let calls = 0
    return { async run() {
      calls++
      if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return result()
    } }
  }
  adapter.configureConcordAdapter({ runner: vacateRunner(() => ({ exitCode: 0, stdout: "not-json", stderr: "" })) })
  const row11: any = await rawHostResult(adapter.work_transition.execute(
    hostCall("session_vacate", { idempotency_key: "vacate-malformed-913" }),
    contextFor(),
  ))
  expectTuple(row11, V, "row 11")
  expect(row11.error.message).toContain("replay session_vacate")

  adapter.configureConcordAdapter({ runner: vacateRunner(() => ({ exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", {
    result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" },
    changed_refs: [], next_valid_intents: [], unexpected_field: true,
  })), stderr: "" })) })
  const row12: any = await rawHostResult(adapter.work_transition.execute(
    hostCall("session_vacate", { idempotency_key: "vacate-malformed-913-b" }),
    contextFor(),
  ))
  expectTuple(row12, V, "row 12")
})

test("CON-913 rows 31/39: an agent-named vacate destination refuses invalid_input before any effect", async () => {
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no core call may run") } } })
  const refused: any = await rawHostResult(adapter.work_transition.execute(hostCall("session_vacate", { idempotency_key: "vacate-named-1", destination: "/other" }), contextFor()))
  expectTuple(refused, I, "row 39")
  expect(calls).toBe(0)
  const helperArgs = { operation: "session_vacate", input: { idempotency_key: "vacate-named-2", destination: "/other" } } as any
  const preCommit = await adapter.moveSessionToRegisteredMainCheckout(helperArgs, contextFor(), { schema_version: "1.0", outcome: "error", error: { kind: "transport_failure", message: "refused" } } as never)
  expectTuple(preCommit as any, I, "row 31 pre-effect")
})

test("CON-913 row 31: a committed vacate with an agent-named destination reports the replay tuple", async () => {
  const helperArgs = { operation: "session_vacate", input: { idempotency_key: "vacate-named-3", destination: "/other" } } as any
  const committed = await adapter.moveSessionToRegisteredMainCheckout(helperArgs, contextFor(), { schema_version: "1.0", outcome: "ok", result: { work_id: "work-1", destination_directory: "/main" } } as never)
  expectTuple(committed as any, V, "row 31 committed")
})

test("CON-913 rows 32-38: vacate move and landing refusals carry the closed vacate tuples", async () => {
  const vacateArgs = { operation: "session_vacate", input: { idempotency_key: "vacate-move-913" } } as any
  const okEnvelope = (result: Record<string, unknown>) => ({ schema_version: "1.0", outcome: "ok", result }) as never
  const landingRunner = (landingExit = 0) => ({ async run(argv: string[]) {
    if (argv[1] === "vacate-landing") {
      return landingExit === 0
        ? { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
        : { exitCode: 1, stdout: "", stderr: "vacate-landing refused by the core" }
    }
    throw new Error("unexpected CLI invocation: " + argv.join(" "))
  } })
  // The host readback answers a proper RouteResult, so the flows below
  // exercise the intended readback branches rather than a malformed client.
  const bindHost = (move: (() => unknown) | "refuse-409", readback: unknown) => {
    hostControlPlane().bind({
      post: async () => {
        if (typeof move === "function") await (move as () => unknown)()
        return { response: new Response(null, { status: move === "refuse-409" ? 409 : 204 }) }
      },
      get: async () => {
        const value = typeof readback === "function" ? (readback as () => unknown)() : readback
        const shape = value as { response?: unknown }
        return shape && typeof shape === "object" && "response" in shape ? value as { response: Response; data?: unknown } : { data: value, response: new Response(null, { status: 200 }) }
      },
    })
  }
  const runCase = async (name: string, run: () => Promise<any>) => {
    resetClaimedWorktrees()
    resetMoveNotices()
    const envelope = await run()
    expect(envelope.outcome, name).toBe("error")
    return envelope
  }

  // Row 32: the committed answer names no absolute destination.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  const unreadable = await runCase("row 32", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ work_id: "work-1" })))
  expectTuple(unreadable as any, V, "row 32")

  // Row 33: the host move route is unavailable, so replay is unreachable.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(() => { throw new MoveSessionUnavailable("the move route is unavailable") }, { id: "session-1", directory: "/worktree" })
  const unavailable = await runCase("row 33", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ work_id: "work-1", destination_directory: "/main" })))
  expectTuple(unavailable as any, VO, "row 33")
  expect(unavailable.error.message).toContain("the move route is unavailable")

  // Row 34: the host move route refuses the retarget (HTTP 409), which the
  // host client raises as MoveSessionRefused.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost("refuse-409", { id: "session-1", directory: "/worktree" })
  const moveRefused = await runCase("row 34", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ work_id: "work-1", destination_directory: "/main" })))
  expectTuple(moveRefused as any, V, "row 34")

  // Row 35: the host readback fails after the move; the request stands.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(async () => {}, () => { throw new Error("the host readback failed") })
  const readbackFailed = await runCase("row 35", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ work_id: "work-1", destination_directory: "/main" })))
  expectTuple(readbackFailed as any, V, "row 35")

  // Row 37: the verified landing carries no work identity to record.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(async () => {}, { id: "session-1", directory: "/main" })
  const unattributable = await runCase("row 37", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ destination_directory: "/main" })))
  expectTuple(unattributable as any, V, "row 37")

  // Row 38: the vacate-landing verb commits before it answers, so a refused
  // landing reports the possible-effect replay tuple.
  adapter.configureConcordAdapter({ runner: landingRunner(1) as never })
  bindHost(async () => {}, { id: "session-1", directory: "/main" })
  const landingRefused = await runCase("row 38", async () => adapter.moveSessionToRegisteredMainCheckout(vacateArgs, contextFor(), okEnvelope({ work_id: "work-1", destination_directory: "/main" })))
  expectTuple(landingRefused as any, V, "row 38")
})

test("CON-913 rows 40/41: a remembered-destination pre-move failure keeps the request and reports replay or contact", async () => {
  for (const [label, unavailable, tuple] of [
    ["row 41", false, V],
    ["row 40", true, VO],
  ] as const) {
    recordPendingVacateDestination("session-1", "/remembered")
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no core call may run before the pre-move lands") } } })
    hostControlPlane().bind({
      // A route refusal is a non-204 answer (MoveSessionRefused); only an
      // unreachable or absent route raises MoveSessionUnavailable.
      post: async () => ({ response: new Response(null, { status: unavailable ? 404 : 409 }) }),
      get: async () => ({ data: { id: "session-1", directory: "/elsewhere" }, response: new Response(null, { status: 200 }) }),
    })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("session_vacate", { idempotency_key: `vacate-premove-${label}` }), contextFor()))
    expectTuple(result, tuple, label)
    expect(calls, label).toBe(0)
    expect(pendingVacateDestination("session-1"), label).toBe("/remembered")
    resetClaimedWorktrees()
  }
})

test("CON-913 rows 25-30: claim refusals after the durable claim report possible effects and name the replay", async () => {
  const claimArgs = (input: Record<string, unknown>) => ({ operation: "worktree_claim", input }) as never
  const okEnvelope = (result: Record<string, unknown>) => ({ schema_version: "1.0", outcome: "ok", result }) as never
  const landingRunner = (landingExit = 0) => ({ async run(argv: string[]) {
    if (argv[1] === "claim-landing") {
      return landingExit === 0
        ? { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
        : { exitCode: 1, stdout: "", stderr: "claim-landing refused by the core" }
    }
    throw new Error("unexpected CLI invocation: " + argv.join(" "))
  } })
  const bindHost = (move: (() => unknown) | "refuse-409", readback: unknown) => {
    hostControlPlane().bind({
      post: async () => {
        if (typeof move === "function") await (move as () => unknown)()
        return { response: new Response(null, { status: move === "refuse-409" ? 409 : 204 }) }
      },
      get: async () => {
        const value = typeof readback === "function" ? (readback as () => unknown)() : readback
        const shape = value as { response?: unknown }
        return shape && typeof shape === "object" && "response" in shape ? value as { response: Response; data?: unknown } : { data: value, response: new Response(null, { status: 200 }) }
      },
    })
  }
  const expectClaimTuple = (envelope: any, label: string, withIdentity: boolean) => {
    expectTuple(envelope, M, label)
    expect(envelope.error.details?.reconciled, label).toBeUndefined()
    expect(envelope.error.details?.reconcile_attempted, label).toBeUndefined()
    if (withIdentity) expect(JSON.stringify(envelope.error), `${label} keeps the claim identity`).toContain("work-1")
  }
  const runCase = async (name: string, run: () => Promise<any>) => {
    resetClaimedWorktrees()
    resetMoveNotices()
    const envelope = await run()
    expect(envelope.outcome, name).toBe("error")
    return envelope
  }

  // Row 25: the core answer names no absolute derived destination.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  const unreadable = await runCase("row 25", async () => adapter.moveSessionToClaimedWorktree(claimArgs({ work_id: "work-1" }), contextFor(), okEnvelope({})))
  expectClaimTuple(unreadable as any, "row 25", true)

  // Row 26: the host move route refuses the retarget; the claim stays
  // durable and the replay is the typed remedy.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost("refuse-409", { id: "session-1", directory: "/worktree" })
  const moveRefused = await runCase("row 26", async () => adapter.moveSessionToClaimedWorktree(claimArgs({ work_id: "work-1" }), contextFor(), okEnvelope({ path: "/claimed" })))
  expectClaimTuple(moveRefused as any, "row 26", true)
  expect(moveRefused.error.message).toContain("worktree_claim")

  // Row 27: the move was accepted but the landing readback fails; the
  // pending target is recorded for the replay.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(async () => {}, () => { throw new Error("the host readback failed") })
  const readbackFailed = await runCase("row 27", async () => adapter.moveSessionToClaimedWorktree(claimArgs({ work_id: "work-1" }), contextFor(), okEnvelope({ path: "/claimed" })))
  expectClaimTuple(readbackFailed as any, "row 27", true)
  expect(unlandedClaimedWorktree("session-1")).toBe("/claimed")

  // Row 28: the host landed the session elsewhere; no mismatch kind.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(async () => {}, { id: "session-1", directory: "/elsewhere" })
  const mismatch = await runCase("row 28", async () => adapter.moveSessionToClaimedWorktree(claimArgs({ work_id: "work-1" }), contextFor(), okEnvelope({ path: "/claimed" })))
  expectClaimTuple(mismatch as any, "row 28", true)
  expect(unlandedClaimedWorktree("session-1")).toBe("/claimed")

  // Row 29: the verified landing carries no work identity to record.
  adapter.configureConcordAdapter({ runner: landingRunner() as never })
  bindHost(async () => {}, { id: "session-1", directory: "/claimed" })
  const unattributable = await runCase("row 29", async () => adapter.moveSessionToClaimedWorktree(claimArgs({}), contextFor(), okEnvelope({ path: "/claimed" })))
  expectClaimTuple(unattributable as any, "row 29", false)

  // Row 30: the core refuses the landing record; the diagnostic never
  // asserts that nothing was recorded, because the verb commits first.
  adapter.configureConcordAdapter({ runner: landingRunner(1) as never })
  bindHost(async () => {}, { id: "session-1", directory: "/claimed" })
  const landingRefused = await runCase("row 30", async () => adapter.moveSessionToClaimedWorktree(claimArgs({ work_id: "work-1" }), contextFor(), okEnvelope({ path: "/claimed" })))
  expectClaimTuple(landingRefused as any, "row 30", true)
  expect(landingRefused.error.message).not.toContain("is not recorded")
})

test("CON-913 row 26 through the tool: a refused claim move reports a possible effect with no reconcile readback", async () => {
  hostControlPlane().bind({
    post: async () => ({ response: new Response(null, { status: 409 }) }),
    get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
  })
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    calls++
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worktree_claim", "ok", { result: { changed_refs: [], next_valid_intents: [], path: "/claimed" }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
    throw new Error("unexpected CLI invocation: " + argv.join(" "))
  } } })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worktree_claim", { work_id: "work-1", project_id: "project-1", base_sha: "a".repeat(40), expected_version: 1, idempotency_key: "claim-concord-913" }), contextFor()))
  expectTuple(result, M, "row 26 via execute")
  expect(calls).toBe(2)
  expect(result.error.details?.reconciled).toBeUndefined()
  expect(result.error.details?.reconcile_attempted).toBeUndefined()
  expect(result.error.message).toContain("worktree_claim")
})



test("CON-913 rows 13/14: an oversized mutation result takes the whole classifier tuple and reconciles", async () => {
  const oversized = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [],
    evidence_refs: Array.from({ length: 32 }, (_, index) => ({
      kind: "artifact", authority: "core", locator_kind: "id", locator: `${index}-${"x".repeat(2040)}`,
    })),
  })
  const readback = coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })
  let calls = 0
  const result: any = await runTransition({ async run() {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    return { exitCode: 0, stdout: JSON.stringify(calls === 2 ? oversized : readback), stderr: "" }
  } })
  expectTuple(result, M, "rows 13/14")
  expect(calls).toBe(4)
  expect(result.error.details.reconciled).toEqual({ found: false, lifecycle: null, version: null })
})

// Contract-defect propagation (R1.7): a real typed defect — the exact object
// a forged factory key throws — is injected into a configured seam, and the
// same object must escape execute, never serialized, never relabeled. The
// Host session reads wrap foreign errors; the configured core runner carries
// the adapter's typed defects directly.
const contractDefect = (): Error => {
  try {
    adapter.adapterError("concord_work_browse", "list", "request-concord-913", "no_such_key" as never, "forged through a cast")
  } catch (error) {
    return error as Error
  }
  throw new Error("the factory accepted a forged key")
}

test("CON-913 defect propagation: a defect thrown by the reconcile readback runner escapes unserialized", async () => {
  const defect = contractDefect()
  const committed = coreEnvelope("concord_work_transition", "lifecycle", "ok", {
    result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [], work_id: "work-1", unexpected_field: true,
  })
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() {
    calls++
    if (calls === 1 || calls === 3) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(committed), stderr: "" }
    throw defect
  } } })
  let caught: unknown = null
  try {
    await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "defect-readback-913" }), contextFor()))
  } catch (error) {
    caught = error
  }
  expect(caught).toBe(defect)
})

test("CON-913 defect propagation: a defect thrown by the session_vacate invoke escapes unserialized", async () => {
  const defect = contractDefect()
  const sessionID = "session-defect-own-row-913"
  hostControlPlane().bind({
    post: async () => ({ response: new Response(null, { status: 204 }) }),
    get: async ({ path }: any) => ({ data: { id: path?.id, directory: process.cwd() }, response: new Response("{}") }),
  })
  adapter.configureConcordAdapter({
    runner: { async run(argv: string[], input: string) {
      if (argv[1] === "invoke" && (JSON.parse(input) as { operation: string }).operation === "session_vacate") throw defect
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    } },
  })
  let caught: unknown = null
  try {
    await rawHostResult(adapter.work_transition.execute(hostCall("session_vacate", {
      idempotency_key: "defect-vacate-913",
    }), { ...landedContextFor(), sessionID }))
  } catch (error) {
    caught = error
  }
  expect(caught).toBe(defect)
})

test("CON-913 defect propagation: a defect thrown by the work_start bootstrap child run escapes unserialized", async () => {
  const defect = contractDefect()
  bindRetargetRoute()
  adapter.configureConcordAdapter({ runner: { async run(argv: string[]) {
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    throw defect
  } } })
  let caught: unknown = null
  try {
    await adapter.work_start.execute(bootstrapArgs, contextFor())
  } catch (error) {
    caught = error
  }
  expect(caught).toBe(defect)
})
