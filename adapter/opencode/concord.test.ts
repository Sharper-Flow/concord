import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { chmod, mkdir, mkdtemp as createDirectory, rm, symlink } from "node:fs/promises"
import { createHash, createPublicKey, verify as verifySignature } from "node:crypto"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { contractOperations, hostToolSchemas, manifestDigest, maxEnvelopeBytes, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { configureCoreBinary, workerFailureObservationDigest } from "./dispatch"
import { dispatchWindows, staleReleaseDispatchRefusal, TASK_TOOL_ID } from "./dispatch-window"
import { claimHostLease, configureHostLease, hostLeaseFault, releaseDisplayName, releaseStaleness, resolveInstalledReleaseRoot } from "./host-lease"
import { armedClaimedWorktree, clearClaimedWorktree, resetClaimedWorktrees, unlandedClaimedWorktree } from "./claimed-worktree"
import { validateGeneratedEnvelope, envelopeFailurePath, expandedPublishedRequestSchema, actionFieldsBranches } from "./generated-contract-tests"
import { hostControlPlane, SESSION_LIST_ROUTE, SESSION_MESSAGES_ROUTE, SESSION_ROUTE, type RouteResult } from "./move-session"
import { adoptManifestDigest, resetManifestPinForTesting } from "./manifest-pin"
import { resetMoveNotices, takeMoveNotice } from "./move-notice"
import { armTurnMoveBoundary, clearTurnMoveBoundary, dispatchRequiresNextTurn } from "./turn-move-boundary"
import { syntheticHostVersionFixture } from "./host-version.test-support"
import { privateKeyObject } from "./credentials"
import { canonicalWorkerControl } from "./worker_control"
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
const credentialSource = await Bun.file(new URL("./credentials.ts", import.meta.url)).text()
const askingSource = await Bun.file(new URL("../../.concord/instructions/asking.md", import.meta.url)).text()
const continuationSource = await Bun.file(new URL("../../.concord/instructions/continuation.md", import.meta.url)).text()
const recordsSource = await Bun.file(new URL("../../.concord/instructions/records.md", import.meta.url)).text()
// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

const adapter = await import("./concord")

test("published dispatch description separates authorization from native Task launch", () => {
  expect(adapter.work_transition.description).toContain("dispatch itself launches nothing")
  expect(adapter.work_transition.description).toContain("issue one native foreground Task")
  expect(adapter.work_transition.description).toContain("exact lane and attempt reference")
  expect(adapter.work_transition.description).toContain("without a packet, resume, background or command fields")
})

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

test("the worker reader transport uses only the held request and dispatched directory", async () => {
  const request: Parameters<typeof adapter.readBoundWorkerSource>[0] = {
    work_id: "read-work", attempt_id: "read-attempt", worktree_identity: "bound-identity",
    packet: {
      schema_version: "1.0", work_id: "read-work", attempt_id: "read-attempt", step_id: "verify",
      lane_id: "verify", lane_digest: "bound-lane",
      inputs: { task: "Read the declared source", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "verification_commands" } },
    },
    ordinal: 0, offset: 0, length: 6,
  }
  const calls: unknown[] = []
  configureHostLease({ reset: true })
  adapter.configureConcordAdapter({ runner: { run: async (argv, input, signal, options) => {
    calls.push({ argv, request: JSON.parse(input), signal, options })
    return { exitCode: 0, stdout: '{"data":"c291cmNl"}\n', stderr: "" }
  } } })
  const signal = new AbortController().signal
  try {
    expect(await adapter.readBoundWorkerSource(request, signal, "/dispatched-worktree")).toEqual({ data: "c291cmNl" })
    expect(calls).toEqual([{ argv: ["concord", "worker-read"], request, signal, options: { cwd: "/dispatched-worktree" } }])
    const { ordinal: _ordinal, ...diffRequest } = request
    expect(await adapter.readBoundWorkerDiff(diffRequest, signal, "/dispatched-worktree")).toEqual({ data: "c291cmNl" })
    expect(calls[1]).toEqual({ argv: ["concord", "worker-diff"], request: diffRequest, signal, options: { cwd: "/dispatched-worktree" } })
  } finally {
    adapter.configureConcordAdapter({ reset: true })
  }
})

for (const [label, read] of [["source", adapter.readBoundWorkerSource], ["diff", adapter.readBoundWorkerDiff]] as const) {
  for (const [scenario, failure, message] of [
    ["empty-stderr refusal", { exitCode: 1, stdout: "", stderr: "" }, `the core refused the pinned ${label} read`],
    ["oversized page", { exitCode: 0, stdout: "x".repeat(16_385), stderr: "" }, `pinned ${label} page exceeded its transport bound`],
  ] as const) {
    test(`the ${label} reader transport names its operation on ${scenario}`, async () => {
      configureHostLease({ reset: true })
      let result = { exitCode: 0, stdout: "{}\n", stderr: "" }
      adapter.configureConcordAdapter({ runner: { run: async () => result } })
      try {
        // The transport forwards the held request; these cases never enter the store.
        expect(await read({} as never, new AbortController().signal, "/dispatched-worktree")).toEqual({})
        result = failure
        await expect(read({} as never, new AbortController().signal, "/dispatched-worktree")).rejects.toThrow(message)
      } finally {
        adapter.configureConcordAdapter({ reset: true })
        configureHostLease({ reset: true })
      }
    })
  }

  test(`the ${label} reader transport names its operation when the host lease refuses`, async () => {
    configureHostLease({ reset: true })
    let calls = 0
    adapter.configureConcordAdapter({ runner: { run: async () => { calls++; return { exitCode: 0, stdout: "{}\n", stderr: "" } } } })
    try {
      expect(await read({} as never, new AbortController().signal, "/dispatched-worktree")).toEqual({})
      configureHostLease({ release: { coreBinary: "concord", releaseRoot: "/release" }, runner: { run: async () => ({ exitCode: 1, stdout: "", stderr: "fixture lease refusal" }) } })
      await claimHostLease(process.pid)
      expect(hostLeaseFault()).toBe("host lease claim failed with exit 1: fixture lease refusal")
      await expect(read({} as never, new AbortController().signal, "/dispatched-worktree")).rejects.toThrow(`${hostLeaseFault()}; no ${label} read ran`)
      expect(calls).toBe(1)
    } finally {
      adapter.configureConcordAdapter({ reset: true })
      configureHostLease({ reset: true })
    }
  })
}

test("the diff reader transport preserves the core's typed refusal and recovery verbatim", async () => {
  configureHostLease({ reset: true })
  const diagnostic = "concord worker-diff: store: worker_diff: unauthorized_dispatch: worker diff read requires an active workflow contract\nrecovery: use a live review with its original packet, claimed worktree, scoped job, and complete first-editing subject history"
  adapter.configureConcordAdapter({ runner: { run: async () => ({ exitCode: 1, stdout: "", stderr: diagnostic + "\n" }) } })
  let caught: unknown
  try {
    try { await adapter.readBoundWorkerDiff({} as never, new AbortController().signal, "/dispatched-worktree") } catch (error) { caught = error }
    expect(caught).toBeInstanceOf(Error)
    expect((caught as Error).message).toBe(diagnostic)
  } finally {
    adapter.configureConcordAdapter({ reset: true })
    configureHostLease({ reset: true })
  }
})

