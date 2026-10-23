import { expect, mock, spyOn, test } from "bun:test"
import * as childProcess from "node:child_process"
import { EventEmitter } from "node:events"
import { setImmediate } from "node:timers/promises"
import NativeLaneProvider, { childRunStream } from "./native-lane.mjs"
import { readWorkerReportTexts } from "../worker-report-protocol.js"

const agent = "concord-review"
const child = "child"
const message = (who = agent, finish = "stop") => ({
  info: { role: "assistant", agent: who, sessionID: child, providerID: "fixture", modelID: "actual-model", finish },
  parts: [
    { id: "text", type: "text", sessionID: "child", text: "fixture-report" },
    { type: "tool", tool: "task", state: { status: "error" } },
    { type: "step-finish", reason: finish },
  ],
})

test("native eval streams exact child parts without the parent dispatch", () => {
  const { output, metadata } = childRunStream([message()], agent, child)
  expect(readWorkerReportTexts(output).texts).toEqual(["fixture-report"])
  expect(JSON.parse(output.split("\n")[1]).part.tool).toBe("task")
  expect(metadata).toEqual({ readback_model: "fixture/actual-model", readback_agent: agent, session_id: child })
})

test("native eval refuses absent, substituted, or incomplete child results", () => {
  for (const messages of [[], [message("build")], [message(agent, "length")]]) {
    expect(() => childRunStream(messages, agent, child)).toThrow()
  }
})

test("native eval reads one executing model across every child assistant", () => {
  const first = message(agent, "tool-calls")
  const last = message()
  const response = childRunStream([first, last], agent, child)
  expect(response.metadata.readback_model).toBe("fixture/actual-model")
  expect(response.output.split("\n")).toHaveLength(first.parts.length + last.parts.length)
})

for (const [label, override] of [
  ["missing provider", { providerID: undefined }],
  ["missing model", { modelID: undefined }],
  ["empty provider", { providerID: "" }],
  ["empty model", { modelID: "" }],
  ["untyped model", { modelID: 42 }],
  ["invalid model", { modelID: "not a model" }],
  ["missing child", { sessionID: undefined }],
  ["foreign child", { sessionID: "other-child" }],
  ["missing agent", { agent: undefined }],
  ["foreign agent", { agent: "concord-verify" }],
  ["error terminal", { error: { name: "SyntheticError" } }],
] as const) {
  test(`native eval refuses ${label} assistant readback`, () => {
    const bad = message()
    Object.assign(bad.info, override)
    expect(() => childRunStream([bad], agent, child)).toThrow()
    expect(() => childRunStream([bad, message()], agent, child)).toThrow()
  })
}

test("native eval refuses mixed executing models even when the last assistant is valid", () => {
  for (const override of [{ providerID: "other" }, { modelID: "other-model" }]) {
    const first = message(agent, "tool-calls")
    Object.assign(first.info, override)
    expect(() => childRunStream([first, message()], agent, child)).toThrow()
  }
})

test("native eval admits only registered lane targets", () => {
  expect(new NativeLaneProvider({ config: { lane: "review" } }).id()).toBe("native-concord-review")
  expect(() => new NativeLaneProvider({ config: { lane: "build" } })).toThrow()
})

test("native eval asks its parent to invoke Task instead of injecting an executable subtask", () => {
  const provider = new NativeLaneProvider({ config: { lane: "review" } })
  const part = provider.taskMessage("Evaluate the synthetic report.")
  expect(part.type).toBe("text")
  expect(part.text).toContain("Invoke the native Task tool exactly once")
  expect(JSON.parse(part.text.split("\n")[1])).toEqual({ subagent_type: agent, description: `Advisory ${agent} eval`, prompt: "Evaluate the synthetic report." })
})

test("native eval disables Promptfoo whole-call retries", () => {
  expect(new NativeLaneProvider({ config: { lane: "review", maxRetries: 3 } }).config).toEqual({ lane: "review", maxRetries: 0 })
})

