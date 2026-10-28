import { expect, test } from "bun:test"
import { createWorkerCapabilityGuard, toolAllowedByCapabilities } from "./lane_capabilities"
import { agentLanes } from "./generated-agent-lanes"
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { tmpdir } from "node:os"
import { defaultRunner } from "./dispatch"
import { reviewSubjectFailure } from "./worker_subject"
import { DispatchWindows, serializeLanePacket } from "./dispatch-window"
import type { AgentLanePacket, DispatchRunner } from "./dispatch"
import { HostControlPlane } from "./move-session"

test("running-host qualification uses the bound health route and fails closed", async () => {
  const host = new HostControlPlane()
  expect(await host.runningVersion()).toBeNull()
  const cases: Array<[unknown, string | null]> = [[null, null], [[], null], ["1.18.35", null], [{}, null], [{ version: "" }, null], [{ version: 11835 }, null], [{ version: "1.18.35" }, "1.18.35"]]
  for (const [data, expected] of cases) {
    host.bind({
      get: async request => {
        expect(request.url).toBe("/global/health")
        expect(request.signal).toBeInstanceOf(AbortSignal)
        return { data, response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("health must not write") },
    })
    expect(await host.runningVersion()).toBe(expected)
  }
  for (const unreachable of [false, true]) {
    host.bind({ get: async () => {
      if (unreachable) throw new Error("host unavailable")
      return { data: { version: "1.18.35" }, response: new Response(null, { status: 503 }) }
    }, post: async () => { throw new Error("health must not write") } })
    expect(await host.runningVersion()).toBeNull()
  }
})

async function fixture(laneID = "verify", subject?: string, subjectRunner?: DispatchRunner, directory = process.cwd(), readOnly = false) {
  const lane = agentLanes.find((entry) => entry.id === laneID)!
  const packet: AgentLanePacket = {
    schema_version: "1.0", attempt_id: "capability-attempt", work_id: "capability-work", step_id: "execute",
    lane_id: lane.id, lane_digest: readOnly ? "sha256:3a4cd631ddffb655e6a432bb186af0dfe70113031a79c4d45487f53412f2f15d" : lane.digest,
    inputs: { task: "Run the recorded check", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "verification_commands" } },
  }
  if (subject) packet.inputs.work_context = { source_event_frontier: 1, required_reading: [], findings: [], domain_groups: [], subject_commit: subject }
  const windows = new DispatchWindows()
  windows.open("parent", packet, "sha256:" + "a".repeat(64), directory)
  await windows.bind("task", "parent", { subagent_type: `concord-${laneID}`, prompt: "capability-attempt", description: "bounded worker" }, "task-call", async () => directory, directory)
  const state = { agent: `concord-${laneID}`, openingAgent: `concord-${laneID}`, openingRole: "user", openingSession: undefined as string | undefined, parentScope: "managed" as "managed" | "unmanaged" | null, opening: serializeLanePacket(packet), extraPart: false, readerUnavailable: false, paginate: false, messageReads: 0, nestedParent: false, failedReads: new Set<string>(), failedAncestry: new Set<string>(), hostVersion: "1.18.35" as string | null }
  const placement = { directory }
  const reader = {
    get: async (id: string) => {
      if (state.failedReads.has(id)) throw new Error("host session read unavailable")
      return { data: { id, directory: placement.directory, agent: id === "root" || id === "parent" ? "build" : state.agent, ...(id === "root" || id === "parent" ? {} : { parentID: "parent" }) }, response: new Response(null, { status: 200 }) }
    },
    messages: async (id: string, _limit: number, before: string | undefined) => {
      state.messageReads++
      if (state.paginate && before === undefined) return {
        data: Array.from({ length: 100 }, () => ({ info: { role: "assistant", agent: "build" }, parts: [] })),
        response: new Response(null, { status: 200, headers: { link: '<https://host.invalid/session/child/message?before=oldest>; rel="next"' } }),
      }
      return { data: [{ info: { sessionID: state.openingSession ?? id, role: state.openingRole, agent: state.openingAgent }, parts: [{ type: "text", text: state.opening }, ...(state.extraPart ? [{ type: "text", text: "extra instruction" }] : [])] }], response: new Response(null, { status: 200 }) }
    },
  }
  const guard = createWorkerCapabilityGuard({ windows, subjectRunner, sessions: {
    runningVersion: async () => state.hostVersion,
    sessionReader: () => state.readerUnavailable ? null : reader,
    taskScope: async () => state.parentScope,
    hasManagedParent: async (id: string) => {
      if (state.failedAncestry.has(id)) throw new Error("host ancestry read unavailable")
      return id === "parent" ? state.nestedParent : id !== "root"
    },
  } })
  const before = (tool: string, args: Record<string, unknown> = {}, sessionID = "child", callID = "call-native") => guard.before({ tool, sessionID, callID }, { args })
  const isNativeChild = (sessionID: string) => guard.isNativeChildOf(sessionID, "parent")
  return { state, windows, guard, before, isNativeChild, placement }
}