test("the worker reader transport refuses failed, oversized, or non-single-object output", async () => {
  configureHostLease({ reset: true })
  for (const result of [
    { exitCode: 1, stdout: '{"data":"forbidden"}', stderr: "source registration changed" },
    { exitCode: 0, stdout: '{"data":"' + "x".repeat(16_384) + '"}', stderr: "" },
    { exitCode: 0, stdout: '{}\n{}\n', stderr: "" },
    { exitCode: 0, stdout: '[]\n', stderr: "" },
  ]) {
    adapter.configureConcordAdapter({ runner: { run: async () => result } })
    try {
      // This transport test never enters the store or authenticates a packet.
      await expect(adapter.readBoundWorkerSource({} as Parameters<typeof adapter.readBoundWorkerSource>[0], new AbortController().signal, "/dispatched-worktree")).rejects.toThrow()
      await expect(adapter.readBoundWorkerDiff({} as Parameters<typeof adapter.readBoundWorkerDiff>[0], new AbortController().signal, "/dispatched-worktree")).rejects.toThrow()
    } finally {
      adapter.configureConcordAdapter({ reset: true })
    }
  }
})

function workerControlTransportFixture() {
  const context = { sessionID: "actual-child", messageID: "actual-message", agent: "concord-verify", directory: "/dispatched-worktree", abort: new AbortController().signal, ask: async () => {} }
  const request: Parameters<typeof adapter.executeBoundWorkerControl>[0] = { work_id: "check-work", project_id: "check-project", attempt_id: "check-attempt", worktree_identity: "sha256:" + "b".repeat(64), control_id: "control:check", packet: { schema_version: "2.0", work_id: "check-work", attempt_id: "check-attempt", step_id: "verify", lane_id: "verify", lane_digest: "sha256:" + "c".repeat(64), inputs: { task: "Check", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "commands" } } } }
  const digest = "sha256:" + "a".repeat(64)
  const ambient = { project_id: request.project_id, product_ids: ["check-product"], scope_version: "scope-check", main_worktree: false }
  return { context, request, digest, ambient }
}

test("the worker check transport signs the actual child and invokes only the private native ingress", async () => {
  const f = workerControlTransportFixture()
  const calls: Array<{ argv: string[]; input: any; options: unknown }> = []
  const seed = new Uint8Array(32).fill(7)
  configureHostLease({ reset: true })
  adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => seed }, runner: { run: async (argv, input, signal, options) => {
    expect(signal).toBe(f.context.abort)
    const body = JSON.parse(input)
    calls.push({ argv, input: body, options })
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" }
    return { exitCode: 0, stderr: "", stdout: JSON.stringify({ work_id: f.request.work_id, project_id: f.request.project_id, lease_id: body.assertion.lease_id, operation_ref: "native/retained", oracle: { phase: "execute", attempt_id: f.request.attempt_id, control_id: f.request.control_id, worker_packet_digest: f.digest, qualification: "fail" } }) }
  } } })
  try {
    const result: any = await adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)
    expect(result.oracle.qualification).toBe("fail")
    expect(calls.map((call) => call.argv)).toEqual([["concord", "project-resolve"], ["concord", "worker-control"]])
    expect(calls[1].options).toEqual({ cwd: f.context.directory })
    const { assertion, ...request } = calls[1].input
    expect(request).toEqual(f.request)
    expect(assertion).toMatchObject({ agent_ref: f.context.agent, session_ref: f.context.sessionID, directory: f.context.directory, worktree: f.context.directory, product_id: "check-product", project_id: f.request.project_id, control_id: f.request.control_id, packet_digest: f.digest, request_id: "actual-child-actual-message" })
    expect(assertion.nonce).toMatch(/^[a-f0-9]{64}$/)
    expect(assertion.model).toBeUndefined()
    expect(verifySignature(null, canonicalWorkerControl(assertion), createPublicKey(privateKeyObject(seed)), Buffer.from(assertion.signature, "base64"))).toBe(true)
    expect(verifySignature(null, canonicalWorkerControl({ ...assertion, agent_ref: "coordinator" }), createPublicKey(privateKeyObject(seed)), Buffer.from(assertion.signature, "base64"))).toBe(false)
  } finally { adapter.configureConcordAdapter({ reset: true }) }
})

test("worker check cancellation during credential lookup never reaches native execution", async () => {
  const f = workerControlTransportFixture()
  const abort = new AbortController()
  f.context.abort = abort.signal
  const calls: string[][] = []
  configureHostLease({ reset: true })
  adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => { abort.abort(new Error("child canceled")); return new Uint8Array(32).fill(7) } }, runner: { run: async (argv) => { calls.push(argv); return { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" } } } })
  try {
    await expect(adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)).rejects.toThrow("child canceled")
    expect(calls).toEqual([["concord", "project-resolve"]])
  } finally { adapter.configureConcordAdapter({ reset: true }) }
})

test("worker native transport never retries a refusal or a mismatched producer response", async () => {
  for (const refused of [true, false]) {
    const f = workerControlTransportFixture()
    let calls = 0
    configureHostLease({ reset: true })
    adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => new Uint8Array(32).fill(7) }, runner: { run: async (argv) => {
      calls++
      return argv[1] === "project-resolve" ? { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" } : { exitCode: refused ? 1 : 0, stdout: refused ? "" : '{"work_id":"foreign"}', stderr: refused ? "execution policy refused" : "" }
    } } })
    try {
      await expect(adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)).rejects.toThrow("reconcile native lease")
      expect(calls).toBe(2)
    } finally { adapter.configureConcordAdapter({ reset: true }) }
  }
})

test("worker check retains a bound unavailable execution and its released lease without retry", async () => {
  const f = workerControlTransportFixture()
  let calls = 0
  configureHostLease({ reset: true })
  adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => new Uint8Array(32).fill(7) }, runner: { run: async (argv, input) => {
    calls++
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" }
    const lease = JSON.parse(input).assertion.lease_id
    return { exitCode: 1, stderr: "candidate inputs changed", stdout: JSON.stringify({ work_id: f.request.work_id, project_id: f.request.project_id, lease_id: lease, operation_ref: `worktree_verify:${lease}`, outcome: "refused", diagnostic: "candidate inputs changed", lease_disposition: { state: "released", outcome: "completed" }, oracle: { phase: "execute", attempt_id: f.request.attempt_id, control_id: f.request.control_id, worker_packet_digest: f.digest, qualification: "unavailable", stages: [{ name: "test", exit_code: 0 }], stdout: { ref: "retained-stdout" } } }) }
  } } })
  try {
    const response: any = await adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)
    expect(response.outcome).toBe("refused")
    expect(response.oracle.qualification).toBe("unavailable")
    expect(response.oracle.stages).toEqual([{ name: "test", exit_code: 0 }])
    expect(response.oracle.stdout.ref).toBe("retained-stdout")
    expect(response.lease_disposition).toEqual({ state: "released", outcome: "completed" })
    expect(response.operation_ref).toBe(`worktree_verify:${response.lease_id}`)
    expect(calls).toBe(2)
  } finally { adapter.configureConcordAdapter({ reset: true }) }
})

test("worker check refuses forged acceptance or disposition on a nonzero native response", async () => {
  for (const alter of [
    (r: any) => { r.oracle.qualification = "pass" },
    (r: any) => { r.outcome = "ok" },
    (r: any) => { r.operation_ref = "foreign-run" },
    (r: any) => { r.lease_disposition.state = "held" },
    (r: any) => { r.lease_disposition.outcome = "running" },
    (r: any) => { delete r.lease_disposition },
    (r: any) => { delete r.diagnostic },
  ]) {
    const f = workerControlTransportFixture()
    let calls = 0
    configureHostLease({ reset: true })
    adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => new Uint8Array(32).fill(7) }, runner: { run: async (argv, input) => {
      calls++
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" }
      const lease = JSON.parse(input).assertion.lease_id
      const response = { work_id: f.request.work_id, project_id: f.request.project_id, lease_id: lease, operation_ref: `worktree_verify:${lease}`, outcome: "refused", diagnostic: "candidate inputs changed", lease_disposition: { state: "released", outcome: "completed" }, oracle: { phase: "execute", attempt_id: f.request.attempt_id, control_id: f.request.control_id, worker_packet_digest: f.digest, qualification: "unavailable" } }
      alter(response)
      return { exitCode: 1, stderr: "input refusal", stdout: JSON.stringify(response) }
    } } })
    try {
      await expect(adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)).rejects.toThrow("reconcile native lease")
      expect(calls).toBe(2)
    } finally { adapter.configureConcordAdapter({ reset: true }) }
  }
})