async function syntheticHost(options: {
  chunks?: string[], exitBeforeReady?: boolean, failRoute?: string, statusRoute?: string,
  bodyFailure?: boolean, readFailure?: boolean, cancelFailure?: boolean, holdClose?: boolean,
} = {}) {
  const cause = Object.assign(new Error("synthetic private transport detail"), { code: "ECONNRESET" })
  const failure = new TypeError("fetch failed", { cause })
  const requests: { method: string, url: string }[] = []
  const beforeLine: number[] = []
  let closed = false
  const reader = {
    read: mock(async () => {
      if (options.readFailure) throw failure
      const events = [
        { type: "session.created", properties: { info: { id: child, parentID: "parent", agent } } },
        { type: "session.idle", properties: { sessionID: child } },
      ]
      return { done: false, value: new TextEncoder().encode(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join("")) }
    }),
    cancel: mock(async () => { if (options.cancelFailure) throw new Error("synthetic cancellation failure") }),
  }
  const server = Object.assign(new EventEmitter(), {
    stdout: new EventEmitter(), stderr: new EventEmitter(),
    kill: mock(() => {
      if (!options.holdClose) queueMicrotask(() => { closed = true; server.emit("close") })
      return true
    }),
  })
  const spawn = spyOn(childProcess, "spawn").mockReturnValue(server as unknown as childProcess.ChildProcess)
  const fetch = spyOn(globalThis, "fetch").mockImplementation((async (input, init) => {
    const url = String(input)
    const route = new URL(url).pathname
    requests.push({ method: init?.method ?? "GET", url })
    if (route === options.failRoute) throw failure
    if (route === options.statusRoute) return { ok: false, status: 503, text: async () => "synthetic private body" }
    if (route === "/event") return { ok: true, body: { getReader: () => reader } }
    return {
      ok: true, status: route.endsWith("/prompt_async") ? 204 : 200,
      json: async () => {
        if (options.bodyFailure && route.endsWith("/message")) throw failure
        if (route === "/agent") return [{ name: agent, mode: "subagent", hidden: true }]
        if (route === "/session") return { id: "parent" }
        if (route === `/session/${child}/message`) return [message()]
        throw new Error(`unexpected synthetic route ${route}`)
      },
    }
  }) as typeof globalThis.fetch)
  try {
    let settled = false
    const outcome = new NativeLaneProvider({ config: { lane: "review" } }).callApi("synthetic private prompt")
      .then(response => { settled = true; return { response, error: undefined } }, error => { settled = true; return { response: undefined, error } })
    let output = ""
    for (const chunk of options.chunks ?? ["opencode server listening on http://127.0.0.1:18000\n"]) {
      output += chunk
      server.stdout.emit("data", Buffer.from(chunk))
      await setImmediate()
      if (!output.includes("\n")) beforeLine.push(requests.length)
    }
    if (options.exitBeforeReady) server.emit("close")
    const settledBeforeClose = settled
    if (options.holdClose) { closed = true; server.emit("close") }
    return { ...await outcome, requests, beforeLine, failure, reader, server, closed, settledBeforeClose }
  } finally {
    spawn.mockRestore()
    fetch.mockRestore()
  }
}

test("native eval waits for a complete listening line at every stdout chunk boundary", async () => {
  for (const ending of ["\n", "\r\n"]) {
    const line = `opencode server listening on http://127.0.0.1:18000${ending}`
    for (let split = 0; split < line.length; split++) {
      const run = await syntheticHost({ chunks: [line.slice(0, split), line.slice(split)] })
      expect(run.beforeLine).toEqual([0])
      expect(run.error).toBeUndefined()
      expect(run.requests[0]).toEqual({ method: "GET", url: "http://127.0.0.1:18000/agent" })
      expect(run.requests).toHaveLength(5)
      expect(run.reader.cancel).toHaveBeenCalledTimes(1)
      expect(run.server.kill).toHaveBeenCalledTimes(1)
      expect(run.closed).toBe(true)
    }
  }
})