test("the capability ceiling denies shell, editors, and unknown indirect executors to non-authors", () => {
  for (const lane of agentLanes.filter((entry) => !(entry.capabilities as readonly string[]).includes("edit_scoped_files"))) {
    expect(toolAllowedByCapabilities(lane.capabilities, "read")).toBe(true)
    for (const tool of ["bash", "edit", "write", "patch", "apply_patch", "morph_edit", "vision_vision_remove", "linear_update_issue", "new_server_execute", "constructor"]) {
      expect(toolAllowedByCapabilities(lane.capabilities, tool)).toBe(false)
    }
  }
})

test("authoring and execution grants follow capabilities without expanding them", () => {
  expect(toolAllowedByCapabilities(["edit_scoped_files"], "edit")).toBe(true)
  expect(toolAllowedByCapabilities(["edit_scoped_files"], "bash")).toBe(false)
  expect(toolAllowedByCapabilities(["edit_scoped_files", "run_tests"], "bash")).toBe(true)
  expect(toolAllowedByCapabilities(["run_tests"], "concord_worker_check")).toBe(true)
  expect(toolAllowedByCapabilities(["read_repository"], "concord_worker_check")).toBe(false)
  expect(toolAllowedByCapabilities(["run_tests"], "worker_check")).toBe(false)
  expect(toolAllowedByCapabilities(["read_repository"], "exa_web_fetch_exa")).toBe(false)
  expect(toolAllowedByCapabilities(["search_public_sources"], "exa_web_fetch_exa")).toBe(true)
})

test("the authenticated worker guard refuses direct mutation keys", async () => {
  const f = await fixture()
  for (const tool of ["bash", "apply_patch", "morph_edit", "vision_vision_remove", "linear_update_issue"]) {
    await expect(f.before(tool)).rejects.toThrow("does not grant")
  }
  await f.before("read")
})

test("Code Mode grants only its existing leaf capabilities on a qualified host", async () => {
  for (const lane of agentLanes) {
    const f = await fixture(lane.id)
    await f.before("execute", { code: "return tools.lgrep.search_text({path: 'ignored'})" })
    await f.before("lgrep_search_text", { path: process.cwd() })
    await expect(f.before("lgrep_search_text", { path: "/tmp" })).rejects.toThrow("dispatched repository")
    for (const tool of ["linear_update_issue", "vision_vision_remove", "lgrep_invalidate_cache", "unknown_server_read"]) {
      await expect(f.before(tool)).rejects.toThrow("does not grant")
    }
    const publicAccess = (lane.capabilities as readonly string[]).includes("search_public_sources")
    for (const tool of ["exa_web_search_exa", "context7_query-docs"]) {
      if (publicAccess) await f.before(tool)
      else await expect(f.before(tool)).rejects.toThrow("does not grant")
    }
    f.windows.takeInFlight("parent")
    await expect(f.before("lgrep_search_text", { path: process.cwd() })).rejects.toThrow("active authorized attempt")
  }
  expect(toolAllowedByCapabilities([], "execute")).toBe(false)
  expect(toolAllowedByCapabilities(["run_tests"], "execute")).toBe(false)
})