test("worker check preserves recovery context for malformed or oversized nonzero producer stdout", async () => {
  for (const stdout of ["{partial", '{"work_id":"foreign"}', "x".repeat(maxEnvelopeBytes + 1)]) {
    const f = workerControlTransportFixture()
    configureHostLease({ reset: true })
    adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => new Uint8Array(32).fill(7) }, runner: { run: async (argv) => argv[1] === "project-resolve" ? { exitCode: 0, stdout: JSON.stringify(f.ambient), stderr: "" } : { exitCode: 1, stdout, stderr: "native producer refused" } } })
    try {
      const attempt = adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)
      await expect(attempt).rejects.toThrow("native producer refused")
      await expect(attempt).rejects.toThrow("reconcile native lease")
    } finally { adapter.configureConcordAdapter({ reset: true }) }
  }
})

test("worker check does not infer an ingress refusal from a zero-exit malformed response", async () => {
  const f = workerControlTransportFixture()
  configureHostLease({ reset: true })
  adapter.configureConcordAdapter({ credentials: { getPrivateKey: async () => new Uint8Array(32).fill(7) }, runner: { run: async (argv) => ({ exitCode: 0, stdout: argv[1] === "project-resolve" ? JSON.stringify(f.ambient) : "{partial", stderr: "" }) } })
  try {
    const attempt = adapter.executeBoundWorkerControl(f.request, f.context, f.digest, f.context.directory)
    await expect(attempt).rejects.toThrow("native control result unavailable; reconcile native lease")
  } finally { adapter.configureConcordAdapter({ reset: true }) }
})

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
  // fields is one closed union: the legacy shape and the consequence_v1
  // extension that requires decision_domains. Every inspection below reads
  // each branch, never the factored parent view where a branch-only field
  // is invisible.
  const fieldsBranches = actionFieldsBranches(actionBranch)
  expect(fieldsBranches, "approve_contract fields union branches").toHaveLength(2)
  const legacyFields = fieldsBranches.find((fields: any) => !fields.required?.includes("decision_domains"))
  const extensionFields = fieldsBranches.find((fields: any) => fields.required?.includes("decision_domains"))
  expect(legacyFields, "the legacy fields branch").toBeObject()
  expect(extensionFields, "the consequence_v1 fields branch").toBeObject()
  expect(legacyFields.properties.decision_domains, "the legacy branch stays closed to the extension fields").toBeUndefined()
  expect(extensionFields.properties.decision_domains, "the extension branch declares decision_domains").toBeObject()
  // CON-412: the items close per kind; each branch binds outcome_kind to
  // outcome_payload.kind and carries that branch's payload inline.
  for (const fields of fieldsBranches) {
    const itemVariants = fields.properties.outcome_predicates.items.oneOf
    expect(itemVariants.map((branch: any) => branch.properties.outcome_kind.const)).toEqual(["exists", "absent", "outcome", "check"])
    expect(itemVariants.map((branch: any) => branch.properties.outcome_payload.properties.kind.const)).toEqual(["exists", "absent", "outcome", "check"])
  }
  // The branch names the conditional action fields, so a calling agent can
  // read them before calling.
  for (const field of ["action_id", "selected_choice", "decision_context_digest"]) {
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

// CD-0017 D4, extended by CD-0223. A session with a managed parent is a
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

test("approval host-round-trip asks and resubmits the exact resolve challenge once", async () => {
  const input = {
    from_work_id: "work-a",
    to_work_id: "work-b",
    from_expected_version: 3,
    to_expected_version: 3,
    from_contract_version: 1,
    to_contract_version: 1,
    resolution_kind: "supersedes",
    reason: "replaced by the resolved item",
    idempotency_key: "resolve-adapter-1",
  }
  const digest = `sha256:${"d".repeat(64)}`
  const challenge = coreEnvelope("concord_work_relate", "resolve_overlap", "error", {
    error: {
      kind: "approval_required", retry_safe: false,
      recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: {
        tool: "concord_work_relate", operation: "resolve_overlap", consequence: "relation",
        operation_digest: digest,
        scope: ["product_id:product-a", "product_ids:product-a", "product_ids:product-b", "project_ids:ambient", "work_ids:work-a", "work_ids:work-b", "scope_version:1"],
        versions: ["from:3", "to:3"], expires_at: "2026-09-14T12:10:00Z",
      },
      details: {
        approval_ref: "e".repeat(64), operation_digest: digest,
        summary: "resolve the overlap by superseding work-b with work-a",
        resolution_kind: "supersedes", from_work_id: "work-a", to_work_id: "work-b",
      },
    },
  })
  const success = coreEnvelope("concord_work_relate", "resolve_overlap", "ok", {
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
  const result: any = await rawHostResult(adapter.work_relate.execute(hostCall("resolve_overlap", input), contextFor(async () => { approvals++ })))
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
    packet_schema_version: "1.0", report_schema_version: "1.0",
    dispatch: {
      attempt_id: "attempt-1", lane_id: lane.id,
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
    expect(result.error.kind, JSON.stringify(result)).toBe("operation_conflict")
    expect(result.error.adapter_reason).toBe("unknown_effect")
    expect(result.error.recovery_action.kind).toBe("reconcile_operation")
    expect(readStarted).toBe(true)
    expect(result.error.effect_state).toBe("possible")
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
  const conflicted: any = await runTransition({ async run(_argv: string[]) { calls++; if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }; if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(approvalChallenge()), stderr: "" }; return { exitCode: 0, stdout: "not-json", stderr: "" } } })
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
  // The refused re-read arms no dispatch and opens no handoff consume.
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
      expect(result.error.effect_state).toBe("none")
      expect(result.work_id).toBeUndefined()
      expect(calls.map(({ argv }) => argv[1])).toEqual(["project-resolve"])
      expect(updates).toBe(supportsPatch ? 1 : 0)
    }
  }
})

