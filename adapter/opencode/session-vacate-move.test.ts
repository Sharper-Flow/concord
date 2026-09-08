import { afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { moveSessionToRegisteredMainCheckout } from "./concord"

const context = () => ({
  sessionID: "session-1",
  messageID: "message-1",
  abort: new AbortController().signal,
  directory: "/worktree",
}) as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]

const args = (input: Record<string, unknown>) => ({ operation: "session_vacate", input }) as Parameters<typeof moveSessionToRegisteredMainCheckout>[0]
const okEnvelope = () => ({
  schema_version: "1.0",
  outcome: "ok",
  result: { destination_directory: "/main" },
}) as Parameters<typeof moveSessionToRegisteredMainCheckout>[2]

async function fakeHost(post: (body: any) => { status: number; body: any }, get: () => { status: number; body: any }) {
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async (request: any) => {
          const result = post(request.body)
          return { data: result.body, response: new Response(null, { status: result.status }) }
        },
        get: async () => {
          const result = get()
          return { data: result.body, response: new Response(null, { status: result.status }) }
        },
      },
    } as never,
    serverUrl: new URL("http://127.0.0.1:4096"),
  })
}

afterEach(async () => {
  await ConcordAdapterPlugin({})
})

describe("session_vacate moves only to the core-derived checkout", () => {
  test("moves and verifies the registered destination", async () => {
    let moved = ""
    await fakeHost((body) => {
      moved = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: "/main" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-1" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(moved).toBe("/main")
  })

  test("refuses an agent-named destination before the move", async () => {
    let calls = 0
    await fakeHost(() => {
      calls++
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: "/main" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-2", destination: "/other" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    expect(calls).toBe(0)
    if (envelope.outcome === "error") expect((envelope.error as { adapter_reason?: string }).adapter_reason).toBe("agent_named_destination")
  })

  test("refuses a landing mismatch", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/other" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-3" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") expect((envelope.error as { adapter_reason?: string }).adapter_reason).toBe("vacate_destination_mismatch")
  })
})