test("Code Mode refuses unknown and unqualified running hosts without blocking direct tools", async () => {
  for (const version of [null, "", "1.18.34", "1.18.36", "2.0.0"]) {
    const f = await fixture()
    f.state.hostVersion = version
    await expect(f.before("execute", { code: "return 1" })).rejects.toThrow("qualified running host")
    await f.before("read")
    await f.before("lgrep_search_text", { path: process.cwd() })
  }
})

test("nested MCP names cannot impersonate plugin or native tools", async () => {
  for (const lane of agentLanes) {
    const f = await fixture(lane.id)
    await f.before("execute", {}, "child", "outer")
    for (const tool of ["concord_worker_read", "concord_worker_check", "concord_worker_diff", "concord_knowledge", "apply_patch", "morph_edit", "execute"]) {
      await expect(f.before(tool, {}, "child", "outer/1")).rejects.toThrow("does not grant MCP tool")
    }
    await f.before("lgrep_search_text", { path: process.cwd() }, "child", "outer/2")
    await expect(f.before("lgrep_search_text", { path: "/tmp" }, "child", "outer/3")).rejects.toThrow("dispatched repository")
    await f.before("concord_worker_read", {}, "child", "a/b/1")
    expect(f.guard.isNestedMcp({ sessionID: "other", callID: "outer/1" })).toBe(false)
    f.guard.after({ tool: "execute", sessionID: "child", callID: "outer" })
    expect(f.guard.isNestedMcp({ sessionID: "child", callID: "outer/1" })).toBe(false)
    await f.before("concord_worker_read", {}, "child", "outer/1")
  }
})

test("only admitted native executions register nested identities, and terminal events clear them", async () => {
  const unknown = await fixture()
  unknown.state.hostVersion = null
  await expect(unknown.before("execute", {}, "child", "unqualified")).rejects.toThrow("qualified running host")
  expect(unknown.guard.isNestedMcp({ sessionID: "child", callID: "unqualified/1" })).toBe(false)
  for (const type of ["session.idle", "session.error", "session.deleted"]) {
    const f = await fixture()
    await expect(f.guard.before({ tool: "execute", sessionID: "child" }, { args: {} })).rejects.toThrow("native host call identity")
    await f.before("execute", {}, "child", "outer")
    f.guard.event({ type, properties: type === "session.deleted" ? { info: { id: "child" } } : { sessionID: "child" } })
    expect(f.guard.isNestedMcp({ sessionID: "child", callID: "outer/1" })).toBe(false)
  }
})

test("local MCP reads and cache preparation stay in the dispatched repository", async () => {
  const f = await fixture()
  for (const tool of ["lgrep_search_text", "lgrep_index_symbols_folder", "lgrep_index_semantic"]) {
    await f.before(tool, { path: process.cwd() })
    await expect(f.before(tool, { path: "/tmp" })).rejects.toThrow("dispatched repository")
    await expect(f.before(tool)).rejects.toThrow("dispatched repository")
  }
  await expect(f.before("lgrep_invalidate_cache", { path: process.cwd() })).rejects.toThrow("does not grant")
})

test("a different opening packet or an added instruction grants no worker tool access", async () => {
  for (const variant of ["different", "extra"]) {
    const f = await fixture()
    if (variant === "different") f.state.opening = "not the authorized packet"
    else f.state.extraPart = true
    await expect(f.before("read")).rejects.toThrow("authorized packet")
  }
})

test("one authorized attempt binds only one host child", async () => {
  const f = await fixture()
  await f.before("read")
  await expect(f.before("read", {}, "second-child")).rejects.toThrow("another host child")
})

test("agent switches cannot widen the bound worker capability ceiling", async () => {
  const f = await fixture()
  await f.before("read")
  f.state.agent = "build"
  await expect(f.before("bash")).rejects.toThrow("does not grant")
})

