// CD-0102 D2: the plugin entry must register the hook that binds an authorized
// dispatch to the next native Task call. Without the registration the window is
// unreachable and any Task call the model composes runs unbound.
import { describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { hostControlPlane, MOVE_SESSION_ROUTE, MoveSessionUnavailable } from "./move-session"

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-plugin",
  lane_id: "implement",
  lane_version: 1,
  lane_digest: "sha256:" + "b".repeat(64),
  work_id: "work-plugin",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", context: "", constraints: [] },
}

describe("plugin entry registers the dispatch window hook", () => {
  test("binds the recorded packet onto the next task call", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    expect(typeof plugin["tool.execute.before"]).toBe("function")

    dispatchWindows().open("session-plugin", packet)
    const output = { args: { subagent_type: "general", prompt: "whatever I like", task_id: "old" } }
    await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-plugin", callID: "call-1" }, output)

    expect(output.args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(output.args.prompt).attempt_id).toBe("attempt-plugin")
    expect(output.args.task_id).toBeUndefined()
    expect(dispatchWindows().has("session-plugin")).toBe(false)
  })

  test("refuses an unauthorized task call and leaves other tools untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    const output = { args: { subagent_type: "general", prompt: "unbound" } }
    await expect(
      plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-none", callID: "call-2" }, output),
    ).rejects.toThrow(/no authorized dispatch/i)

    const other = { args: { filePath: "/x" } }
    await plugin["tool.execute.before"]({ tool: "read", sessionID: "session-none", callID: "call-3" }, other)
    expect(other.args.filePath).toBe("/x")
  })

  // CD-0102 D5: the completion entry point is reachable only if the entry module
  // registers it. A dispatch with no registered completion records no attempt.
  test("registers the completion hook and leaves an unbound call untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.after": (
        i: { tool: string; sessionID: string; callID: string; args: unknown },
        o: { title: string; output: string; metadata: unknown },
      ) => Promise<void>
    }
    expect(typeof plugin["tool.execute.after"]).toBe("function")

    const output = { title: "read", output: "file contents", metadata: {} }
    await plugin["tool.execute.after"]({ tool: "read", sessionID: "session-none", callID: "call-4", args: {} }, output)
    expect(output.output).toBe("file contents")
  })
})

// CD-0098 D2. The control plane takes its transport from the client the host
// supplies, because that client dispatches in process and carries the host's
// headers. A host that binds no listener still exposes a `serverUrl`, so a
// route reached through that value cannot connect and work start refuses on
// every attempt.
describe("plugin entry binds the control plane to the host client", () => {
  test("routes the move through the client the host supplied", async () => {
    const seen: Array<{ url: string; body?: unknown }> = []
    const raw = {
      get: async () => ({ data: { directory: "/w" }, response: new Response(null, { status: 200 }) }),
      post: async ({ url, body }: { url: string; body?: unknown }) => {
        seen.push({ url, body })
        return { response: new Response(null, { status: 204 }) }
      },
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })

    await hostControlPlane().moveSession("session-1", "/w")
    expect(seen).toEqual([{ url: MOVE_SESSION_ROUTE, body: { sessionID: "session-1", destination: { directory: "/w" } } }])
    expect(await hostControlPlane().sessionDirectory("session-1")).toBe("/w")
  })

  test("refuses the route when the host supplied no client", async () => {
    await ConcordAdapterPlugin({ serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toBeInstanceOf(MoveSessionUnavailable)
    // A server URL is present and still yields no route: the URL is not what
    // the adapter needs, so its presence must not read as a usable transport.
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toThrow(/handed the plugin no client/)
  })
})

// The client parses the refusal body before it returns, so the parsed value on
// the result is the only readable copy. A refusal that reads the response a
// second time reports that the host said nothing, and the operator loses the
// one sentence that says why the move failed.
describe("a refusal carries the host's own words", () => {
  // A body the client already consumed. Reading it again throws, which is what
  // a real host response does once the SDK client has parsed it.
  const consumed = (status: number, payload: unknown): Response => {
    const response = new Response(JSON.stringify(payload), { status })
    void response.text()
    return response
  }

  test("reports the host message when the move is refused", async () => {
    const raw = {
      get: async () => ({ data: { directory: "/w" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({
        data: { data: { message: "worktree /w is held by another session" } },
        response: consumed(409, { data: { message: "worktree /w is held by another session" } }),
      }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toThrow(
      /worktree \/w is held by another session/,
    )
  })

  test("reports the host message when the session readback is refused", async () => {
    const raw = {
      get: async () => ({
        data: { message: "session-1 is unknown to this host" },
        response: consumed(404, { message: "session-1 is unknown to this host" }),
      }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(
      /session-1 is unknown to this host/,
    )
  })

  test("says so plainly when the host supplied no refusal at all", async () => {
    const raw = {
      get: async () => ({ response: new Response(null, { status: 500 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(/no readable refusal/)
  })
})
