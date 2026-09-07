// A claimed worktree is the session's worktree only when the session runs in
// it. These tests hold the claim hook to that contract: the move runs after a
// successful claim, the landing is read back from the host, and every failure
// mode is a typed refusal whose remedy is an idempotent replay (issue #822).
import { afterEach, afterAll, describe, expect, test } from "bun:test"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { hostControlPlane } from "./move-session"
import { moveSessionToClaimedWorktree } from "./concord"

const context = (overrides: Partial<Parameters<typeof moveSessionToClaimedWorktree>[1]> = {}) =>
  ({ sessionID: "session-1", messageID: "message-1", abort: new AbortController().signal, directory: "/old", ...overrides }) as Parameters<typeof moveSessionToClaimedWorktree>[1]

const claimArgs = (path: string) => ({ operation: "worktree_claim", input: { work_id: "work-1", path } }) as Parameters<typeof moveSessionToClaimedWorktree>[0]

const okEnvelope = () => ({ schema_version: "1.0", outcome: "ok" }) as Parameters<typeof moveSessionToClaimedWorktree>[2]

async function fakeHost(handlers: { post?: (url: string, body: any) => { status: number; body: any }; get?: (url: string) => { status: number; body: any } }) {
  const raw = {
    post: async (_url: string, body: any) => {
      const result = handlers.post?.(_url, body) ?? { status: 204, body: null }
      return { data: result.body, response: new Response(null, { status: result.status }) }
    },
    get: async (_url: string) => {
      const result = handlers.get?.(_url) ?? { status: 200, body: { directory: "/claimed" } }
      return { data: result.body, response: new Response(null, { status: result.status }) }
    },
  }
  await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
}

afterEach(async () => {
  await ConcordAdapterPlugin({})
})

describe("worktree_claim moves the session into the claimed worktree", () => {
  test("returns the successful envelope when the session lands in the claimed path", async () => {
    await fakeHost({})
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
  })

  test("refuses when the host lands the session elsewhere", async () => {
    await fakeHost({ get: () => ({ status: 200, body: { directory: "/somewhere-else" } }) })
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; recovery_action?: { kind?: string }; message?: string }
      expect(error.adapter_reason).toBe("claim_move_destination_mismatch")
      expect(error.recovery_action).toEqual({ kind: "retry_same_request" })
      expect(error.message).toContain("/claimed")
    }
  })

  test("refuses with the host's own words when the move is rejected", async () => {
    await fakeHost({ post: () => ({ status: 409, body: { data: { message: "worktree /claimed is held by another session" } } }) })
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; message?: string }
      expect(error.adapter_reason).toBe("claim_move_refused")
      expect(error.message).toContain("held by another session")
      expect(error.message).toContain("replay worktree_claim")
    }
  })

  test("leaves non-claim operations and refused claims untouched", async () => {
    await fakeHost({})
    const other = { operation: "lifecycle", input: { work_id: "work-1" } } as Parameters<typeof moveSessionToClaimedWorktree>[0]
    expect(await moveSessionToClaimedWorktree(other, context(), okEnvelope())).toEqual(okEnvelope())
    const refused = { schema_version: "1.0", outcome: "error", error: { kind: "version_conflict" } } as unknown as Parameters<typeof moveSessionToClaimedWorktree>[2]
    expect(await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), refused)).toEqual(refused)
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