test("every bound lane keeps its tool ceiling despite a broader selected agent", async () => {
  for (const lane of agentLanes) {
    const f = await fixture(lane.id)
    await f.before("read")
    f.state.agent = "build"
    for (const tool of ["question", "task", "concord_work_transition"]) {
      await expect(f.before(tool)).rejects.toThrow(`lane ${lane.id} does not grant tool ${tool}`)
    }
    await f.before("read")
  }
})

test("a settling or retired attempt grants no further worker calls", async () => {
  const f = await fixture()
  await f.before("read")
  const record = f.windows.claimSettlement("parent")!
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
  f.windows.unclaimSettlement("parent", record)
  f.windows.takeInFlight("parent")
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
})

test("a new message on a retired child restores its restriction before any tool call", async () => {
  const f = await fixture()
  f.windows.takeInFlight("parent")
  await f.guard.observe("child")
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
})

test("a verified root session does not depend on host reads while another worker runs", async () => {
  const f = await fixture()
  await f.guard.observe("root")
  f.state.failedReads.add("root")
  f.state.failedAncestry.add("root")
  await f.before("bash", {}, "root")
  await f.guard.observe("root")
})

test("a cached root switched to a lane must acquire an authorized attempt", async () => {
  const f = await fixture()
  await f.guard.observe("root", "build")
  await f.guard.observe("root", "concord-review")
  await expect(f.before("read", {}, "root")).rejects.toThrow("active authorized attempt")
  await f.guard.observe("root", "build")
  await f.before("bash", {}, "root")
})

test("an unobserved caller without host parent identity cannot bypass an active attempt", async () => {
  const f = await fixture()
  f.state.readerUnavailable = true
  for (const sessionID of ["root", "child"]) {
    await expect(f.before("read", {}, sessionID)).rejects.toThrow("cannot authenticate")
  }
})

test("an ancestry read failure does not abort chat or let a retired child escape", async () => {
  const f = await fixture()
  f.windows.takeInFlight("parent")
  f.state.failedAncestry.add("child")
  await f.guard.observe("child")
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
})

test("root host work and a coordinator utility do not acquire or consume lane authority", async () => {
  const f = await fixture()
  await f.before("bash", {}, "root")
  f.state.agent = f.state.openingAgent = "concord-explore"
  f.state.opening = "Read the bounded utility assignment"
  await f.before("read")
  expect(f.windows.inFlightAttempt("parent")?.packet.attempt_id).toBe("capability-attempt")
})

test("a retired lane child stays fenced after an agent switch, without a cached native identity", async () => {
  const f = await fixture()
  await f.before("read")
  f.windows.takeInFlight("parent")
  f.state.agent = "build"
  await f.guard.observe("child", "build")
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
  await expect(f.isNativeChild("child")).resolves.toBe(false)
})

test("ordinary host children cannot claim a lane child slot even with exact packet bytes", async () => {
  for (const agent of ["host-reviewer", "concord-local-reviewer", "general"]) {
    const f = await fixture()
    f.state.agent = f.state.openingAgent = agent
    await f.before("bash")
    expect(await f.guard.authorize("child")).toBeNull()
    f.state.agent = f.state.openingAgent = "concord-verify"
    await f.before("read", {}, "real-worker")
    await expect(f.before("bash", {}, "real-worker")).rejects.toThrow("does not grant")
    expect(f.windows.inFlightAttempt("parent")?.packet.attempt_id).toBe("capability-attempt")
  }
})

test("ordinary children under an enrolled parent need no live attempt", async () => {
  const f = await fixture()
  f.windows.takeInFlight("parent")
  f.state.agent = f.state.openingAgent = "host-reviewer"
  await f.guard.observe("child")
  await f.before("bash")
  expect(await f.guard.authorize("child")).toBeNull()
})

