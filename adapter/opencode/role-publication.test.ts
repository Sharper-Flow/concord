// Role publication conformance. The generated denial map projects each
// registered role's no-overlay parameter-schema budget out of the complete
// registered catalog: host visibility and execution denial derive from one
// registry population, so the published byte totals a role's session can
// reach are a test-owned consequence of the generated grants, not a prose
// claim. Every number here is a projection from resolved host permission
// semantics (catalog minus generated denials, selected by whole tool), not a
// captured provider request, and not an immutable ceiling: a later allow
// rule can re-expose a denied tool, so the no-overlay budget bounds the
// generated configuration only. The real-host merge evidence for that
// boundary is the opt-in `--host-merge` mode of host_schema_probe.ts.
import { expect, test } from "bun:test"
import fs from "node:fs"
import path from "node:path"
import { contractOperations, hostToolSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema } from "./generated-contract-tests"
import { ciWatchTool, publishCiWatchDefinition } from "./ci-watch"
import { publishWorkerReadDefinition } from "./worker_source"
import { publishWorkerCheckDefinition } from "./worker_control"
import { publishWorkerDiffDefinition } from "./worker_diff"
import { guardPrimaryRole, PrimaryRoleRefusal } from "./primary-role"
import { roleDeniedConcordTools } from "./generated-primary-profiles"
import { agentLanes, agentUtilities } from "./generated-agent-lanes"

const adapter = await import("./concord")

// The complete registered catalog: the request tools plus the
// separately hooked host tools.
const CATALOG = [...new Set(contractOperations.map((operation: any) => operation.tool)), ...Object.keys(hostToolSchemas)].sort()
const LANE_AGENTS = agentLanes.map((lane) => `concord-${lane.id}`)
const UTILITY_AGENTS = agentUtilities.map((utility) => `concord-${utility.id}`)
const READS = ["concord_product_view", "concord_work_browse", "concord_work_trace", "concord_knowledge", "concord_domain"]

// Every registered parameter root, measured through the production
// definition hooks exactly as the host renders them for the provider.
const registeredBytes = new Map<string, number>()
for (const tool of [...new Set(contractOperations.map((operation: any) => operation.tool))]) {
  const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await adapter.publishRequestDefinition({ toolID: tool }, output)
  expect(output.jsonSchema, tool).toBeObject()
  registeredBytes.set(tool, Buffer.byteLength(JSON.stringify(output.jsonSchema), "utf8"))
}
const workStartOutput = { description: "", parameters: {}, jsonSchema: undefined as unknown }
await adapter.publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartOutput)
expect(workStartOutput.jsonSchema).toBeObject()
registeredBytes.set("concord_work_start", Buffer.byteLength(JSON.stringify(workStartOutput.jsonSchema), "utf8"))
const watcher = ciWatchTool()
const watcherRegistration = {
  description: watcher.description,
  parameters: {},
  jsonSchema: {
    type: "object",
    properties: Object.fromEntries(Object.entries(watcher.args as Record<string, unknown>).filter(([, value]) => value !== undefined)),
    required: Object.keys(watcher.args as Record<string, unknown>),
  },
}
await publishCiWatchDefinition({ toolID: "concord_ci_watch" }, watcherRegistration)
expect(watcherRegistration.jsonSchema).toBeObject()
registeredBytes.set("concord_ci_watch", Buffer.byteLength(JSON.stringify(watcherRegistration.jsonSchema), "utf8"))
const readerOutput = { description: "", parameters: {}, jsonSchema: undefined as unknown }
await publishWorkerReadDefinition({ toolID: "concord_worker_read" }, readerOutput)
expect(readerOutput.jsonSchema).toEqual(hostToolSchemas.concord_worker_read)
registeredBytes.set("concord_worker_read", Buffer.byteLength(JSON.stringify(readerOutput.jsonSchema), "utf8"))
const checkOutput = { description: "", parameters: {}, jsonSchema: undefined as unknown }
await publishWorkerCheckDefinition({ toolID: "concord_worker_check" }, checkOutput)
expect(checkOutput.jsonSchema).toEqual(hostToolSchemas.concord_worker_check)
registeredBytes.set("concord_worker_check", Buffer.byteLength(JSON.stringify(checkOutput.jsonSchema), "utf8"))
const diffOutput = { description: "", parameters: {}, jsonSchema: undefined as unknown }
await publishWorkerDiffDefinition({ toolID: "concord_worker_diff" }, diffOutput)
expect(diffOutput.jsonSchema).toEqual(hostToolSchemas.concord_worker_diff)
const diffBytes = 398
registeredBytes.set("concord_worker_diff", Buffer.byteLength(JSON.stringify(diffOutput.jsonSchema), "utf8"))

