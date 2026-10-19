import { expect, test } from "bun:test"
import { HostControlPlane, SESSION_MESSAGES_ROUTE } from "./move-session"

const task = { agent: "concord-research", description: "Research one bounded question", prompt: '{"approved":"packet"}' }
const contextPart = { id: "prt_ciwatch-fixture", type: "text", text: "CI wait success", synthetic: true }

test("a native subtask preserves the parent's host-selected model without assigning a worker model", async () => {
  const host = new HostControlPlane()
  const requests: unknown[] = []
  const signal = new AbortController().signal
  host.bind({
    async get(request) {
      expect(request.path?.id).toBe("parent")
      return { data: { id: "parent", agent: "Concord", model: { providerID: "user-provider", id: "selected-model", variant: "selected-variant" } }, response: new Response(null, { status: 200 }) }
    },
    async post(request) {
      requests.push(request)
      return {
        data: { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [{ id: "part-queued", type: "subtask", ...task }] },
        response: new Response(null, { status: 200 }),
      }
    },
  })
  await host.queueSubtask("parent", "Concord", task, signal)
  expect(requests).toEqual([{
    url: SESSION_MESSAGES_ROUTE,
    path: { id: "parent" },
    body: { agent: "Concord", model: { providerID: "user-provider", modelID: "selected-model" }, variant: "selected-variant", noReply: true, parts: [{ type: "subtask", ...task }] },
    signal,
  }])
})

test.each([
  { parts: [contextPart, { type: "subtask", ...task }] },
  { parts: [{ type: "subtask", ...task }, contextPart] },
])("a native subtask acknowledgment allows host-added context in either order", async ({ parts }) => {
  const host = new HostControlPlane()
  host.bind({
    async get() { return { data: { id: "parent", agent: "Concord" }, response: new Response(null, { status: 200 }) } },
    async post() { return { data: { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts }, response: new Response(null, { status: 200 }) } },
  })
  await host.queueSubtask("parent", "Concord", task)
})

test.each([
  { info: { id: "message-queued", sessionID: "another-parent", agent: "Concord" }, parts: [{ type: "subtask", ...task }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "another-agent" }, parts: [{ type: "subtask", ...task }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [contextPart] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [{ type: "subtask", ...task }, { type: "subtask", ...task }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [contextPart, { type: "subtask", ...task }, { type: "subtask", ...task, prompt: "another packet" }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [{ type: "subtask", ...task, prompt: "different packet" }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [contextPart, { type: "subtask", ...task, prompt: "different packet" }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [contextPart, { type: "subtask", ...task, agent: "concord-implement" }] },
  { info: { id: "message-queued", sessionID: "parent", agent: "Concord" }, parts: [contextPart, { type: "subtask", ...task, description: "Different task" }] },
])("a host acknowledgment must retain the intended parent and exact native subtask", async data => {
  const host = new HostControlPlane()
  host.bind({
    async get() { return { data: { id: "parent", agent: "Concord" }, response: new Response(null, { status: 200 }) } },
    async post() { return { data, response: new Response(null, { status: 200 }) } },
  })
  await expect(host.queueSubtask("parent", "Concord", task, new AbortController().signal)).rejects.toThrow("native subtask")
})

test.each([null, { providerID: "user-provider" }, { id: "selected-model" }, { providerID: "", id: "selected-model" }])("an invalid host model selection cannot fall back to a different choice", async model => {
  const host = new HostControlPlane()
  let posts = 0
  host.bind({
    async get() { return { data: { id: "parent", agent: "Concord", model }, response: new Response(null, { status: 200 }) } },
    async post() { posts++; throw new Error("an invalid host selection cannot queue work") },
  })
  await expect(host.queueSubtask("parent", "Concord", task)).rejects.toThrow("host model selection")
  expect(posts).toBe(0)
})
