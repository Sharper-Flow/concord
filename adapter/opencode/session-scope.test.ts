import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { dispatchWindows } from "./dispatch-window"
import { configureHostLease } from "./host-lease"
import { HostControlPlane, hostControlPlane, SESSION_ROUTE } from "./move-session"

const SCOPE_KEY = "concord.task_scope"
type Session = { id: string; directory: string; parentID?: string; agent?: string; metadata?: Record<string, unknown> }

function host(records: Session[], persist = true) {
  const sessions = new Map(records.map((session) => [session.id, structuredClone(session)]))
  const reads: string[] = []
  const writes: Array<{ url: string; path?: Record<string, unknown>; body?: unknown }> = []
  const client = {
    get: async ({ url, path, signal }: { url: string; path?: Record<string, unknown>; signal?: AbortSignal }) => {
      signal?.throwIfAborted()
      expect(url).toBe(SESSION_ROUTE)
      const id = String(path?.id)
      reads.push(id)
      const session = sessions.get(id)
      return { data: structuredClone(session), response: new Response(null, { status: session ? 200 : 404 }) }
    },
    patch: async (request: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
      writes.push(structuredClone({ url: request.url, path: request.path, body: request.body }))
      const id = String(request.path?.id)
      const session = sessions.get(id)
      if (!session) return { response: new Response(null, { status: 404 }) }
      const body = request.body as { metadata: Record<string, unknown> }
      if (persist) session.metadata = structuredClone(body.metadata)
      return { data: structuredClone(session), response: new Response(null, { status: 200 }) }
    },
    post: async () => { throw new Error("scope admission must not change permissions, move a session, or invoke a core operation") },
  }
  return { sessions, reads, writes, client, plugin: () => ConcordAdapterPlugin({ client: { _client: client } as never }) }
}

const session = (id: string, managed = false): Session => ({ id, directory: "/synthetic/worktree", metadata: managed ? { [SCOPE_KEY]: "managed" } : {} })
const task = (sessionID: string) => ({ tool: "task", sessionID, callID: "scope-call" })
const args = (): { subagent_type: string; prompt: string; description: string; task_id?: string } => ({ subagent_type: "general", prompt: "Read the bounded source", description: "Read source", task_id: "prior-host-worker" })

afterEach(() => {
  dispatchWindows().close("scope-window")
  dispatchWindows().takeInFlight("scope-window")
  hostControlPlane().bind(undefined)
  configureHostLease({ reset: true })
})

test("an unmanaged native Task preserves arguments and creates no Concord evidence", async () => {
  const fixture = host([{ ...session("scope-unmanaged"), agent: "concord-1" }])
  const plugin = await fixture.plugin()
  const output = { args: args() }
  const original = structuredClone(output.args)
  await plugin["tool.execute.before"](task("scope-unmanaged"), output)
  expect(output.args).toEqual(original)
  expect(fixture.writes).toEqual([])
  expect(dispatchWindows().takeInFlight("scope-unmanaged")).toBeNull()
  const result = { title: "native task", output: "native result", metadata: {} }
  await plugin["tool.execute.after"]({ ...task("scope-unmanaged"), args: output.args }, result)
  expect(result).toEqual({ title: "native task", output: "native result", metadata: {} })
})

test("managed scope refuses ordinary and Concord Tasks without a window", async () => {
  const fixture = host([session("scope-managed", true)])
  const plugin = await fixture.plugin()
  for (const subagent_type of ["general", "explore", "concord-implement"]) {
    const output = { args: { ...args(), subagent_type } }
    const original = structuredClone(output.args)
    await expect(plugin["tool.execute.before"](task("scope-managed"), output)).rejects.toThrow("no authorized dispatch window")
    expect(output.args).toEqual(original)
  }
})

test("managed participation survives agent changes and plugin recreation", async () => {
  const fixture = host([{ ...session("scope-restart", true), agent: "concord-1" }])
  const first = await fixture.plugin()
  await expect(first["tool.execute.before"](task("scope-restart"), { args: args() })).rejects.toThrow("no authorized dispatch window")
  fixture.sessions.get("scope-restart")!.agent = "build"
  const freshControlPlane = new HostControlPlane()
  freshControlPlane.bind(fixture.client)
  expect(await freshControlPlane.taskScope("scope-restart")).toBe("managed")
  const restarted = await fixture.plugin()
  await expect(restarted["tool.execute.before"](task("scope-restart"), { args: args() })).rejects.toThrow("no authorized dispatch window")
  expect(fixture.writes).toEqual([])
})