function projectedBytes(role: string): { allowed: string[]; bytes: number } {
  const denied = new Set(roleDeniedConcordTools[role].tools)
  const allowed = CATALOG.filter((tool) => !denied.has(tool))
  // No fallback-zero accounting: a projection may only sum roots the
  // registration loop measured, so a missing entry fails the test instead
  // of silently contributing zero bytes.
  let bytes = 0
  for (const tool of allowed) {
    const size = registeredBytes.get(tool)
    if (size === undefined) throw new Error(`${tool}: the projection summed a tool with no registered root`)
    bytes += size
  }
  return { allowed, bytes }
}

test("every registered role's denial set stays inside the complete catalog", () => {
  const deniedUnion = new Set<string>()
  for (const [role, entry] of Object.entries(roleDeniedConcordTools)) {
    expect(entry.boundary, role).toBeString()
    for (const tool of entry.tools) {
      expect(CATALOG.includes(tool), `${role} denies unregistered tool ${tool}`).toBe(true)
      deniedUnion.add(tool)
    }
  }
  // The union covers the whole catalog. The watcher is denied by no primary
  // profile, so losing it from the lane/utility projection would leave the
  // catalog uncovered and fail here.
  expect([...deniedUnion].sort(), "the generated denials no longer cover the registered catalog").toEqual(CATALOG)
})

test("the authored registries carry exactly the generated role population and grants", () => {
  const profiles = JSON.parse(
    fs.readFileSync(path.resolve(import.meta.dir, "../../contracts/agent-primary-profiles.v1.json"), "utf8"),
  ) as { profiles: Array<{ id: string; denied_concord_tools: string[] }> }
  const manifest = JSON.parse(
    fs.readFileSync(path.resolve(import.meta.dir, "../../contracts/agent-lanes.v1.json"), "utf8"),
  ) as { lanes: Array<{ id: string }>; utilities: Array<{ id: string; allowed_tools: string[] }> }
  const population = [...profiles.profiles.map((profile) => profile.id), ...LANE_AGENTS, ...UTILITY_AGENTS].sort()
  expect(Object.keys(roleDeniedConcordTools).sort(), "the generated role map and the authored registries disagree").toEqual(population)
  for (const profile of profiles.profiles) {
    expect([...roleDeniedConcordTools[profile.id].tools].sort(), profile.id).toEqual([...profile.denied_concord_tools].sort())
  }
  // Utilities enumerate builtins. Lanes declare capabilities instead of
  // utility-style tool lists.
  for (const utility of manifest.utilities) {
    const granted = utility.allowed_tools.filter((tool) => CATALOG.includes(tool))
    expect(granted, `${utility.id} grants registered Concord tools`).toEqual([])
  }
  for (const lane of manifest.lanes) {
    expect(Object.hasOwn(lane, "allowed_tools"), `${lane.id} gained a utility-style tool grant`).toBe(false)
  }
})

