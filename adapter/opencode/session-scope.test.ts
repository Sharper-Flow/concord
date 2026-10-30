import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { hostControlPlane, SESSION_ROUTE } from "./move-session"

const SCOPE_KEY = "concord.task_scope"
type Session = { id: string; directory: string; parentID?: string; metadata?: Record<string, unknown> }
function host(records: Session[], persist = true) {
  const sessions = new Map(records.map(session => [session.id, structuredClone(session)]))
  const reads: string[] = []
  const writes: Array<{ url: string; path?: Record<string, unknown>; body?: unknown }> = []
  const client = {
    get: async ({ url, path, signal }: { url: string; path?: Record<string, unknown>; signal?: AbortSignal }) => {
      signal?.throwIfAborted()
      const id = String(path?.id)
      expect(url).toBe(SESSION_ROUTE)
      reads.push(id)
      return { data: structuredClone(sessions.get(id)), response: new Response(null, { status: sessions.has(id) ? 200 : 404 }) }
    },
    patch: async (request: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
      writes.push(structuredClone({ url: request.url, path: request.path, body: request.body }))
      const session = sessions.get(String(request.path?.id))
      if (!session) return { response: new Response(null, { status: 404 }) }
      if (persist) session.metadata = structuredClone((request.body as { metadata: Record<string, unknown> }).metadata)
      return { data: structuredClone(session), response: new Response(null, { status: 200 }) }
    },
    post: async () => { throw new Error("participation must not change permissions, move a session, or invoke core") },
  }
  return { sessions, reads, writes, client, plugin: () => ConcordAdapterPlugin({ directory: process.cwd(), client: { _client: client } as never }) }
}
const session = (id: string, managed = false): Session => ({ id, directory: process.cwd(), metadata: managed ? { [SCOPE_KEY]: "managed" } : {} })
const task = (sessionID: string) => ({ tool: "task", sessionID, callID: "scope-call" })
const args = () => ({ subagent_type: "general", prompt: "Read the bounded source", description: "Read source", task_id: "another-native-session" })

afterEach(() => {
  hostControlPlane().bind(undefined)
  configureHostLease({ reset: true })
})

test("native Task preserves arguments without reading or changing Concord participation", async () => {
  const fixture = host([session("ordinary"), session("managed", true), { ...session("child"), parentID: "managed" }])
  const plugin = await fixture.plugin()
  for (const id of ["ordinary", "managed", "child", "missing"]) {
    for (const subagent_type of ["general", "explore", "concord-explore", "concord-advisor", "concord-review"]) {
      const output = { args: { ...args(), subagent_type } }
      const original = structuredClone(output)
      const reads = fixture.reads.length
      await plugin["tool.execute.before"](task(id), output)
      expect(output).toEqual(original)
      expect(fixture.reads).toHaveLength(reads)
      expect(fixture.writes).toEqual([])
    }
  }
  expect(Reflect.get(plugin, "tool.execute.after")).toBeUndefined()
  expect(Object.keys(plugin.tool).some(name => name.startsWith("concord_worker_"))).toBe(false)
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
  fixture.client.patch = async request => { await patch(request); throw new Error("response transport failed after persistence") }
  const plugin = await fixture.plugin()
  await expect(hostControlPlane().manageSession("scope-interrupted")).rejects.toThrow("participation may have been recorded")
  await plugin["tool.execute.before"](task("scope-interrupted"), { args: args() })
  expect(await hostControlPlane().taskScope("scope-interrupted")).toBe("managed")
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
