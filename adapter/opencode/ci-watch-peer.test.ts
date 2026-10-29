import { afterEach, beforeEach, expect, spyOn, test } from "bun:test"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { bindCiWatchClient, ciWatchSettled, ciWatchTool, configureCiWatch, drainQueuedCiReports, registerCiWatchPeer, type CiWatchSnapshot } from "./ci-watch"
import { configureCoreBinary } from "./core-runtime"
import { hostControlPlane } from "./move-session"

const KEY = Symbol.for("concord.ci-watch.peer.v1")
const globals = globalThis as typeof globalThis & { [KEY]?: Peer }
type PeerRead = { coverage: string; gaps: string[]; snapshot: CiWatchSnapshot | null; runtime_epoch: string; binding_generation: number; client_bound: boolean; bindings: Array<{ id: string; directory: string }> }
type Peer = { version: number; read: (request: unknown) => Promise<PeerRead> }
const SESSION = "peer-session"
const SHA = "0123456789abcdef0123456789abcdef01234567"
const temporary: string[] = []
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
function fixture(endpoint = "http://peer-a.invalid:1234/", directory = "/synthetic/project") {
  const calls = { reads: 0, posts: 0, spawns: 0 }
  let lookup: () => Promise<{ data?: unknown; response: Response }> = async () => ({ data: { id: SESSION }, response: new Response(null, { status: 200 }) })
  const raw = {
    async get(request: { url: string; path?: Record<string, unknown> }) {
      if (request.url === "/session/{id}") { calls.reads++; return lookup() }
      return { data: {}, response: new Response(null, { status: 200 }) }
    },
    async post(request: { url: string }) { calls.posts++; return { response: new Response(null, { status: request.url === "/session/{id}/prompt_async" ? 400 : 200 }) } },
  }
  const input = { serverUrl: new URL(endpoint), directory, worktree: directory, client: { _client: raw } }
  bindCiWatchClient(input)
  hostControlPlane().bind(input)
  configureCoreBinary("/synthetic/concord")
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "ci-peer-"))
  temporary.push(stateDir)
  configureCiWatch({ stateDir, idleTimeoutMs: 5, idlePollMs: 1, confirmPollMs: 1, confirmWindowMs: 5 })
  configureCiWatch({ spawner: async () => { calls.spawns++; return { exitCode: 0, stdout: JSON.stringify({ status: "error" }), stderr: "" } } })
  return { input, calls, lookup: (value: typeof lookup) => { lookup = value } }
}
function peer(): Peer { return globals[KEY]! }
const request = { session_id: SESSION }
function unknown(result: PeerRead, gap?: string) {
  expect(result.coverage).toBe("unknown")
  if (gap) expect(result.gaps).toContain(gap)
}
beforeEach(() => {
  configureCiWatch({ reset: true })
  delete globals[KEY]
})
afterEach(() => {
  configureCiWatch({ reset: true })
  configureCoreBinary(null)
  hostControlPlane().bind(undefined)
  delete globals[KEY]
  for (const directory of temporary.splice(0)) fs.rmSync(directory, { recursive: true, force: true })
})

test("the peer publishes from registration and positively proves only current-epoch session absence", async () => {
  expect(globals[KEY]).toBeUndefined()
  const f = fixture()
  const binding = registerCiWatchPeer(f.input)
  const port = peer()
  expect(port.version).toBe(1)
  const result = await port.read(request)
  expect(result.coverage).toBe("complete_current_epoch")
  expect(result.gaps).toContain("prior_epoch_unverified")
  expect(result.client_bound).toBe(true)
  expect(result.snapshot!.active).toEqual([])
  expect(result.bindings.map((entry) => entry.id)).toContain(binding.id)
  result.bindings[0]!.directory = "corrupted"
  expect((await port.read(request)).bindings[0]!.directory).toBe(f.input.directory)
  expect(f.calls.posts).toBe(0)
  expect(f.calls.spawns).toBe(0)
})

test("invalid requests and epoch mismatches refuse before any session lookup", async () => {
  const f = fixture()
  registerCiWatchPeer(f.input)
  for (const bad of [null, [], {}, { ...request, extra: true }, { session_id: " " }, { session_id: "s".repeat(257) }, { ...request, since_sequence: -1 }, { ...request, since_sequence: 0.5 }, { ...request, since_sequence: Number.MAX_SAFE_INTEGER + 1 }, { ...request, expected_runtime_epoch: 1 }]) {
    unknown(await peer().read(bad), "invalid_request")
  }
  unknown(await peer().read({ ...request, expected_runtime_epoch: "different-epoch" }), "epoch_mismatch")
  expect(f.calls.reads).toBe(0)
})