test("native eval ignores preamble and refuses an unterminated announcement on host exit", async () => {
  const complete = await syntheticHost({ chunks: ["synthetic preamble\n", "opencode server listening on http://127.0.0.1:18000\n"] })
  expect(complete.error).toBeUndefined()
  const incomplete = await syntheticHost({ chunks: ["opencode server listening on http://127.0.0.1:18000"], exitBeforeReady: true })
  expect(incomplete.error).toBeInstanceOf(Error)
  expect(incomplete.requests).toHaveLength(0)
  expect(incomplete.server.kill).toHaveBeenCalledTimes(1)
})

for (const [method, route, hasReader] of [
  ["GET", "/agent", false],
  ["POST", "/session", false],
  ["GET", "/event", false],
  ["POST", "/session/parent/prompt_async", true],
  ["GET", `/session/${child}/message`, true],
] as const) {
  test(`native eval preserves ${method} ${route} transport cause and cleans up without retry`, async () => {
    const run = await syntheticHost({ failRoute: route })
    expect(run.error?.message).toContain(`${method} ${route}`)
    expect(run.error?.message).toContain("failed: TypeError <- Error ECONNRESET")
    expect(run.error?.cause).toBe(run.failure)
    expect(run.error?.cause.cause.code).toBe("ECONNRESET")
    expect(run.error?.message).not.toContain("private")
    expect(run.requests.filter(request => new URL(request.url).pathname === route)).toHaveLength(1)
    expect(run.reader.cancel).toHaveBeenCalledTimes(hasReader ? 1 : 0)
    expect(run.server.kill).toHaveBeenCalledTimes(1)
    expect(run.closed).toBe(true)
  })
}

test("native eval records an HTTP refusal's status without its body", async () => {
  const run = await syntheticHost({ statusRoute: "/agent" })
  expect(run.error?.message).toBe("native eval GET /agent failed: Error 503")
  expect(run.requests).toHaveLength(1)
  expect(run.server.kill).toHaveBeenCalledTimes(1)
  expect(run.closed).toBe(true)
})

test("native eval preserves response-body transport failures at the readback route", async () => {
  const run = await syntheticHost({ bodyFailure: true })
  expect(run.error?.message).toContain(`GET /session/${child}/message`)
  expect(run.error?.cause).toBe(run.failure)
  expect(run.requests).toHaveLength(5)
  expect(run.reader.cancel).toHaveBeenCalledTimes(1)
  expect(run.server.kill).toHaveBeenCalledTimes(1)
  expect(run.closed).toBe(true)
})

test("native eval identifies event-stream read failures and preserves the cause despite cancellation failure", async () => {
  const run = await syntheticHost({ readFailure: true, cancelFailure: true })
  expect(run.error?.message).toContain("GET /event")
  expect(run.error?.message).toContain("read")
  expect(run.error?.cause).toBe(run.failure)
  expect(run.error?.message).not.toContain("private")
  expect(run.requests).toHaveLength(4)
  expect(run.reader.cancel).toHaveBeenCalledTimes(1)
  expect(run.server.kill).toHaveBeenCalledTimes(1)
  expect(run.closed).toBe(true)
})

test("native eval waits for host closure on success and transport failure", async () => {
  for (const failRoute of [undefined, "/session/parent/prompt_async"]) {
    const run = await syntheticHost({ holdClose: true, failRoute })
    expect(run.settledBeforeClose).toBe(false)
    expect(run.reader.cancel).toHaveBeenCalledTimes(1)
    expect(run.server.kill).toHaveBeenCalledTimes(1)
    expect(run.closed).toBe(true)
    if (failRoute) expect(run.error?.cause).toBe(run.failure)
    else expect(run.response?.metadata.session_id).toBe(child)
  }
})
