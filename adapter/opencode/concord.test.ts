import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { chmod, mkdir, mkdtemp as createDirectory, rm } from "node:fs/promises"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, manifestDigest, maxEnvelopeBytes, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { configureCoreBinary } from "./core-runtime"
import { claimHostLease, configureHostLease, hostLeaseFault } from "./host-lease"
import { validateGeneratedEnvelope, envelopeFailurePath, expandedPublishedRequestSchema, actionFieldsBranches } from "./generated-contract-tests"
import { sessionDirectoryReader, type RouteResult } from "./session-directory"
import { adoptManifestDigest, resetManifestPinForTesting } from "./manifest-pin"
import { mutationProjectionFixture } from "./mutation-projection.fixture"
import { fakeTool } from "./plugin-tool.fixture"
const testDirectories: string[] = []
async function mkdtemp(prefix: string): Promise<string> {
  const directory = await createDirectory(prefix)
  testDirectories.push(directory)
  return directory
}


mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const source = await Bun.file(new URL("./concord.ts", import.meta.url)).text()
const askingSource = await Bun.file(new URL("../../.concord/instructions/asking.md", import.meta.url)).text()
const continuationSource = await Bun.file(new URL("../../.concord/instructions/continuation.md", import.meta.url)).text()
const recordsSource = await Bun.file(new URL("../../.concord/instructions/records.md", import.meta.url)).text()
// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

const adapter = await import("./concord")

const outerSelectedProductID = process.env.CONCORD_SELECTED_PRODUCT_ID
beforeEach(() => {
  sessionDirectoryReader().bind({
    get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
  })
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
})
afterEach(async () => {
  if (outerSelectedProductID === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
  else process.env.CONCORD_SELECTED_PRODUCT_ID = outerSelectedProductID
  await Promise.all(testDirectories.splice(0).map(directory => rm(directory, { recursive: true, force: true })))
})
const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })

