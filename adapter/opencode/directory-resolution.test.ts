import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter } from "./concord"
import { configureCoreBinary } from "./core-runtime"
import { configureHostLease } from "./host-lease"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { validateGeneratedEnvelope } from "./generated-contract-tests"
import { fakeV2Context } from "./v2-context.fixture"

afterEach(() => {
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  configureCoreBinary(null)
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

test("ordinary tool entries resolve the live directory on every call", async () => {
  configureLease()
  let directory = "/host/first"
  const reads: unknown[] = []
  const abort = new AbortController().signal
  const host = fakeV2Context(async (request) => {
    reads.push(request)
    return { location: { directory } }
  })
  await ConcordAdapterPlugin.setup(host.context)
  const calls: Array<{ argv: string[]; body: any }> = []
  configureConcordAdapter({ runner: { async run(argv, input) {
    const body = JSON.parse(input)
    calls.push({ argv, body })
    if (argv[1] === "project-resolve") return { exitCode: 0, stderr: "", stdout: JSON.stringify({ project_id: `project-${directory.split("/").at(-1)}`, product_ids: ["product-test"], scope_version: "scope-test", main_worktree: false }) }
    expect(argv[1]).toBe("invoke")
    return { exitCode: 0, stderr: "", stdout: JSON.stringify({
      schema_version: "1.0", manifest_digest: manifestDigest,
      origin: "core", tool: body.tool, operation: body.operation, outcome: "error", next_cursor: null,
      omissions: [], warnings: [],
      error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" },
    }) }
  } } })
  let index = 0
  for (const tool of host.tools) {
    const toolName = tool.name
    directory = `/host/call-${index++}`
    const operation = contractOperations.find(candidate => candidate.tool === toolName)!.id.split(".")[1]
    const result = await tool.execute({ request: { operation, input: {} } }, {
      sessionID: "session-directory", messageID: `message-${index}`, agent: "Concord", signal: abort,
    })
    const envelope = JSON.parse(result.content)
    expect(envelope.origin, JSON.stringify(envelope)).toBe("core")
    expect(calls.at(-2)?.body, toolName).toEqual({ directory, worktree: directory })
    expect(calls.at(-1)?.body.call_envelope, toolName).toMatchObject({
      directory, worktree: directory, ambient_project_id: `project-call-${index - 1}`,
      selected_product_id: "product-test", scope_version: "scope-test", manifest_digest: manifestDigest,
      session_ref: "session-directory", agent_ref: "Concord",
    })
  }
  expect(index).toBe(7)
  expect(calls).toHaveLength(14)
  expect(reads).toEqual(Array.from({ length: 7 }, () => ({ sessionID: "session-directory" })))
})

for (const [name, get, message] of [
  ["missing directory", async () => ({ location: {} }), "missing"],
  ["transport exception", async () => { throw new Error("host disconnected") }, "host disconnected"],
] as Array<[string, () => Promise<any>, string]>) {
  test(`ordinary directory read fails closed with useful diagnostics: ${name}`, async () => {
    configureLease()
    const host = fakeV2Context(get)
    await ConcordAdapterPlugin.setup(host.context)
    let calls = 0
    configureConcordAdapter({ runner: { async run() { calls++; throw new Error("failed host reads must not invoke core") } } })
    const raw = await host.tools.find(tool => tool.name === "concord_product_view").execute({ request: { operation: "resolve", input: {} } }, {
      sessionID: "session-refused", messageID: "message-refused", agent: "Concord", signal: new AbortController().signal,
    })
    const result = JSON.parse(raw.content)
    expect(result.error).toMatchObject({ kind: "transport_failure", adapter_reason: "session_directory_unreadable", effect_state: "none", retry_safe: true, recovery_action: { kind: "retry_same_request" } })
    expect(result.error.message).toContain(message)
    expect(validateGeneratedEnvelope(result)).toBe(true)
    expect(calls).toBe(0)
  })
}
