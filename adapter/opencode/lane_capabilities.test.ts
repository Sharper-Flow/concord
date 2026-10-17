import { expect, test } from "bun:test"
import { createWorkerCapabilityGuard, toolAllowedByCapabilities } from "./lane_capabilities"
import { agentLanes } from "./generated-agent-lanes"
import { DispatchWindows, serializeLanePacket } from "./dispatch-window"
import type { AgentLanePacket, DispatchRunner } from "./dispatch"

async function fixture(laneID = "verify", subject?: string, subjectRunner?: DispatchRunner) {
  const lane = agentLanes.find((entry) => entry.id === laneID)!
  const packet: AgentLanePacket = {
    schema_version: "1.0", attempt_id: "capability-attempt", work_id: "capability-work", step_id: "execute",
    lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest,
    inputs: { task: "Run the recorded check", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "verification_commands" } },
  }
  if (subject) packet.inputs.work_context = { source_event_frontier: 1, required_reading: [], findings: [], domain_groups: [], subject_commit: subject }
  const windows = new DispatchWindows()
  windows.open("parent", packet, "sha256:" + "a".repeat(64), process.cwd())
  await windows.bind("task", "parent", {}, "task-call", async () => process.cwd(), process.cwd())
  const state = { agent: `concord-${laneID}`, openingAgent: `concord-${laneID}`, opening: serializeLanePacket(packet), extraPart: false, failedReads: new Set<string>(), failedAncestry: new Set<string>() }
  const reader = {
    get: async (id: string) => {
      if (state.failedReads.has(id)) throw new Error("host session read unavailable")
      return { data: { id, directory: process.cwd(), agent: id === "root" || id === "parent" ? "build" : state.agent, ...(id === "root" || id === "parent" ? {} : { parentID: "parent" }) }, response: new Response(null, { status: 200 }) }
    },
    messages: async () => ({ data: [{ info: { role: "user", agent: state.openingAgent }, parts: [{ type: "text", text: state.opening }, ...(state.extraPart ? [{ type: "text", text: "extra instruction" }] : [])] }], response: new Response(null, { status: 200 }) }),
  }
  const guard = createWorkerCapabilityGuard({ windows, subjectRunner, sessions: {
    sessionReader: () => reader,
    taskScope: async () => "managed" as const,
    hasManagedParent: async (id: string) => {
      if (state.failedAncestry.has(id)) throw new Error("host ancestry read unavailable")
      return id !== "parent" && id !== "root"
    },
  } })
  const before = (tool: string, args: Record<string, unknown> = {}, sessionID = "child") => guard.before({ tool, sessionID }, { args })
  return { state, windows, guard, before }
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

test("the authenticated worker hook refuses direct and nested MCP mutation before execution", async () => {
  const f = await fixture()
  for (const tool of ["bash", "apply_patch", "morph_edit", "vision_vision_remove", "linear_update_issue"]) {
    await expect(f.before(tool)).rejects.toThrow("does not grant")
  }
  await f.before("execute")
  await f.before("read")
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

test("a settling or retired attempt grants no further worker calls", async () => {
  const f = await fixture()
  await f.before("read")
  f.windows.claimSettlement("parent")
  await expect(f.before("read")).rejects.toThrow("active authorized attempt")
  f.windows.unclaimSettlement("parent")
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

test("review tool admission refuses subject drift even after the host child is bound", async () => {
  const subject = "a".repeat(40)
  let head = subject
  let dirty = false
  const calls: string[][] = []
  const subjectRunner: DispatchRunner = { run: async (argv) => {
    calls.push(argv)
    return { exitCode: 0, stdout: argv.includes("rev-parse") ? head + "\n" : dirty ? "?? file\0" : "", stderr: "", signal_state: null }
  } }
  const f = await fixture("review", subject, subjectRunner)
  await f.before("read")
  head = "b".repeat(40)
  await expect(f.before("read")).rejects.toThrow("review subject")
  head = subject
  dirty = true
  await expect(f.before("read")).rejects.toThrow("review subject")
  expect(calls.length).toBeGreaterThan(0)
  for (const argv of calls) {
    expect(argv).toEqual(argv.includes("rev-parse")
      ? ["git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", process.cwd(), "rev-parse", "--verify", "HEAD^{commit}"]
      : ["git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", process.cwd(), "status", "--porcelain=v1", "-z", "--untracked-files=all"])
  }
})