test("missing, mismatched, unreachable and unbound sessions cannot prove an empty active set", async () => {
  const f = fixture()
  registerCiWatchPeer(f.input)
  f.lookup(async () => ({ response: new Response(null, { status: 404 }) }))
  unknown(await peer().read(request), "session_missing")
  f.lookup(async () => ({ data: { id: "different-session" }, response: new Response(null, { status: 200 }) }))
  unknown(await peer().read(request), "session_identity_mismatch")
  f.lookup(async () => { throw new Error("synthetic host failure") })
  unknown(await peer().read(request), "session_unreachable")
  f.lookup(async () => ({ data: { id: SESSION }, response: new Response(null, { status: 200 }) }))
  bindCiWatchClient(undefined)
  const unbound = await peer().read(request)
  unknown(unbound, "client_unbound")
  expect(unbound.client_bound).toBe(false)
})

test("same-endpoint instance directories coexist but differing matching endpoints are ambiguous", async () => {
  const first = fixture()
  registerCiWatchPeer(first.input)
  const second = fixture(undefined, "/synthetic/moved")
  registerCiWatchPeer(second.input)
  const moved = await peer().read(request)
  expect(moved.coverage).toBe("complete_current_epoch")
  expect(moved.bindings).toHaveLength(2)
  const third = fixture("http://peer-a.invalid:4321/")
  registerCiWatchPeer(third.input)
  unknown(await peer().read(request), "ambiguous_host")
})

test("a binding change during lookup and a cached reader after reset stay unknown", async () => {
  const f = fixture()
  const binding = registerCiWatchPeer(f.input)
  const cached = peer()
  const gate = deferred<{ data: unknown; response: Response }>()
  f.lookup(() => gate.promise)
  const reading = cached.read(request)
  binding.dispose()
  gate.resolve({ data: { id: SESSION }, response: new Response(null, { status: 200 }) })
  unknown(await reading, "binding_changed")
  unknown(await cached.read(request))
  configureCiWatch({ reset: true })
  const next = fixture()
  registerCiWatchPeer(next.input)
  unknown(await cached.read(request))
  const result = await peer().read(request)
  expect(result.coverage).toBe("complete_current_epoch")
})

test("qualified and unqualified accepted watches are never filtered out of the session set", async () => {
  const f = fixture()
  const binding = registerCiWatchPeer(f.input)
  const held = [deferred<{ exitCode: number; stdout: string; stderr: string }>(), deferred<{ exitCode: number; stdout: string; stderr: string }>()]
  let spawned = 0
  configureCiWatch({ spawner: async () => { f.calls.spawns++; return held[spawned++]!.promise } })
  const context = { sessionID: SESSION }
  const first = JSON.parse((await ciWatchTool(binding.id).execute({ repo: "owner/name", selector: { kind: "pr", value: "1" } }, context)).output)
  const second = JSON.parse((await ciWatchTool().execute({ repo: "owner/name", selector: { kind: "pr", value: "2" } }, context)).output)
  const result = await peer().read(request)
  unknown(result, "unqualified_watch")
  expect(result.snapshot!.active).toHaveLength(2)
  expect(result.snapshot!.active.find((watch) => watch.watch_id === first.watch_id)!.binding_id).toBe(binding.id)
  await peer().read(request)
  expect(f.calls.spawns).toBe(2)
  binding.dispose()
  unknown(await peer().read(request))
  for (const gate of held) gate.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "error" }), stderr: "" })
  await ciWatchSettled(first.watch_id)
  await ciWatchSettled(second.watch_id)
  drainQueuedCiReports(SESSION, "peer-drain", { parts: [] })
})

test("conflicting module owners invalidate both current and cached readers", async () => {
  const f = fixture()
  registerCiWatchPeer(f.input)
  const cached = peer()
  const modulePath = "./ci-watch.ts?peer-owner-copy"
  const copy = await import(modulePath)
  copy.bindCiWatchClient(f.input)
  copy.registerCiWatchPeer(f.input)
  unknown(await cached.read(request), "owner_conflict")
  const conflicted = await peer().read(request)
  unknown(conflicted, "owner_conflict")
  expect(conflicted.client_bound).toBe(true)
  expect(conflicted.gaps).toContain("prior_epoch_unverified")
  copy.configureCiWatch({ reset: true })
})

test("a read-only foreign symbol cannot prevent the existing watch tool from running", async () => {
  const f = fixture()
  Object.defineProperty(globals, KEY, { value: {}, configurable: true, writable: false })
  let binding: { id: string } | undefined
  expect(() => { binding = registerCiWatchPeer(f.input) }).not.toThrow()
  const started = JSON.parse((await ciWatchTool(binding!.id).execute({ repo: "owner/name", selector: { kind: "pr", value: "1" } }, { sessionID: SESSION })).output)
  expect(started.status).toBe("started")
  await ciWatchSettled(started.watch_id)
  drainQueuedCiReports(SESSION, "peer-foreign-drain", { parts: [] })
})