test("exports exactly the generated tool names", () => {
  const names = [...source.matchAll(/export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(/g)].map((match) => match[1])
  expect(names).toEqual(["product_view", "work_browse", "work_trace", "knowledge", "work_define", "domain", "work_transition", "work_relate", "work_compact"])
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
  // `request` property.
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
  sessionDirectoryReader().bind({
    get: async () => ({ data: { id: "session-1", directory: "/moved" }, response: new Response(null, { status: 200 }) }),
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
  sessionDirectoryReader().bind({
    get: async () => ({ data: { id: "session-1", metadata: {} }, response: new Response(null, { status: 200 }) }),
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
  sessionDirectoryReader().bind({
    get: async ({ path }) => {
      expect(path?.id).toBe("session-1")
      return { data: { id: "session-1", directory: "/worktree", parentID: "native-parent" }, response: new Response(null, { status: 200 }) }
    },
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
  sessionDirectoryReader().bind({
    get: async () => ({ data: null, response: new Response(null, { status: 404 }) }),
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

test("an unbound directory reader refuses a concord call closed", async () => {
  sessionDirectoryReader().bind(undefined)
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
  test(`host mutation privately reconstructs projected pins; tampered=${tampered}`, async () => {
    const fixture = mutationProjectionFixture()
    if (tampered) fixture.pages[1].result.offset++
    sessionDirectoryReader().bind({
      get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
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
      expect(reads).toBe(2)
      expect(result.warnings.some((notice: any) => notice.kind === "mutation_result_unavailable")).toBe(true)
    } else {
      expect(reads).toBe(fixture.pages.length)
    }
  })
}

for (const capacity of ["room", "warnings-full", "bytes-full"] as const) {
  test(`mutation-result read failure keeps the receipt and uses only fitting warnings: ${capacity}`, async () => {
    const fixture = mutationProjectionFixture()
    fixture.pages[0].result.offset++
    const core = coreEnvelope("concord_work_relate", "message_send", "ok", {
      ...fixture.envelope,
      warnings: [...fixture.envelope.warnings, ...(capacity === "warnings-full" ? Array.from({ length: 15 }, () => ({ kind: "core_note" })) : [])],
      evidence_refs: [] as Array<{ kind: string; authority: string; locator_kind: string; locator: string }>,
    })
    if (capacity === "bytes-full") {
      core.evidence_refs = Array.from({ length: 32 }, () => ({ kind: "artifact", authority: "core", locator_kind: "id", locator: "" }))
      const remaining = maxEnvelopeBytes - Buffer.byteLength(JSON.stringify(core)) - 1
      for (const ref of core.evidence_refs) ref.locator = "x".repeat(Math.floor(remaining / 32))
      expect(maxEnvelopeBytes - Buffer.byteLength(JSON.stringify(core))).toBeLessThan(40)
    }
    expect(validateGeneratedEnvelope(core), envelopeFailurePath(core) ?? "valid receipt").toBe(true)
    let writes = 0
    let reads = 0
    adapter.configureConcordAdapter({ runner: { async run(argv, input) {
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      const request = JSON.parse(input)
      if (request.operation === "message_send") {
        writes++
        return { exitCode: 0, stdout: JSON.stringify(core), stderr: "" }
      }
      expect([request.tool, request.operation]).toEqual(["concord_work_trace", "mutation_result"])
      reads++
      return { exitCode: 0, stdout: JSON.stringify(coreEnvelope(request.tool, request.operation, "ok", fixture.pages[0])), stderr: "" }
    } } })
    const raw = await adapter.work_relate.execute(hostCall("message_send", { idempotency_key: "projection-key" }), contextFor())
    const output = typeof raw === "string" ? raw : raw.output
    const result = JSON.parse(output)
    expect(writes).toBe(1)
    expect(reads).toBe(1)
    expect(result.result).toEqual(core.result)
    expect(result.changed_refs).toEqual(core.changed_refs)
    expect(result.next_valid_intents).toEqual([])
    expect(validateGeneratedEnvelope(result)).toBe(true)
    expect(Buffer.byteLength(output)).toBeLessThanOrEqual(maxEnvelopeBytes)
    if (capacity === "room") expect(result.warnings).toMatchObject([...fixture.envelope.warnings, { kind: "mutation_result_unavailable", source_id: "adapter" }])
    else expect(result).toEqual(core)
    expect(Reflect.get(adapter, "takeWorkNotices")).toBeUndefined()
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
      sessionDirectoryReader().bind({
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

test("a mutation error preserves committed refs through the adapter boundary", async () => {
  const response = coreEnvelope("concord_work_transition", "lifecycle", "error", {
    changed_refs: [{ entity_kind: "work_item", id: "work-2", version: "5" }],
    error: { kind: "budget_refused", retry_safe: false, recovery_action: { kind: "adjust_budget" }, effect_state: "possible", supported_budget_seconds: 300 },
  })
  expect(validateGeneratedEnvelope(response)).toBe(true)
  adapter.configureConcordAdapter({ runner: runnerWithContext(response) })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", {
    work_id: "work-2", expected_version: 4, target: "completed", reason: "done", idempotency_key: "mutation-refs-1",
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

// CD-0111 D1: the core path is the stamped release constant, never a PATH
// lookup. The transport must contain no ambient `concord` resolution, and an
// unstamped adapter copy must refuse with missing_binary instead of spawning.
test("core_binary_is_the_stamped_release_constant", async () => {
  expect(source).toContain('runner.run([concordBinaryPath(), "invoke"]')
  expect(source).toContain('runner.run([concordBinaryPath(), "project-resolve"]')
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

test("tools forward the coordinator's typed Review without launch authorization", async () => {
  const candidate = { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) }
  const review = {
    disposition: "ready_for_final_proof", input_candidate: candidate, output_candidate: candidate,
    dependencies: { criteria: [{ predicate_id: "predicate:one", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true },
    assessments: [], findings: [], remaining_obligations: [],
  }
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
  expect(result.warnings).toEqual([])
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
})

// ===========================================================================
// Semantic tests: the decided closed mutation tuple through the
// real producers, plus deterministic contract-defect propagation.
// Every test name in this block carries one shared prefix, so a
// `bun test -t` filter on that prefix selects the block.

const expectTuple = (envelope: any, tuple: { kind: string; reason: string; effect: string; recovery: string; retrySafe: boolean }, label = "") => {
  expect(envelope.error.kind, label).toBe(tuple.kind)
  expect(envelope.error.adapter_reason, label).toBe(tuple.reason)
  expect(envelope.error.effect_state, label).toBe(tuple.effect)
  expect(envelope.error.recovery_action.kind, label).toBe(tuple.recovery)
  expect(envelope.error.retry_safe, label).toBe(tuple.retrySafe)
  expect(validateGeneratedEnvelope(envelope), `${label}: ${envelopeFailurePath(envelope)} ${JSON.stringify(envelope)}`).toBe(true)
}
const M = { kind: "operation_conflict", reason: "unknown_effect", effect: "possible", recovery: "reconcile_operation", retrySafe: false }

test("CON-913 row 6: a malformed mutation answer takes the whole conflict tuple", async () => {
  const lifecycle: any = await runTransition(runnerWithContext({ exitCode: 0, stdout: "not-json", stderr: "" }))
  expectTuple(lifecycle, M, "row 6 lifecycle")
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

test("CON-913 defect propagation: a defect thrown by a mutation invoke escapes unserialized", async () => {
  const defect = contractDefect()
  adapter.configureConcordAdapter({
    runner: { async run(argv: string[]) {
      if (argv[1] === "invoke") throw defect
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    } },
  })
  let caught: unknown = null
  try {
    await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "defect-invoke-913" }), contextFor()))
  } catch (error) {
    caught = error
  }
  expect(caught).toBe(defect)
})