test("invalid work start input cannot enroll a host session", async () => {
  // The lane guard's caller check (CD-0223) is the one permitted host read;
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
    // The lane guard's caller check (CD-0223) is the one permitted host read;
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
      "session-prepare": () => ({ exitCode: 1, stdout: "", stderr: "concord session-prepare: lane identity refused" }),
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

// The abandonment observation records the coordinator's request the host
// observed: the abandonment tag, the declared operation as the event type,
// no child session (the retained dispatch record holds none and the
// coordinator session never poses as one), the exact detail as the observed
// message, the full detail plus the authenticated call identity in the
// digest, and no diagnosis.
test("worker_abandon records the abandonment observation the host observed", async () => {
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
  const detail = "the coordinator closed the attempt with no worker report"
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail, idempotency_key: "worker-abandon-observation-1" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(result.outcome).toBe("ok")
  const request = calls[0].input
  const token = createHash("sha256").update("concord_work_transition.worker_abandon\0worker-abandon-observation-1").digest("hex")
  const host = { tag: "abandonment", event_type: "worker_abandon" }
  expect(request.failure_attribution).toEqual({
    observer: "host_adapter",
    phase: "host_execution",
    cause: "unknown",
    basis: "unknown",
    sources: [{
      kind: "host_event",
      digest: "sha256:" + createHash("sha256").update(JSON.stringify({ host, message: detail, identity: { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", event_id: `worker-abandon-${token}` } })).digest("hex"),
      message: detail,
      host,
    }],
  })
  expect(request.failure_attribution.sources[0].host.session_id).toBeUndefined()
  expect(JSON.stringify(request.failure_attribution)).not.toContain("session-1")
  expect(request.failure_attribution.diagnosis).toBeUndefined()
  // The signed assertion binds the observation digest (v2 assertion).
  expect(request.assertion.version).toBe(2)
  expect(typeof request.assertion.observation_digest).toBe("string")
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
  expect(pending.error.recovery_action.kind).toBe("reconcile_operation")
  expect(pending.error.message).toContain("durable_commit")
  expect(pending.error.message).not.toContain("remains open")
  // The retry reaches a CLI exit 0, so the recorded envelope flows through
  // the receipt replay and the abandonment settles.
  abandonExit = 0
  const settled: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(settled.outcome).toBe("ok")
})

// A typed durable refusal binds its effect_state: none proves nothing was
// written and possible proves an uncertain effect. Neither fact reaches the
// receipt replay, and neither answer may claim the attempt was closed — the
// close reporting follows the committed effect only.
test.each(["none", "possible"] as const)("a typed worker_abandon refusal with a %s effect reports the refusal and claims no closure", async (effectState) => {
  const refusalMessage = "store: worker_fail: liveness gate refused: the worker attempt cannot close while its host process is live"
  const calls: Array<{ argv: string[]; input: any }> = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      calls.push({ argv, input: JSON.parse(input) })
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [], error: { kind: "operation_conflict", operation: "worker_evidence", retry_safe: false, effect_state: effectState, message: refusalMessage } }), stderr: refusalMessage }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "error", { error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "receipt replay refused" } })), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: `worker-abandon-typed-refusal-${effectState}` }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(result.outcome).toBe("error")
  expect(result.error.adapter_reason).toBe("worker_abandon_refused")
  expect(result.error.effect_state).toBe(effectState)
  expect(result.error.recovery_action.kind).toBe("reconcile_operation")
  expect(result.error.message).not.toContain("was closed")
  if (effectState === "none") {
    expect(result.error.message).toBe(refusalMessage)
  } else {
    expect(result.error.message).toContain(refusalMessage)
    expect(result.error.message).toContain("reconcile the original worker_abandon attempt and its receipt")
  }
  // The receipt replay never runs behind a refusal: no closure claim can form.
  expect(calls.filter(({ argv }) => argv[1] === "invoke")).toHaveLength(0)
  expect(calls.filter(({ argv }) => argv[1] === "worker-abandon")).toHaveLength(1)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
})