test("an unmarked child inherits managed participation from its host parent", async () => {
  const fixture = host([session("scope-parent", true), { ...session("scope-child"), parentID: "scope-parent", agent: "build" }])
  const plugin = await fixture.plugin()
  await expect(plugin["tool.execute.before"](task("scope-child"), { args: args() })).rejects.toThrow("no authorized dispatch window")
  expect(fixture.reads).toEqual(["scope-child", "scope-parent"])
})

test("a coordinator can auto-start the CI wait utility without a dispatch window", async () => {
  const fixture = host([session("scope-coordinator", true)])
  const plugin = await fixture.plugin()
  const output = { args: { ...args(), subagent_type: "concord-ci-wait" } }
  const original = structuredClone(output.args)
  await plugin["tool.execute.before"](task("scope-coordinator"), output)
  expect(output.args).toEqual(original)
  expect(fixture.reads).toEqual(["scope-coordinator"])
  expect(fixture.writes).toEqual([])
  const result = { title: "CI wait", output: "result", metadata: {} }
  await plugin["tool.execute.after"]({ ...task("scope-coordinator"), args: output.args }, result)
  expect(result).toEqual({ title: "CI wait", output: "result", metadata: {} })
  expect(dispatchWindows().takeInFlight("scope-coordinator")).toBeNull()
})

test("a lane session cannot auto-start the CI wait utility", async () => {
  const fixture = host([session("scope-parent", true), { ...session("scope-lane"), parentID: "scope-parent" }])
  const plugin = await fixture.plugin()
  const output = { args: { ...args(), subagent_type: "concord-ci-wait" } }
  const original = structuredClone(output.args)
  await expect(plugin["tool.execute.before"](task("scope-lane"), output)).rejects.toThrow("managed parent")
  expect(output.args).toEqual(original)
  expect(fixture.reads).toEqual(["scope-lane", "scope-parent"])
  expect(fixture.writes).toEqual([])
})

test("unmanaged sessions cannot invoke Concord lanes without authorization", async () => {
  const fixture = host([session("scope-direct")])
  const plugin = await fixture.plugin()
  const output = { args: { ...args(), subagent_type: "concord-research" } }
  await expect(plugin["tool.execute.before"](task("scope-direct"), output)).rejects.toThrow("no authorized dispatch window")
  expect(fixture.writes).toEqual([])
})

test("an unmanaged caller cannot resume a managed session or its child", async () => {
  const fixture = host([session("scope-caller"), session("scope-target", true), { ...session("scope-target-child"), parentID: "scope-target" }])
  const plugin = await fixture.plugin()
  for (const task_id of ["scope-target", "scope-target-child"]) {
    const output = { args: { ...args(), task_id } }
    const original = structuredClone(output.args)
    await expect(plugin["tool.execute.before"](task("scope-caller"), output)).rejects.toThrow("cannot resume a managed Concord session")
    expect(output.args).toEqual(original)
  }
})

test("unmanaged resume targets and absent targets retain native Task behavior", async () => {
  const fixture = host([session("scope-caller"), session("scope-native")])
  const plugin = await fixture.plugin()
  for (const task_id of ["scope-native", "scope-absent-target", ""]) {
    const output = { args: { ...args(), task_id } }
    const original = structuredClone(output.args)
    await plugin["tool.execute.before"](task("scope-caller"), output)
    expect(output.args).toEqual(original)
  }
})

test("an authorized Task binds once and the managed session stays protected", async () => {
  const fixture = host([session("scope-window", true), session("scope-other", true)])
  const plugin = await fixture.plugin()
  const packet = { schema_version: "1.0" as const, attempt_id: "scope-attempt", lane_id: "implement", lane_version: 1, lane_digest: "sha256:" + "a".repeat(64), work_id: "scope-work", step_id: "repair", inputs: { task: "Approved task" } }
  dispatchWindows().open("scope-window", packet)
  await expect(plugin["tool.execute.before"](task("scope-other"), { args: args() })).rejects.toThrow("no authorized dispatch window")
  expect(dispatchWindows().has("scope-window")).toBe(true)
  const output = { args: args() }
  await plugin["tool.execute.before"](task("scope-window"), output)
  expect(output.args).toEqual({ subagent_type: "concord-implement", prompt: JSON.stringify(packet), description: "implement lane, attempt scope-attempt" })
  await expect(plugin["tool.execute.before"](task("scope-window"), { args: args() })).rejects.toThrow("no authorized dispatch window")
})