test("per-role no-overlay byte budgets match the complete parameter roots", () => {
  // Pinned measured budgets at this head. A catalog or schema change moves
  // these numbers, which is a visible test change, never silent drift.
  // Coverage and positivity are asserted before any total is formed: a
  // role's zero budget is its empty allowed list, never a missing root.
  expect([...registeredBytes.keys()].sort(), "registered byte accounting must cover the catalog exactly").toEqual(CATALOG)
  for (const [tool, size] of registeredBytes) {
    expect(Number.isFinite(size) && size > 0, `${tool} must carry a finite positive root size`).toBe(true)
  }
  const READER_BUDGET = { allowed: ["concord_worker_read"], bytes: 601 }
  const CHECKER_BUDGET = { allowed: ["concord_worker_check", "concord_worker_read"], bytes: 1013 }
  const EMPTY_BUDGET = { allowed: [] as string[], bytes: 0 }
  const PRIMARY_BUDGET = { allowed: CATALOG, bytes: 156_128 }
  // Neither full primary profile denies a tool, so each no-overlay budget
  // is the whole registered catalog at the full published total. Planner
  // loses exactly the work-state writers' roots.
  expect(projectedBytes("Concord-RTE")).toEqual(PRIMARY_BUDGET)
  expect(projectedBytes("Concord")).toEqual(PRIMARY_BUDGET)
  const WRITERS = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
  const writerBytes = WRITERS.reduce((sum, tool) => sum + (registeredBytes.get(tool) ?? Number.NaN), 0)
  expect(projectedBytes("Concord-Planner")).toEqual({ allowed: CATALOG.filter((tool) => !WRITERS.includes(tool)), bytes: PRIMARY_BUDGET.bytes - writerBytes })
  for (const lane of agentLanes) {
    const role = `concord-${lane.id}`
    const executes = lane.capabilities.some((capability) => ["run_tests", "run_targeted_checks", "run_validators"].includes(capability))
    const expected = executes ? CHECKER_BUDGET : lane.capabilities.includes("read_repository") ? READER_BUDGET : EMPTY_BUDGET
    expect(projectedBytes(role), role).toEqual(lane.capabilities.some(capability => capability === "inspect_diff") ? { allowed: [...expected.allowed, "concord_worker_diff"].sort(), bytes: expected.bytes + diffBytes } : expected)
  }
  for (const role of UTILITY_AGENTS) {
    expect(projectedBytes(role), role).toEqual(EMPTY_BUDGET)
  }
  const registeredTotal = [...registeredBytes.values()].reduce((sum, size) => sum + size, 0)
  expect(registeredTotal).toBe(156_128)
})

test("grants select whole tools and never trim a published schema", async () => {
  // Each allowed tool keeps its complete per-operation branch list; the
  // primary profiles keep the full transition root with every registry
  // action variant. Role authority never rewrites publication: it can only
  // withhold a whole tool.
  for (const tool of READS) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await adapter.publishRequestDefinition({ toolID: tool }, output)
    const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
    const operations = contractOperations.filter((operation: any) => operation.tool === tool)
    expect(expanded.oneOf, tool).toHaveLength(operations.length)
  }
  expect(registeredBytes.get("concord_work_transition")).toBe(99_233)
  expect(workflowActionPublicVariants.length).toBe(31)
})

test("publication is role-blind, so a denied role's budget is not an immutable ceiling", async () => {
  // Registration publishes the complete root for every tool regardless of
  // any role's denial: the definition hook receives only the tool ID. The
  // role projection is therefore host visibility, not schema removal. A
  // later session or agent allow can re-expose a denied tool. The
  // real-host demonstration of that growth is the opt-in
  // --host-merge probe; this test pins the structural precondition.
  for (const tool of CATALOG) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    if (tool === "concord_work_start") {
      await adapter.publishWorkStartDefinition({ toolID: tool }, output)
    } else if (tool === "concord_ci_watch") {
      await publishCiWatchDefinition({ toolID: tool }, output)
    } else if (tool === "concord_worker_read") {
      await publishWorkerReadDefinition({ toolID: tool }, output)
    } else if (tool === "concord_worker_check") {
      await publishWorkerCheckDefinition({ toolID: tool }, output)
    } else if (tool === "concord_worker_diff") {
      await publishWorkerDiffDefinition({ toolID: tool }, output)
    } else {
      await adapter.publishRequestDefinition({ toolID: tool }, output)
    }
    const rootBytes = registeredBytes.get(tool)
    expect(rootBytes, tool).toBeNumber()
    expect(Buffer.byteLength(JSON.stringify(output.jsonSchema), "utf8"), tool).toBe(rootBytes!)
  }
})

test("every generated denial still refuses with the typed role refusal", async () => {
  for (const [role, entry] of Object.entries(roleDeniedConcordTools)) {
    for (const toolID of entry.tools) {
      const calls: unknown[] = []
      const guarded = guardPrimaryRole(toolID, { description: "stub", args: {}, execute: async (args: unknown, _context: unknown) => { calls.push(args); return "ran" } })
      await expect(guarded.execute({ request: {} }, { agent: role })).rejects.toBeInstanceOf(PrimaryRoleRefusal)
      expect(calls, `${role} executed denied tool ${toolID}`).toEqual([])
    }
  }
})