// The own-row recovery's abandon retry owes the same committed-effect
// reporting: a typed retry refusal with a none effect is a refusal that
// leaves the attempt open, never a closed attempt whose receipt failed.
test("an own-row recovery reports a refused retry without claiming closure", async () => {
  const MAIN_CHECKOUT = "/data/repo-main"
  const sessionID = "session-abandon-retry-refusal"
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
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live`
  const retryRefusalMessage = "store: worker_fail: projection_not_found: the named attempt is not open in this store"
  const calls: Array<{ argv: string[]; input: string }> = []
  let abandonCalls = 0
  const windows = dispatchWindows()
  windows.open(sessionID, {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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
          : { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [], error: { kind: "operation_conflict", operation: "worker_evidence", retry_safe: false, effect_state: "none", message: retryRefusalMessage } }), stderr: retryRefusalMessage }
      }
      if (command === "invoke") {
        const operation = (JSON.parse(input) as { operation: string }).operation
        if (operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: sessionDirectory, destination_directory: MAIN_CHECKOUT }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
        if (operation === "worker_abandon") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "error", { error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "receipt replay refused" } })), stderr: "" }
      }
      if (command === "vacate-landing") return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (command === "work-resume") return { exitCode: 1, stdout: "", stderr: "work resume refused" }
      throw new Error(`unexpected command ${argv.join(" ")}`)
    } },
  })
  const context = { ...landedContextFor(), sessionID }
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "worker-abandon-retry-refusal-1" }
  const toolResult = (await adapter.work_transition.execute(hostCall("worker_abandon", input), context)) as { output: string }
  const result: any = envelopeLine(toolResult.output)
  expect(result.outcome).toBe("error")
  expect(result.error.adapter_reason).toBe("worker_abandon_refused")
  expect(result.error.effect_state).toBe("none")
  expect(result.error.message).toBe(retryRefusalMessage)
  // No text anywhere in the composed answer claims the attempt was closed.
  expect(toolResult.output).not.toContain("was closed")
  expect(result.error.details.recovery_stopped_at).toBe("worker_abandon_retry")
  expect(result.error.details.abandon_error_message).toBe(ownRowRefusal)
  expect(result.error.details.recovery_error_message).toBe(retryRefusalMessage)
  expect(result.error.details.recovery_steps).toEqual([
    "session_vacate: the session moved to the registered main checkout and the verified landing released its occupancy rows",
    expect.stringContaining("work_start re-land refused: work resume refused; replay work_start once the session's tool context runs in the claimed worktree"),
  ])
  // The receipt replay never runs behind a refusal.
  expect(calls.filter(({ argv, input }) => argv[1] === "invoke" && JSON.parse(input).operation === "worker_abandon")).toHaveLength(0)
  // Nothing committed, so the retained in-flight record stays until its named
  // release route runs; a refused retry releases nothing.
  expect(windows.has(sessionID)).toBe(true)
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
  expect(moves).toEqual([MAIN_CHECKOUT])
  // The retry carries the same derived event identity the refused first
  // write never committed.
  const abandonInputs = calls.filter(({ argv }) => argv[1] === "worker-abandon").map(({ input }) => JSON.parse(input))
  expect(abandonInputs).toHaveLength(2)
  expect(abandonInputs[0].event_id).toBe(abandonInputs[1].event_id)
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
    lane_digest: "sha256:" + "a".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "c".repeat(64), process.cwd())
  if (consumed) {
    await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "concord-implement", prompt: "attempt-1", description: "bounded worker" }, "call-cancel", async () => process.cwd(), process.cwd())
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

// A missing-row refusal is not an acknowledged close. Its diagnostic cannot
// grant the adapter authority to release the retained attempt or invent a receipt.
test.each(["none", "possible", "unknown"] as const)("a missing-row worker_abandon refusal keeps its retained record (effect=%s)", async (effectState) => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  // The suite shares one window registry across files, so this test owns a
  // session identity no other test touches.
  const context = { ...contextFor(), sessionID: `session-abandon-missing-row-${effectState}` }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-stranded",
    lane_id: "implement",
    lane_digest: "sha256:" + "b".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "d".repeat(64), process.cwd())
  await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "concord-implement", prompt: "attempt-stranded", description: "bounded worker" }, "call-stranded", async () => process.cwd(), process.cwd())
  expect(windows.inFlight(context.sessionID, "call-stranded")).not.toBeNull()
  const calls: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      calls.push(argv[1])
      if (argv[1] === "worker-abandon") {
        const message = "store: worker_attempt_read: projection_not_found: worker dispatch row does not exist"
        return { exitCode: 1, stdout: effectState === "unknown" ? "" : JSON.stringify({ ok: false, event_ids: null, error: { kind: "projection_not_found", operation: "worker_attempt_read", retry_safe: false, effect_state: effectState, message } }), stderr: message }
      }
      throw new Error(`no receipt may run after a refused abandonment: ${argv[1]}`)
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-stranded", lane_id: "implement", detail: "the host Task call was interrupted before the worker started", idempotency_key: "worker-abandon-nothing-durable-1" }
  const refused: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(refused.outcome).toBe("error")
  expect(refused.origin).toBe("adapter")
  expect(refused.authority).toBe("unreachable")
  expect(refused.error.adapter_reason).toBe("worker_abandon_refused")
  expect(refused.error.effect_state).toBe(effectState === "none" ? "none" : "possible")
  expect(refused.result).toBeUndefined()
  expect(validateGeneratedEnvelope(refused), JSON.stringify(refused)).toBe(true)
  expect(calls).toEqual(["worker-abandon"])
  expect(windows.inFlight(context.sessionID, "call-stranded")).not.toBeNull()
  expect(() => windows.open(context.sessionID, retained, "", process.cwd())).toThrow()
  expect(windows.releaseRetained(context.sessionID, retained.attempt_id, retained.lane_id)).toBe(true)
})

test("a missing-row worker_abandon refusal preserves a foreign retained record", async () => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  const context = { ...contextFor(), sessionID: "session-abandon-nothing-durable-foreign" }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-stranded",
    lane_id: "implement",
    lane_digest: "sha256:" + "b".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(context.sessionID, retained, "sha256:" + "e".repeat(64), process.cwd())
  await windows.bind(TASK_TOOL_ID, context.sessionID, { subagent_type: "concord-implement", prompt: "attempt-stranded", description: "bounded worker" }, "call-foreign", async () => process.cwd(), process.cwd())
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: "concord worker-abandon: store: worker_attempt_read: projection_not_found: worker dispatch row does not exist" }
      throw new Error(`unexpected core call: ${argv[1]}`)
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-elsewhere", lane_id: "implement", detail: "a foreign attempt identity", idempotency_key: "worker-abandon-nothing-durable-foreign" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(result.outcome).toBe("error")
  expect(result.origin).toBe("adapter")
  expect(result.authority).toBe("unreachable")
  expect(result.error.adapter_reason).toBe("worker_abandon_refused")
  expect(result.error.effect_state).toBe("possible")
  expect(result.result).toBeUndefined()
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  expect(windows.inFlight(context.sessionID, "call-foreign")).not.toBeNull()
  expect(windows.releaseRetained(context.sessionID, "attempt-stranded", "implement")).toBe(true)
})

// A committed abandon's envelope carries the coordinator's own detail as its
// diagnostic (the close route composes it from the abandon request), so a
// detail that quotes a refusal shape must not reroute the close. The explicit
// committed fact runs the receipt; only a refusal reaches own-row recovery.
test.each([
  ["nothing-durable", "concord worker-abandon: store: worker_attempt_read: projection_not_found: worker dispatch row does not exist"],
  ["own-row", "concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session session-abandon-committed-detail still holds the worker attempt worktree; its host process 4242 is still live"],
])("a committed worker_abandon whose detail quotes a %s refusal shape records its receipt", async (_name, detail) => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const windows = dispatchWindows()
  const sessionID = "session-abandon-committed-detail"
  const context = { ...contextFor(), sessionID }
  const retained = {
    schema_version: "1.0" as const,
    attempt_id: "attempt-1",
    lane_id: "implement",
    lane_digest: "sha256:" + "a".repeat(64),
    work_id: "work-1",
    step_id: "repair",
    inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }
  windows.open(sessionID, retained, "sha256:" + "b".repeat(64), process.cwd())
  await windows.bind(TASK_TOOL_ID, sessionID, { subagent_type: "concord-implement", prompt: "attempt-1", description: "bounded worker" }, "call-committed-detail", async () => process.cwd(), process.cwd())
  expect(windows.inFlight(sessionID, "call-committed-detail")).not.toBeNull()
  const calls: Array<{ argv: string[]; operation?: string }> = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      const operation = argv[1] === "invoke" ? (JSON.parse(input) as { operation: string }).operation : undefined
      calls.push({ argv, operation })
      if (argv[1] === "worker-abandon") return { exitCode: 0, stdout: "", stderr: "" }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke" && operation === "worker_abandon") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      throw new Error(`a committed close may run only its receipt: ${argv.join(" ")}`)
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail, idempotency_key: "worker-abandon-committed-detail-1" }
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), context))
  expect(result.outcome).toBe("ok")
  // The receipt is the durable replay answer, never the composed
  // nothing-durable release the detail's vocabulary could invite.
  expect(result.result.nothing_durable_to_abandon).toBeUndefined()
  // One close write, one receipt, and no recovery route: no session_vacate.
  expect(calls.filter(({ argv }) => argv[1] === "worker-abandon")).toHaveLength(1)
  expect(calls.filter(({ argv, operation }) => argv[1] === "invoke" && operation === "worker_abandon")).toHaveLength(1)
  expect(calls.filter(({ operation }) => operation === "session_vacate")).toHaveLength(0)
  // The committed close released the retained record, so the session
  // dispatches again without a host restart.
  expect(windows.has(sessionID)).toBe(false)
  expect(windows.inFlight(sessionID, "call-committed-detail")).toBeNull()
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
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
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
    lane_digest: "sha256:" + "a".repeat(64), work_id: "work-1", step_id: "repair",
    inputs: { task: "the bounded task", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" }, constraints: [] },
  }, "sha256:" + "b".repeat(64), process.cwd())
  let abandonCalls = 0
  const abandonInputs: any[] = []
  const receiptDirectories: string[] = []
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv, input) {
      const request = JSON.parse(input)
      switch (argv[1]) {
        case "worker-abandon":
          abandonCalls++
          abandonInputs.push(request)
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
  // The refused first write and the retry record the same abandonment
  // observation: the host observed one request, so the retry re-records it
  // unchanged — same event identity, same observation — and the v2
  // assertion binds the same observation digest.
  expect(abandonInputs).toHaveLength(2)
  expect(abandonInputs[0].event_id).toBe(abandonInputs[1].event_id)
  expect(abandonInputs[0].failure_attribution).toEqual(abandonInputs[1].failure_attribution)
  const attribution = abandonInputs[0].failure_attribution
  expect(attribution.observer).toBe("host_adapter")
  expect(attribution.phase).toBe("host_execution")
  expect(attribution.cause).toBe("unknown")
  expect(attribution.basis).toBe("unknown")
  expect(attribution.sources).toHaveLength(1)
  expect(attribution.sources[0].kind).toBe("host_event")
  expect(attribution.sources[0].host).toEqual({ tag: "abandonment", event_type: "worker_abandon" })
  expect(attribution.sources[0].host.session_id).toBeUndefined()
  expect(attribution.sources[0].message).toBe("the host never started the worker")
  expect(attribution.diagnosis).toBeUndefined()
  expect(abandonInputs[0].assertion.version).toBe(2)
  expect(abandonInputs[0].assertion.observation_digest).toBe(abandonInputs[1].assertion.observation_digest)
  expect(abandonInputs[0].assertion.observation_digest).toBe(workerFailureObservationDigest(attribution))
  expect(validateGeneratedEnvelope(result), `${envelopeFailurePath(result)}: ${JSON.stringify(result)}`).toBe(true)
})

test.each([false, true])("a terminal abandon releases only its authorization when the receipt fails (foreign=%s)", async (foreign) => {
  bindSessionRoutes({ sessions: [{ id: "ses_other", directory: "/elsewhere" }] })
  const sessionID = `session-abandon-receipt-failure-${foreign}`
  const windows = dispatchWindows()
  windows.open(sessionID, {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: "implement",
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
  expect(askingSource).toContain("reservations include gate waivers and decisions accepted law expressly leaves to")
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

// ===========================================================================
// Semantic tests: the decided closed tuples (design §3 M/V/VO/I) through the
// real producers, plus deterministic contract-defect propagation.
// Every test name in this block carries one shared prefix, so a
// `bun test -t` filter on that prefix selects the block.
const { MoveSessionUnavailable } = await import("./move-session")
const { pendingVacateDestination, recordPendingVacateDestination } = await import("./claimed-worktree")
const { serializeLanePacket } = await import("./dispatch-window")

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

test("CON-913 rows 11/12: post-approval vacate refusals keep the replay recovery", async () => {
  const digest = "sha256:" + "a".repeat(64)
  const challenge = coreEnvelope("concord_work_transition", "session_vacate", "error", {
    error: {
      kind: "approval_required", retry_safe: false, recovery_action: { kind: "request_approval" }, effect_state: "none",
      consequence_summary: {
        tool: "concord_work_transition", operation: "session_vacate", consequence: "session_vacate",
        operation_digest: digest, scope: ["product_id:product-1", "project_ids:project-1"], versions: ["work:2"], expires_at: "2026-12-01T00:00:00Z",
      },
      details: { approval_ref: "challenge-vacate-913", operation_digest: digest },
    },
  })
  expect(validateGeneratedEnvelope(challenge), envelopeFailurePath(challenge) ?? undefined).toBe(true)
  const resubmissionRunner = (resubmission: () => { exitCode: number; stdout: string; stderr: string }) => {
    let calls = 0
    return { async run() {
      calls++
      if (calls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (calls === 2) return { exitCode: 0, stdout: JSON.stringify(challenge), stderr: "" }
      return resubmission()
    } }
  }
  let approvals = 0
  adapter.configureConcordAdapter({ runner: resubmissionRunner(() => ({ exitCode: 0, stdout: "not-json", stderr: "" })) })
  const row11: any = await rawHostResult(adapter.work_transition.execute(
    hostCall("session_vacate", { idempotency_key: "vacate-approval-913" }),
    contextFor(async () => { approvals++ }),
  ))
  expect(approvals).toBe(1)
  expectTuple(row11, V, "row 11")
  expect(row11.error.message).toContain("replay session_vacate")

  approvals = 0
  adapter.configureConcordAdapter({ runner: resubmissionRunner(() => ({ exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", "ok", {
    result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" },
    changed_refs: [], next_valid_intents: [], unexpected_field: true,
  })), stderr: "" })) })
  const row12: any = await rawHostResult(adapter.work_transition.execute(
    hostCall("session_vacate", { idempotency_key: "vacate-approval-913-b" }),
    contextFor(async () => { approvals++ }),
  ))
  expect(approvals).toBe(1)
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

test("CON-913 row 21: a lease-less worker_abandon refuses transport and closes no attempt", async () => {
  configureHostLease({ reset: true })
  try {
    await claimHostLease(process.pid)
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no core call may run without a lease") } } })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", {
      work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "abandon-lease-913",
    }), contextFor()))
    expectTuple(result, { kind: "transport_failure", reason: "host_lease_missing", effect: "none", recovery: "contact_operator", retrySafe: true }, "row 21")
    expect(calls).toBe(0)
  } finally {
    configureHostLease({ reset: true })
  }
})

test("pre-effect generated-input correction names worker_abandon detail and bound", async () => {
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "x".repeat(257), idempotency_key: "abandon-input-correction" }
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no effect before input correction") } } })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expect(result.error).toMatchObject({ kind: "invalid_input", effect_state: "none", retry_safe: false, recovery_action: { kind: "correct_request" } })
  expect(result.error.message).toContain("detail")
  expect(result.error.message).toContain("256")
  expect(result.error.message).toContain("maxLength")
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  expect(calls).toBe(0)
  for (const error of [{ effect_state: "possible" }, { effect_state: "partial" }, { kind: "approval_required" }, { kind: "unauthorized" }, { retry_safe: true }]) {
    expect(validateGeneratedEnvelope({ ...result, error: { ...result.error, ...error } })).toBe(false)
  }
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      calls++
      if (argv[1] === "worker-abandon") return { exitCode: 0, stdout: "", stderr: "" }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      throw new Error(`unexpected call ${argv}`)
    } },
  })
  const corrected: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", { ...input, detail: "The worker session ended." }), contextFor()))
  expect(corrected.outcome).toBe("ok")
  expect(calls).toBeGreaterThan(0)
})

test("CON-913 rows 22/23: worker_abandon input refusals derive correct-request invalid_input", async () => {
  const cases: Array<[string, Record<string, unknown>]> = [
    ["row 22 invalid payload", { work_id: "work-1", lane_id: "implement", detail: "missing attempt identity" }],
    ["row 23 unknown lane", { work_id: "work-1", attempt_id: "attempt-1", lane_id: "no-such-lane-concord-913", detail: "the worker session ended", idempotency_key: "abandon-lane-913" }],
  ]
  for (const [label, input] of cases) {
    let calls = 0
    adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no dispatch may run for a refused input") } } })
    const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
    expectTuple(result, I, label)
    expect(calls, label).toBe(0)
  }
})

test("CON-913 row 24: an uncertain durable abandonment reconciles the original attempt instead of retrying", async () => {
  let abandonExit = 1
  const abandonStderr = "store: durable_commit: unavailable: synthetic commit IO error"
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv) {
      if (argv[1] === "worker-abandon") return abandonExit === 1
        ? { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: ["abandon-913"], error: { kind: "unavailable", operation: "durable_commit", retry_safe: true, effect_state: "possible", message: abandonStderr } }), stderr: abandonStderr }
        : { exitCode: 0, stdout: "", stderr: "" }
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worker_abandon", "ok", { result: { changed_refs: [], next_valid_intents: [] }, changed_refs: [], next_valid_intents: [] })), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  })
  const input = { work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "abandon-uncertain-913" }
  const pending: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", input), contextFor()))
  expectTuple(pending, { kind: "operation_conflict", reason: "worker_abandon_refused", effect: "possible", recovery: "reconcile_operation", retrySafe: false }, "row 24")
  expect(pending.error.message).toContain("worker_abandon")
  expect(pending.error.message).toContain("durable_commit")
})

test("CON-913 row 15: worker_reconcile input refusals derive correct-request invalid_input", async () => {
  let calls = 0
  adapter.configureConcordAdapter({ runner: { async run() { calls++; throw new Error("no core call may run for a refused input") } } })
  const result: any = await rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", {
    work_id: "work-1", attempt_id: "attempt-1",
  }), contextFor()))
  expectTuple(result, I, "row 15")
  expect(calls).toBe(0)
})

// Retained-recovery fixtures: one completed host Task carrying the original
// packet and report, served through the bound host routes, with the evidence
// verbs answered by the adapter runner seam.
const recoveryLane = ((await import("./generated-agent-lanes")).agentLanes as readonly any[]).find((lane) => lane.id === "verify")!
const RECOVERY_SESSION = "parent-concord-913"
const recoveryPacket: Parameters<typeof serializeLanePacket>[0] = {
  schema_version: "1.0", work_id: "work-concord-913", attempt_id: "attempt-concord-913",
  lane_id: recoveryLane.id, lane_digest: recoveryLane.digest, step_id: "repair",
  inputs: { task: "Verify a retained synthetic report.", binding: { objective_source: "contract_premise", work_version: 7, contract_version: 1, assigned_result: "exit_codes" } },
}
const recoveryReport = {
  schema_version: "1.0", readback_model: "openai/test-model", status: "completed",
  evidence: ((recoveryLane.evidence_obligations ?? []) as readonly any[]).map((obligation) => ({ obligation, detail: "synthetic result" })),
}
const recoveryWrap = `<task id="child-concord-913" state="completed">\n<task_result>\n${JSON.stringify(recoveryReport)}\n</task_result>\n</task>`
const recoveryContextFixture = {
  packet_digest: "sha256:" + "a".repeat(64), worker_worktree: process.cwd(), coordinator_session: RECOVERY_SESSION,
  attempt_epoch: 23, dispatch_event_id: "historical-concord-913", lifecycle_state: "dispatched",
  packet_schema_version: "1.0", report_schema_version: "1.0",
  dispatch: {
    attempt_id: recoveryPacket.attempt_id, lane_id: recoveryLane.id, lane_digest: recoveryLane.digest,
    capability_class: recoveryLane.capability_class, packet_schema_version: "1.0", report_schema_version: "1.0",
    packet_digest: "sha256:" + "a".repeat(64), readback_model: "openai/test-model",
    host_provenance: { digest: "sha256:" + "b".repeat(64), sources: [{ kind: "agent_definition", path: "original-agent.md", sha256: "sha256:" + "c".repeat(64) }] },
  },
}
const recoveryTranscriptHost = () => {
  const parent = { info: { id: RECOVERY_SESSION, directory: process.cwd() }, messages: [{ info: { id: "parent-message-913", sessionID: RECOVERY_SESSION, role: "assistant" }, parts: [{ id: "retained-part-concord-913", sessionID: RECOVERY_SESSION, type: "tool", tool: "task", state: { status: "completed", input: { subagent_type: `concord-${recoveryLane.id}`, prompt: serializeLanePacket(recoveryPacket) }, output: recoveryWrap } }] }] }
  const child = { info: { id: "child-concord-913", parentID: RECOVERY_SESSION, directory: process.cwd() }, messages: [
    { info: { id: "opening-913", sessionID: "child-concord-913", role: "user", time: { created: 1 } }, parts: [{ type: "text", text: serializeLanePacket(recoveryPacket) }] },
    { info: { id: "report-913", sessionID: "child-concord-913", role: "assistant", agent: `concord-${recoveryLane.id}`, providerID: "openai", modelID: "test-model", time: { created: 2 } }, parts: [{ type: "text", text: JSON.stringify(recoveryReport) }] },
  ] }
  hostControlPlane().bind({
    get: async ({ url, path }: any) => {
      const id = (path as { id?: string })?.id
      if (url === SESSION_MESSAGES_ROUTE) return { data: id === RECOVERY_SESSION ? parent.messages : child.messages, response: new Response("[]") }
      const info = id === RECOVERY_SESSION ? parent.info : id === "child-concord-913" ? child.info : { id, directory: process.cwd() }
      return { data: info, response: new Response("{}") }
    },
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
}
const runReconcile913 = async (runner: any, input?: Record<string, unknown>, controller?: AbortController) => {
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner,
  })
  return rawHostResult(adapter.work_transition.execute(hostCall("worker_reconcile", input ?? {
    work_id: recoveryPacket.work_id, attempt_id: recoveryPacket.attempt_id, task_part_id: "retained-part-concord-913", idempotency_key: "reconcile-concord-913",
  }), contextFor(() => {}, controller)))
}

test("CON-913 row 18: a stopped recovery composite reports an uncertain effect with its cause", async () => {
  const ghost = JSON.parse(JSON.stringify(recoveryContextFixture))
  ghost.dispatch.lane_id = "ghost-lane"
  const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: ghost }, changed_refs: [], next_valid_intents: [],
  })
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: process.cwd() }, response: new Response("{}") }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  const result: any = await runReconcile913({ async run(argv: string[]) {
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" }
    throw new Error("no evidence may run past a stopped composite: " + argv.join(" "))
  } })
  expectTuple(result, M, "row 18")
  expect(result.error.message).toContain("the original dispatched lane requires content revalidation")
})

test("CON-913 row 17: a caller deadline during the recovery composite keeps the conflict tuple and the cause", async () => {
  const controller = new AbortController()
  const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: recoveryContextFixture }, changed_refs: [], next_valid_intents: [],
  })
  hostControlPlane().bind({
    get: async ({ signal }: any) => {
      if (signal?.aborted) throw signal.reason
      return { data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }
    },
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
  const result: any = await runReconcile913({ async run(argv: string[]) {
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "invoke") {
      controller.abort(Object.assign(new DOMException("deadline hit", "TimeoutError")))
      return { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" }
    }
    throw new Error("no evidence may run past a caller deadline: " + argv.join(" "))
  } }, { work_id: recoveryPacket.work_id, attempt_id: recoveryPacket.attempt_id, task_part_id: "retained-part-concord-913", idempotency_key: "reconcile-deadline-913" }, controller)
  expectTuple(result, M, "row 17")
  expect(result.error.message).toContain("deadline hit")
  expect(result.error.message).not.toContain("before evidence")
})

test("CON-913 row 19: a stopped recovery maps every lane outcome to the reconcile tuple", async () => {
  const laneRefusals = [
    ["possible effect, lane retryable", { kind: "unavailable", operation: "durable_commit", effect_state: "possible", retry_safe: true, message: "synthetic uncertain commit" }],
    ["possible effect, lane contact", { kind: "readback_refusal", operation: "worker_dispatch", effect_state: "possible", retry_safe: false, message: "synthetic refused dispatch" }],
    ["no effect, lane retryable", { kind: "unavailable", operation: "durable_commit", effect_state: "none", retry_safe: true, message: "synthetic uncertain commit" }],
    ["no effect, lane contact", { kind: "readback_refusal", operation: "worker_dispatch", effect_state: "none", retry_safe: false, message: "synthetic refused dispatch" }],
  ] as const
  for (const [label, laneError] of laneRefusals) {
    recoveryTranscriptHost()
    let evidenceRuns = 0
    const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
      result: { changed_refs: [], next_valid_intents: [], worker_recovery: recoveryContextFixture }, changed_refs: [], next_valid_intents: [],
    })
    const result: any = await runReconcile913({ async run(argv: string[], input: string) {
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") return { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" }
      if (argv[1] === "worker-dispatch") {
        evidenceRuns++
        const request = JSON.parse(input)
        return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [request.event_id], error: laneError }), stderr: laneError.message }
      }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    } })
    expectTuple(result, M, `row 19 ${label}`)
    expect(evidenceRuns, `row 19 ${label}`).toBe(1)
  }
})

test.each([
  { name: "dynamic missing evidence", kind: "missing_evidence", effect_state: "none", retry_safe: false },
  { name: "possible report refusal", kind: "invalid_payload", effect_state: "possible", retry_safe: false },
  { name: "retry-safe report refusal", kind: "invalid_payload", effect_state: "none", retry_safe: true },
  { name: "unknown completion effect", kind: "invalid_payload", effect_state: "unknown", retry_safe: false },
])("worker_reconcile retains aggregate possible without closing $name", async ({ name, kind, effect_state, retry_safe }) => {
  recoveryTranscriptHost()
  const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: recoveryContextFixture }, changed_refs: [], next_valid_intents: [],
  })
  const calls: string[] = []
  const result: any = await runReconcile913({ async run(argv: string[], input: string) {
    const request = JSON.parse(input)
    calls.push(argv[1])
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "invoke" && request.operation === "worker_reconcile") return { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" }
    if (argv[1] === "worker-dispatch") return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
    if (argv[1] === "worker-complete") return { exitCode: 1, stdout: effect_state === "unknown" ? "" : JSON.stringify({ ok: false, event_ids: [], error: { kind, operation: "worker_oracle_findings", effect_state, retry_safe, message: name } }), stderr: name }
    throw new Error("no failure close, replacement worker or completed receipt is allowed: " + argv.join(" "))
  } })
  expect(result.outcome).toBe("error")
  expect(result.error).toMatchObject({ kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible", retry_safe: false, recovery_action: { kind: "reconcile_operation" } })
  expect(result.error.details?.lifecycle_state).toBeUndefined()
  expect(result.error.message).toContain(name)
  expect(result.result).toBeUndefined()
  expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
  expect(calls.filter(command => command.startsWith("worker-"))).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls.filter(command => command === "invoke")).toHaveLength(1)
})

test.each(["recorded", "foreign", "none", "possible", "unknown"] as const)("worker_reconcile distinguishes a recorded failure from an uncertain close (%s)", async (closeState) => {
  recoveryTranscriptHost()
  const recorded = closeState === "recorded" || closeState === "foreign"
  const retained = { ...recoveryPacket, attempt_id: closeState === "foreign" ? "attempt-foreign" : recoveryPacket.attempt_id }
  const windows = dispatchWindows()
  windows.open(RECOVERY_SESSION, retained, recoveryContextFixture.packet_digest, process.cwd())
  await windows.bind(TASK_TOOL_ID, RECOVERY_SESSION, { subagent_type: `concord-${recoveryPacket.lane_id}`, prompt: retained.attempt_id, description: "bounded worker" }, "retained-part-concord-913", async () => process.cwd(), process.cwd())
  const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: recoveryContextFixture }, changed_refs: [], next_valid_intents: [],
  })
  const calls: Array<{ command: string; input: any }> = []
  const refusal = { kind: "invalid_payload", operation: "worker_oracle_findings", retry_safe: false, effect_state: "none", message: "synthetic fixed oracle contradiction" }
  try {
    const result: any = await runReconcile913({ async run(argv: string[], input: string) {
      const request = JSON.parse(input)
      calls.push({ command: argv[1], input: request })
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      if (argv[1] === "invoke") {
        if (request.operation === "worker_reconcile") return { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" }
        throw new Error("a recorded failure must not request a completed receipt or dispatch another worker")
      }
      if (argv[1] === "worker-dispatch") return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
      if (argv[1] === "worker-complete") return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [], error: refusal }), stderr: refusal.message }
      if (argv[1] === "worker-fail") {
        if (recorded) return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
        const error = { ...refusal, operation: "worker_fail", effect_state: closeState, message: "synthetic failure-close refusal" }
        return { exitCode: 1, stdout: closeState === "unknown" ? "" : JSON.stringify({ ok: false, event_ids: [], error }), stderr: error.message }
      }
      throw new Error(`unexpected CLI invocation: ${argv.join(" ")}`)
    } })
    expect(result.outcome).toBe("error")
    expect(result.origin).toBe("adapter")
    expect(result.authority).toBe("unreachable")
    expect(result.error.kind).toBe("operation_conflict")
    expect(result.error.adapter_reason).toBe(recorded ? "worker_reconcile_failed_recorded" : "unknown_effect")
    expect(result.error.effect_state).toBe("possible")
    expect(result.error.recovery_action.kind).toBe("reconcile_operation")
    expect(result.error.retry_safe).toBe(false)
    if (recorded) {
      expect(result.error.details).toEqual({ work_id: recoveryPacket.work_id, attempt_id: recoveryPacket.attempt_id, lifecycle_state: "failed" })
      expect(result.error.message).toContain(refusal.message)
    } else {
      expect(result.error.details?.lifecycle_state).toBeUndefined()
      expect(result.error.message).toContain("synthetic failure-close refusal")
    }
    expect(result.result).toBeUndefined()
    expect(validateGeneratedEnvelope(result), JSON.stringify(result)).toBe(true)
    expect(calls.filter(({ command }) => command === "invoke")).toHaveLength(1)
    const evidence = calls.filter(({ command }) => command.startsWith("worker-"))
    expect(evidence.map(({ command }) => command)).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
    expect(evidence.every(({ input }) => input.work_id === recoveryPacket.work_id && input.attempt_id === recoveryPacket.attempt_id)).toBe(true)
    expect(evidence[0].input.event_id).toBe(recoveryContextFixture.dispatch_event_id)
    expect(evidence[0].input.authorized_packet).toEqual(recoveryPacket)
    expect(evidence[2].input.failure_kind).toBe("invalid_report")
    expect(evidence[2].input.failure_attribution.phase).toBe("evidence_write")
    const released = closeState === "recorded"
    expect(windows.inFlight(RECOVERY_SESSION, "retained-part-concord-913") === null).toBe(released)
    if (released) {
      expect(() => windows.open(RECOVERY_SESSION, retained, recoveryContextFixture.packet_digest, process.cwd())).not.toThrow()
    } else {
      expect(() => windows.open(RECOVERY_SESSION, retained, recoveryContextFixture.packet_digest, process.cwd())).toThrow()
    }
  } finally {
    windows.releaseRetained(RECOVERY_SESSION, retained.attempt_id, retained.lane_id)
  }
})

test("CON-913 row 20: accepted evidence with an unconfirmed receipt reports the reconcile tuple", async () => {
  recoveryTranscriptHost()
  let dispatchCalls = 0
  let completeCalls = 0
  let invokeCalls = 0
  const prepareOk = coreEnvelope("concord_work_transition", "worker_reconcile", "ok", {
    result: { changed_refs: [], next_valid_intents: [], worker_recovery: recoveryContextFixture }, changed_refs: [], next_valid_intents: [],
  })
  const receiptPending = coreEnvelope("concord_work_transition", "worker_reconcile", "error", {
    error: { kind: "operation_conflict", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "possible", message: "the completed-attempt receipt is unavailable" },
  })
  const result: any = await runReconcile913({ async run(argv: string[], input: string) {
    if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
    if (argv[1] === "worker-dispatch") {
      dispatchCalls++
      const request = JSON.parse(input)
      return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
    }
    if (argv[1] === "worker-complete") {
      completeCalls++
      const request = JSON.parse(input)
      return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
    }
    if (argv[1] === "invoke") {
      invokeCalls++
      const operation = (JSON.parse(input) as { operation: string }).operation
      if (operation === "worker_reconcile") return invokeCalls === 1 ? { exitCode: 0, stdout: JSON.stringify(prepareOk), stderr: "" } : { exitCode: 0, stdout: JSON.stringify(receiptPending), stderr: "" }
      if (operation === "list") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_browse", "list", "ok", { result: { items: [] } })), stderr: "" }
    }
    throw new Error("unexpected CLI invocation: " + argv.join(" "))
  } })
  expectTuple(result, M, "row 20")
  expect(dispatchCalls).toBe(1)
  expect(completeCalls).toBe(1)
  expect(result.error.message).toContain("receipt")
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
// host session reads are excluded: move-session and the worker readback wrap
// every foreign error, so an injected defect cannot cross them without
// editing files outside this scope.
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

test("CON-913 defect propagation: a defect thrown by the host ask escapes unserialized", async () => {
  const defect = contractDefect()
  adapter.configureConcordAdapter({ runner: runnerWithContext((_argv: string[], _input: string, _signal: AbortSignal, calls: number) => calls === 2 ? approvalChallenge() : approvalSuccess()) })
  let caught: unknown = null
  try {
    await rawHostResult(adapter.work_transition.execute(hostCall("lifecycle", { work_id: "work-1", expected_version: 2, target: "completed", reason: "done", idempotency_key: "defect-ask-913" }), contextFor(async () => { throw defect })))
  } catch (error) {
    caught = error
  }
  expect(caught).toBe(defect)
})

test("CON-913 defect propagation: a defect thrown by the own-row recovery's vacate invoke escapes unserialized", async () => {
  const defect = contractDefect()
  const sessionID = "session-defect-own-row-913"
  hostControlPlane().bind({
    post: async () => ({ response: new Response(null, { status: 204 }) }),
    get: async ({ path }: any) => ({ data: { id: path?.id, directory: process.cwd() }, response: new Response("{}") }),
  })
  const ownRowRefusal = `concord worker-abandon: store: worker_fail: worktree_ownership_conflict: session ${sessionID} still holds the worker attempt worktree; its host process 4242 is still live`
  adapter.configureConcordAdapter({
    credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } },
    runner: { async run(argv: string[], input: string) {
      if (argv[1] === "worker-abandon") return { exitCode: 1, stdout: "", stderr: ownRowRefusal }
      if (argv[1] === "invoke" && (JSON.parse(input) as { operation: string }).operation === "session_vacate") throw defect
      if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    } },
  })
  let caught: unknown = null
  try {
    await rawHostResult(adapter.work_transition.execute(hostCall("worker_abandon", {
      work_id: "work-1", attempt_id: "attempt-1", lane_id: "implement", detail: "the worker session ended", idempotency_key: "defect-own-row-913",
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