test("unknown metadata and broken ancestry never imply unmanaged scope", async () => {
  for (const value of [null, false, "unmanaged", {}, ["managed"]]) {
    const fixture = host([{ ...session("scope-invalid"), metadata: { [SCOPE_KEY]: value } }])
    const plugin = await fixture.plugin()
    await expect(plugin["tool.execute.before"](task("scope-invalid"), { args: args() })).rejects.toThrow("managed Task scope")
  }
  for (const parentID of ["scope-cycle", "scope-missing"]) {
    const fixture = host([{ ...session("scope-cycle"), parentID }])
    const plugin = await fixture.plugin()
    await expect(plugin["tool.execute.before"](task("scope-cycle"), { args: args() })).rejects.toThrow()
  }
})

test("malformed host session documents cannot confer unmanaged scope", async () => {
  for (const data of [null, [], { id: "another-session" }, { id: "scope-shape", metadata: null }, { id: "scope-shape", metadata: [] }, { id: "scope-shape", parentID: null }, { id: "scope-shape", parentID: "" }]) {
    const plugin = await ConcordAdapterPlugin({ client: { _client: {
      get: async () => ({ data, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("scope reads must not mutate the host") },
    } } as never })
    const output = { args: args() }
    const original = structuredClone(output.args)
    await expect(plugin["tool.execute.before"](task("scope-shape"), output)).rejects.toThrow("managed Task scope")
    expect(output.args).toEqual(original)
  }
})

test("unavailable resume-target scope is not treated as an absent target", async () => {
  const plugin = await ConcordAdapterPlugin({ client: { _client: {
    get: async ({ path }: { path?: Record<string, unknown> }) => path?.id === "scope-caller"
      ? { data: session("scope-caller"), response: new Response(null, { status: 200 }) }
      : { response: new Response(null, { status: 503 }) },
    post: async () => { throw new Error("scope reads must not mutate the host") },
  } } as never })
  const output = { args: args() }
  await expect(plugin["tool.execute.before"](task("scope-caller"), output)).rejects.toThrow("503")
  expect(output.args).toEqual(args())
})

test("unavailable scope fails closed only for Task calls", async () => {
  const fixture = host([])
  const plugin = await fixture.plugin()
  await expect(plugin["tool.execute.before"](task("scope-absent"), { args: args() })).rejects.toThrow()
  fixture.reads.length = 0
  const output = { args: { filePath: "/synthetic/source" } }
  await plugin["tool.execute.before"]({ tool: "read", sessionID: "scope-absent", callID: "read-call" }, output)
  expect(fixture.reads).toEqual([])
  expect(output.args.filePath).toBe("/synthetic/source")
})

test("explicit enrollment persists only participation and preserves unrelated metadata", async () => {
  const fixture = host([{ ...session("scope-enroll"), metadata: { another_plugin: { value: 7 } } }])
  await fixture.plugin()
  await hostControlPlane().manageSession("scope-enroll")
  expect(fixture.writes).toEqual([{ url: SESSION_ROUTE, path: { id: "scope-enroll" }, body: { metadata: { another_plugin: { value: 7 }, [SCOPE_KEY]: "managed" } } }])
  expect(await hostControlPlane().taskScope("scope-enroll")).toBe("managed")
  await hostControlPlane().manageSession("scope-enroll")
  expect(fixture.writes).toHaveLength(1)
})

test("an acknowledged enrollment must also pass persisted readback", async () => {
  const fixture = host([session("scope-unpersisted")], false)
  await fixture.plugin()
  await expect(hostControlPlane().manageSession("scope-unpersisted")).rejects.toThrow("persist")
  expect(await hostControlPlane().taskScope("scope-unpersisted")).toBe("unmanaged")
})

test("an interrupted enrollment retains any participation the host recorded", async () => {
  const fixture = host([session("scope-interrupted")])
  const patch = fixture.client.patch
  fixture.client.patch = async (request) => {
    await patch(request)
    throw new Error("response transport failed after persistence")
  }
  const plugin = await fixture.plugin()
  await expect(hostControlPlane().manageSession("scope-interrupted")).rejects.toThrow("participation may have been recorded")
  await expect(plugin["tool.execute.before"](task("scope-interrupted"), { args: args() })).rejects.toThrow("no authorized dispatch window")
  await hostControlPlane().manageSession("scope-interrupted")
  expect(fixture.writes).toHaveLength(1)
})

test("an aborted enrollment and an unsupported update route write nothing", async () => {
  const fixture = host([session("scope-aborted")])
  await fixture.plugin()
  const abort = new AbortController()
  abort.abort()
  await expect(hostControlPlane().manageSession("scope-aborted", abort.signal)).rejects.toThrow()
  expect(fixture.writes).toEqual([])
  hostControlPlane().bind({ get: fixture.client.get, post: fixture.client.post })
  await expect(hostControlPlane().manageSession("scope-aborted")).rejects.toThrow("metadata update")
  expect(fixture.writes).toEqual([])
})
