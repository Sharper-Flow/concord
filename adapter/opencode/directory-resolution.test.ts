import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter } from "./concord"
import { configureCoreBinary } from "./core-runtime"
import { configureHostLease } from "./host-lease"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope } from "./generated-contract-tests"
import { sessionDirectoryReader, type RouteResult } from "./session-directory"

afterEach(() => {
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  configureCoreBinary(null)
  sessionDirectoryReader().bind(undefined)
})

function configureLease() {
  configureCoreBinary("concord")
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/test" },
    runner: { async run() {
      return { exitCode: 0, stderr: "", stdout: JSON.stringify({ release_root: "/releases/test", core_binary: "concord", manifest_digest: manifestDigest }) }
    } },
  })
}

test("ordinary tool entries resolve the live directory on every call with a GET-only bound client", async () => {
  configureLease()
  let directory = "/host/first"
  const reads: unknown[] = []
  const abort = new AbortController().signal
  const plugin = await ConcordAdapterPlugin({
    directory: "/stale/plugin", worktree: "/stale/plugin",
    client: { _client: { get: async (request: unknown) => {
      reads.push(request)
      return { data: { directory }, response: new Response(null, { status: 200 }) }
    } } } as never,
    serverUrl: new URL("https://example.invalid"),
  })
  const calls: Array<{ argv: string[]; body: any }> = []
  configureConcordAdapter({ runner: { async run(argv, input) {
    const body = JSON.parse(input)
    calls.push({ argv, body })
    if (argv[1] === "project-resolve") return { exitCode: 0, stderr: "", stdout: JSON.stringify({ project_id: `project-${directory.split("/").at(-1)}`, product_ids: ["product-test"], scope_version: "scope-test", main_worktree: false }) }
    expect(argv[1]).toBe("invoke")
    const operation = contractOperations.find(candidate => candidate.id === `${body.tool}.${body.operation}`)
    const queryID = operation && "query_id" in operation ? operation.query_id : undefined
    return { exitCode: 0, stderr: "", stdout: JSON.stringify({
      schema_version: "1.0", manifest_digest: manifestDigest, request_id: body.call_envelope.request_id,
      origin: "core", tool: body.tool, operation: body.operation, ...(queryID ? { query_id: queryID } : {}), outcome: "error", resolved_scope: null,
      authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null,
      omissions: [], warnings: [], evidence_refs: [], replayed: false,
      error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
    }) }
  } } })
  let index = 0
  for (const [toolName, tool] of Object.entries(plugin.tool)) {
    directory = `/host/call-${index++}`
    const operation = contractOperations.find(candidate => candidate.tool === toolName)!.id.split(".")[1]
    const result = await tool.execute({ request: { operation, input: {} } }, {
      sessionID: "session-directory", messageID: `message-${index}`, agent: "Concord",
      directory: "/stale/context", worktree: "/stale/context", abort, metadata: () => {}, ask: async () => {},
    })
    const envelope = JSON.parse(typeof result === "string" ? result : result.output)
    expect(envelope.origin, JSON.stringify(envelope)).toBe("core")
    expect(calls.at(-2)?.body, toolName).toEqual({ directory, worktree: directory })
    expect(calls.at(-1)?.body.call_envelope, toolName).toMatchObject({
      directory, worktree: directory, ambient_project_id: `project-call-${index - 1}`,
      selected_product_id: "product-test", scope_version: "scope-test", manifest_digest: manifestDigest,
      session_ref: "session-directory", agent_ref: "Concord",
    })
  }
  expect(index).toBe(8)
  expect(calls).toHaveLength(16)
  expect(reads).toEqual(Array.from({ length: 8 }, () => ({ url: "/session/{id}", path: { id: "session-directory" }, signal: abort })))
})

const consumed = async (data: unknown): Promise<RouteResult> => {
  const response = new Response(JSON.stringify(data), { status: 403 })
  await response.text()
  return { data, response }
}
for (const [name, get, message] of [
  ["parsed refusal", () => consumed({ message: "host access denied" }), "host access denied"],
  ["nested parsed refusal", () => consumed({ data: { message: "nested refusal" } }), "nested refusal"],
  ["text refusal", async () => ({ response: new Response("host unavailable", { status: 503 }) }), "host unavailable"],
  ["empty refusal", async () => ({ response: new Response(null, { status: 500 }) }), "no readable refusal"],
  ["missing directory", async () => ({ data: { directory: "" }, response: new Response(null, { status: 200 }) }), "no directory"],
  ["transport exception", async () => { throw new Error("host disconnected") }, "host disconnected"],
] as Array<[string, () => Promise<RouteResult>, string]>) {
  test(`ordinary directory read fails closed with useful diagnostics: ${name}`, async () => {
    configureLease()
    const plugin = await ConcordAdapterPlugin({ directory: "/not-a-fallback", client: { _client: { get } } as never })
    let calls = 0
    configureConcordAdapter({ runner: { async run() { calls++; throw new Error("failed host reads must not invoke core") } } })
    const raw = await plugin.tool.concord_product_view.execute({ request: { operation: "resolve", input: {} } }, {
      sessionID: "session-refused", messageID: "message-refused", agent: "Concord", directory: "/not-a-fallback",
      worktree: "/not-a-fallback", abort: new AbortController().signal, metadata: () => {}, ask: async () => {},
    })
    const result = JSON.parse(typeof raw === "string" ? raw : raw.output)
    expect(result.error).toMatchObject({ kind: "transport_failure", adapter_reason: "session_directory_unreadable", effect_state: "none", retry_safe: true, recovery_action: { kind: "retry_same_request" } })
    expect(result.error.message).toContain(message)
    expect(validateGeneratedEnvelope(result)).toBe(true)
    expect(calls).toBe(0)
  })
}
