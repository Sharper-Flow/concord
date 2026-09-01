import { expect, test } from "bun:test"
import { createAgentSwitchNotice } from "./agent-switch-hook"

const pluginSource = await Bun.file(new URL("./concord-plugin.ts", import.meta.url)).text()
const SENTINEL = "--- ACTIVE AGENT CHANGED ---"
const TITLE_PROMPT = "You are a title generator. You output ONLY a thread title"

test("plugin entry registers the agent-switch hooks", () => {
  expect(pluginSource).toContain('"chat.message"')
  expect(pluginSource).toContain("createAgentSwitchNotice()")
})

test("first message in a session produces no notice", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-first", agent: "concord-0" })

  const out = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-first" }, out)

  expect(out.system[0]).toBe("BASE PROMPT")
})

test("an agent change appends a notice naming both agents", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-switch", agent: "concord-0" })
  await hook.chatMessage({ sessionID: "s-switch", agent: "concord-1" })

  const out = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-switch" }, out)

  expect(out.system[0].startsWith("BASE PROMPT")).toBe(true)
  expect(out.system[0]).toContain(SENTINEL)
  expect(out.system[0]).toContain("`concord-0`")
  expect(out.system[0]).toContain("`concord-1`")
})

test("the notice is consumed once", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-once", agent: "concord-1" })
  await hook.chatMessage({ sessionID: "s-once", agent: "concord-2" })

  const first = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-once" }, first)
  const second = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-once" }, second)

  expect(first.system[0]).toContain(SENTINEL)
  expect(second.system[0]).toBe("BASE PROMPT")
})

test("repeating the same agent produces no notice", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-same", agent: "concord-2" })
  await hook.chatMessage({ sessionID: "s-same", agent: "concord-2" })

  const out = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-same" }, out)

  expect(out.system[0]).toBe("BASE PROMPT")
})

test("an internal call does not consume the notice", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-internal", agent: "concord-2" })
  await hook.chatMessage({ sessionID: "s-internal", agent: "concord-0" })

  const internal = { system: [TITLE_PROMPT] }
  await hook.transform({ sessionID: "s-internal" }, internal)
  const real = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-internal" }, real)

  expect(internal.system[0]).toBe(TITLE_PROMPT)
  expect(real.system[0]).toContain(SENTINEL)
})

test("a message without an agent is ignored", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-noagent", agent: "concord-0" })
  await hook.chatMessage({ sessionID: "s-noagent" })

  const out = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-noagent" }, out)

  expect(out.system[0]).toBe("BASE PROMPT")
})

test("a session tracks its own agent independently", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-a", agent: "concord-0" })
  await hook.chatMessage({ sessionID: "s-b", agent: "concord-1" })
  await hook.chatMessage({ sessionID: "s-b", agent: "concord-2" })

  const a = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-a" }, a)
  const b = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-b" }, b)

  expect(a.system[0]).toBe("BASE PROMPT")
  expect(b.system[0]).toContain("`concord-1`")
})

test("a transform without a session id changes nothing", async () => {
  const hook = createAgentSwitchNotice()
  const out = { system: ["BASE PROMPT"] }
  await hook.transform({}, out)

  expect(out.system[0]).toBe("BASE PROMPT")
})

test("an existing sentinel from another emitter is not duplicated", async () => {
  const hook = createAgentSwitchNotice()
  await hook.chatMessage({ sessionID: "s-dedupe", agent: "concord-0" })
  await hook.chatMessage({ sessionID: "s-dedupe", agent: "concord-1" })

  const alreadyMarked = { system: ["BASE PROMPT\n\n--- ACTIVE AGENT CHANGED ---\nhost plugin was here"] }
  await hook.transform({ sessionID: "s-dedupe" }, alreadyMarked)
  const next = { system: ["BASE PROMPT"] }
  await hook.transform({ sessionID: "s-dedupe" }, next)

  expect(alreadyMarked.system[0]).not.toContain("`concord-0`")
  expect(next.system[0]).toBe("BASE PROMPT")
})