test("native provenance is read once across pages, but current identity and role still gate tools", async () => {
  const f = await fixture()
  f.state.agent = f.state.openingAgent = "host-reviewer"
  f.state.paginate = true
  await f.before("bash")
  expect(f.state.messageReads).toBe(2)
  for (let i = 0; i < 10; i++) await f.before("read")
  expect(f.state.messageReads).toBe(2)
  f.state.agent = "concord-review"
  await expect(f.before("read")).rejects.toThrow("authorized attempt")
  f.state.agent = "host-reviewer"
  f.state.failedReads.add("child")
  await expect(f.before("read")).rejects.toThrow("host session read unavailable")
})

test("malformed original identity and nested or missing ancestry never authorize native tools", async () => {
  const refusal = { agent: "original host child agent", role: "original host child agent", session: "original host child agent", nested: "cannot nest", missing: "host parent session is missing" }
  for (const variant of ["agent", "role", "session", "nested", "missing"] as const) {
    const f = await fixture()
    f.state.agent = f.state.openingAgent = "host-reviewer"
    if (variant === "agent") f.state.openingAgent = ""
    if (variant === "role") f.state.openingRole = "assistant"
    if (variant === "session") f.state.openingSession = "foreign-child"
    if (variant === "nested") f.state.nestedParent = true
    if (variant === "missing") {
      f.windows.takeInFlight("parent")
      f.state.parentScope = null
      await f.guard.observe("child")
    }
    await expect(f.before("read")).rejects.toThrow(refusal[variant])
  }
})

test("a stale Review digest grants no tools and preserves the original packet", async () => {
  const subject = "a".repeat(40)
  const calls: string[][] = []
  const subjectRunner: DispatchRunner = { run: async (argv) => {
    calls.push(argv)
    return { exitCode: 0, stdout: "", stderr: "", signal_state: null }
  } }
  const f = await fixture("review", subject, subjectRunner, process.cwd(), true)
  const before = serializeLanePacket(f.windows.activeAttempt("parent")!.packet)
  for (const tool of ["read", "edit", "bash", "concord_worker_check"]) {
    await expect(f.before(tool)).rejects.toThrow("content revalidation")
  }
  expect(serializeLanePacket(f.windows.activeAttempt("parent")!.packet)).toBe(before)
  expect(calls).toHaveLength(0)
})

test("repairing Review admits clean B, two edits and a commit, and verifies clean terminal C", async () => {
  const root = mkdtempSync(join(tmpdir(), "review-capability-"))
  const signal = AbortSignal.timeout(15_000)
  const git = async (...args: string[]) => {
    const r = await defaultRunner.run(["git", "-C", root, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", ...args], "", signal)
    expect(r.exitCode, r.stderr).toBe(0)
    return r.stdout.trim()
  }
  try {
    await git("init", "--quiet", "--initial-branch=main")
    await git("commit", "--quiet", "--allow-empty", "-m", "baseline")
    const b = await git("rev-parse", "HEAD")
    const f = await fixture("review", b, defaultRunner, root)
    const record = f.windows.activeAttempt("parent")!
    const lane = agentLanes.find((x) => x.id === "review")!
    await f.before("edit")
    writeFileSync(join(root, "value.txt"), "first edit\n")
    await f.before("edit")
    writeFileSync(join(root, "value.txt"), "second edit\n")
    await expect(f.before("concord_worker_check")).rejects.toThrow("tree_clean=false")
    await f.before("bash")
    await git("add", "value.txt")
    await f.before("bash")
    await git("commit", "--quiet", "-m", "repair")
    const c = await git("rev-parse", "HEAD")
    expect(c).not.toBe(b)
    await f.before("concord_worker_check")
    expect(await reviewSubjectFailure(lane, record.packet, root, signal, defaultRunner, "terminal")).toBeNull()
    f.placement.directory = tmpdir()
    await expect(f.before("edit")).rejects.toThrow("directory")
    f.placement.directory = root
    f.windows.claimSettlement("parent")
    await expect(f.before("edit")).rejects.toThrow("active authorized attempt")
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
