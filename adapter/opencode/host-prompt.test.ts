import { expect, test } from "bun:test"
import { promptAndConfirm, type HostPromptClient, type SessionMessage } from "./host-prompt"

const sessionID = "session-prompt"
const text = "Synthetic continuation fixture"
const timing = { confirmWindowMs: 2, confirmPollMs: 1 }
const metadata = { "fixture.marker": "fixture-token" }

function fixture(mode: "delivered" | "missing-route" | "rejected" | "unpersisted" | "unanswered" | "placeholder" | "wrong-parent" | "assistant-error") {
  const posts: unknown[] = []
  let reads = 0
  const messages: SessionMessage[] = [{ info: { role: "user", id: "injected" }, parts: [{ type: "text", text }] }]
  if (mode !== "unanswered") messages.push({
    info: { role: "assistant", id: "reply", parentID: mode === "wrong-parent" ? "other-user" : "injected", ...(mode === "assistant-error" ? { error: { name: "MessageAbortedError" } } : {}) },
    parts: mode === "placeholder" ? [] : [{ type: "text", text: "Continuing the original request" }],
  })
  const client: HostPromptClient = {
    async get({ url }) {
      if (url === "/session/{id}") return { response: new Response(null, { status: 200 }), data: { agent: "concord-1", model: { providerID: "test", id: "test-model", variant: "fixture" } } }
      reads++
      return { response: new Response(null, { status: 200 }), data: mode === "unpersisted" ? [] : messages }
    },
    async post(request) {
      posts.push(request)
      return { response: new Response(null, { status: mode === "missing-route" ? 404 : mode === "rejected" ? 503 : 204 }) }
    },
  }
  return { client, posts, reads: () => reads }
}

test("native prompt delivery preserves host identity and confirms parented activity", async () => {
  const host = fixture("delivered")
  expect(await promptAndConfirm(host.client, sessionID, text, timing, metadata)).toBe("delivered")
  expect(host.posts).toHaveLength(1)
  expect(host.posts[0]).toMatchObject({ url: "/session/{id}/prompt_async", path: { id: sessionID }, body: {
    agent: "concord-1", model: { providerID: "test", modelID: "test-model" }, variant: "fixture",
    parts: [{ type: "text", text, synthetic: true, metadata }],
  } })
})

test.each([["missing-route", "404"], ["rejected", "503"]] as const)("a %s prompt is distinct from an unconfirmed accepted prompt", async (mode, status) => {
  const host = fixture(mode)
  await expect(promptAndConfirm(host.client, sessionID, text, timing, metadata)).rejects.toThrow(`the host prompt_async route answered ${status}`)
  expect(host.posts).toHaveLength(1)
  expect(host.reads()).toBe(0)
})

test("HTTP 204 without persistence does not confirm delivery", async () => {
  const host = fixture("unpersisted")
  await expect(promptAndConfirm(host.client, sessionID, text, timing, metadata)).rejects.toThrow("the host never persisted the injected report message")
  expect(host.posts).toHaveLength(1)
  expect(host.reads()).toBeGreaterThan(0)
})

test.each(["unanswered", "placeholder", "wrong-parent", "assistant-error"] as const)("persisted %s delivery is not confirmed and is never resent", async mode => {
  const host = fixture(mode)
  expect(await promptAndConfirm(host.client, sessionID, text, timing, metadata)).toBe("unconfirmed")
  expect(host.posts).toHaveLength(1)
})

test("supersession at the final pre-post check prevents delivery", async () => {
  const host = fixture("delivered")
  await expect(promptAndConfirm(host.client, sessionID, text, timing, metadata, () => false)).rejects.toThrow("the host prompt was superseded before delivery")
  expect(host.posts).toHaveLength(0)
  expect(host.reads()).toBe(0)
})
