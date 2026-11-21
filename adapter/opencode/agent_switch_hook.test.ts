import { expect, test } from "bun:test"
import { createAgentSwitchNotice } from "./agent-switch-hook"

const pluginSource = await Bun.file(new URL("./concord-plugin.ts", import.meta.url)).text()
const SENTINEL = "--- ACTIVE AGENT CHANGED ---"
const base = () => [{ type: "text" as const, text: "BASE PROMPT" }]

test("plugin entry registers the agent-switch context hook", () => {
  expect(pluginSource).toContain('ctx.session.hook("context", agentSwitch.context)')
  expect(pluginSource).toContain("createAgentSwitchNotice()")
})

test("first request in a session produces no notice", () => {
  const hook = createAgentSwitchNotice()
  const system = base()
  hook.context({ sessionID: "s-first", agent: "concord-0", system })
  expect(system).toEqual(base())
})

test("an agent change appends exactly one notice naming both agents", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-switch", agent: "concord-0", system: base() })
  const system = base()
  hook.context({ sessionID: "s-switch", agent: "concord-1", system })
  expect(system).toHaveLength(2)
  expect(system[0]).toEqual(base()[0])
  expect(system[1].type).toBe("text")
  expect(system[1].text).toContain(SENTINEL)
  expect(system[1].text).toContain("`concord-0`")
  expect(system[1].text).toContain("`concord-1`")
})

test("the notice appears once and repeated requests with the same agent produce none", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-once", agent: "concord-1", system: base() })
  const first = base()
  hook.context({ sessionID: "s-once", agent: "concord-2", system: first })
  const second = base()
  hook.context({ sessionID: "s-once", agent: "concord-2", system: second })
  expect(first).toHaveLength(2)
  expect(first[1].text).toContain(SENTINEL)
  expect(second).toEqual(base())
})

test("a request without an agent changes nothing and preserves the session agent", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-noagent", agent: "concord-0", system: base() })
  const system = base()
  hook.context({ sessionID: "s-noagent", system } as Parameters<typeof hook.context>[0])
  expect(system).toEqual(base())
  const next = base()
  hook.context({ sessionID: "s-noagent", agent: "concord-1", system: next })
  expect(next[1].text).toContain("`concord-0`")
  expect(next[1].text).toContain("`concord-1`")
})

test("a session tracks its own agent independently", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-a", agent: "concord-0", system: base() })
  hook.context({ sessionID: "s-b", agent: "concord-1", system: base() })
  const b = base()
  hook.context({ sessionID: "s-b", agent: "concord-2", system: b })
  const a = base()
  hook.context({ sessionID: "s-a", agent: "concord-0", system: a })
  expect(a).toEqual(base())
  expect(b).toHaveLength(2)
  expect(b[1].text).toContain("`concord-1`")
  expect(b[1].text).toContain("`concord-2`")
})

test("a request without a session id changes nothing and does not affect another session", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-id", agent: "concord-0", system: base() })
  const system = base()
  hook.context({ agent: "concord-1", system } as Parameters<typeof hook.context>[0])
  expect(system).toEqual(base())
  const next = base()
  hook.context({ sessionID: "s-id", agent: "concord-0", system: next })
  expect(next).toEqual(base())
})

test("an existing sentinel from another emitter is not duplicated", () => {
  const hook = createAgentSwitchNotice()
  hook.context({ sessionID: "s-dedupe", agent: "concord-0", system: base() })
  const alreadyMarked = [...base(), { type: "text" as const, text: `\n\n${SENTINEL}\nhost plugin was here` }]
  const expected = [...alreadyMarked]
  hook.context({ sessionID: "s-dedupe", agent: "concord-1", system: alreadyMarked })
  expect(alreadyMarked).toEqual(expected)
  const next = base()
  hook.context({ sessionID: "s-dedupe", agent: "concord-1", system: next })
  expect(next).toEqual(base())
})