test("every qualified queued watch remains in the complete set across an instance move", async () => {
  const f = fixture()
  const original = registerCiWatchPeer(f.input)
  for (const value of ["1", "2"]) {
    const started = JSON.parse((await ciWatchTool(original.id).execute({ repo: "owner/name", selector: { kind: "pr", value } }, { sessionID: SESSION })).output)
    await ciWatchSettled(started.watch_id)
  }
  registerCiWatchPeer({ ...f.input, directory: "/synthetic/moved" })
  const moved = await peer().read(request)
  expect(moved.coverage).toBe("complete_current_epoch")
  expect(moved.snapshot!.active).toHaveLength(2)
  expect(moved.snapshot!.active.every((watch) => watch.phase === "queued" && watch.binding_id === original.id)).toBe(true)
  original.dispose()
  unknown(await peer().read(request), "disposed_watch")
  drainQueuedCiReports(SESSION, "peer-moved-drain", { parts: [] })
})

test("a response over the fixed byte bound returns unknown, never a truncated complete set", async () => {
  const f = fixture(undefined, "/" + "d".repeat(4095))
  for (let index = 0; index < 64; index++) registerCiWatchPeer(f.input)
  const result = await peer().read(request)
  unknown(result, "response_limit")
  expect(result.snapshot).toBeNull()
  expect(Buffer.byteLength(JSON.stringify(result), "utf8")).toBeLessThanOrEqual(256 * 1024)
})

test("binding-slot overflow is an explicit coverage gap", async () => {
  const f = fixture()
  for (let index = 0; index < 65; index++) registerCiWatchPeer(f.input)
  unknown(await peer().read(request), "binding_limit")
})

test("a watch from a different host cannot acquire coverage from a same-named session", async () => {
  const old = fixture()
  const binding = registerCiWatchPeer(old.input)
  const started = JSON.parse((await ciWatchTool(binding.id).execute({ repo: "owner/name", selector: { kind: "pr", value: "1" } }, { sessionID: SESSION })).output)
  await ciWatchSettled(started.watch_id)
  old.lookup(async () => ({ response: new Response(null, { status: 404 }) }))
  const other = fixture("http://peer-a.invalid:4321/")
  registerCiWatchPeer(other.input)
  const result = await peer().read(request)
  unknown(result, "watch_host_mismatch")
  expect(result.snapshot!.active).toHaveLength(1)
  drainQueuedCiReports(SESSION, "peer-wrong-host-drain", { parts: [] })
})

test("an owner read before its epoch's wall timestamp reports a clock gap even when empty", async () => {
  const f = fixture()
  registerCiWatchPeer(f.input)
  const result = await peer().read(request)
  const clock = spyOn(Date, "now").mockReturnValue(result.snapshot!.owner_started_at - 1)
  try {
    unknown(await peer().read(request), "clock_gap")
  } finally {
    clock.mockRestore()
  }
})

test("the peer preserves terminal subject identity and never renews its freshness", async () => {
  const f = fixture()
  const binding = registerCiWatchPeer(f.input)
  configureCiWatch({ spawner: async () => ({ exitCode: 0, stdout: JSON.stringify({ status: "success", sha: SHA, head_sha: SHA, checks: { total: 1, passing: 1, failing: 0, pending: 0, skipped: 0 } }), stderr: "" }) })
  const started = JSON.parse((await ciWatchTool(binding.id).execute({ repo: "owner/name", selector: { kind: "pr", value: "1" } }, { sessionID: SESSION })).output)
  await ciWatchSettled(started.watch_id)
  const receipt = (await peer().read(request)).snapshot!.receipts[0]!
  expect(receipt.terminal_observation).toMatchObject({ sha: SHA, evidence: "exact_head", stale: false })
  const clock = spyOn(Date, "now").mockReturnValue(receipt.terminal_observation.slice_started_at + 180_001)
  try {
    const stale = (await peer().read(request)).snapshot!.receipts[0]!.terminal_observation
    expect(stale).toMatchObject({ sha: SHA, evidence: "unavailable", stale: true, slice_started_at: receipt.terminal_observation.slice_started_at })
    clock.mockReturnValue(receipt.terminal_observation.report_received_at - 1)
    expect((await peer().read(request)).snapshot!.receipts[0]!.terminal_observation.gaps).toContain("clock_gap")
  } finally {
    clock.mockRestore()
    drainQueuedCiReports(SESSION, "peer-terminal-drain", { parts: [] })
  }
})
