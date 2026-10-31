import { afterAll, afterEach, beforeEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { configureCoreBinary } from "./core-runtime"
import { hostControlPlane } from "./move-session"
import { armTurnMoveBoundary, clearTurnMoveBoundary, questionRequiresNormalChat, TURN_MOVE_QUESTION_REFUSAL } from "./turn-move-boundary"

configureCoreBinary("concord")
const sessionID = "session-boundary"
let plugin: any

beforeEach(async () => {
  clearTurnMoveBoundary(sessionID)
  hostControlPlane().bind(undefined)
  plugin = await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
})

afterEach(() => {
  clearTurnMoveBoundary(sessionID)
  hostControlPlane().bind(undefined)
  configureHostLease({ reset: true })
})

describe("same-turn session move boundary", () => {
  test("refuses question before pending prompt creation and clears on the next chat message", async () => {
    armTurnMoveBoundary(sessionID)
    expect(questionRequiresNormalChat(sessionID)).toBe(true)
    await expect(plugin["tool.execute.before"]({ tool: "question", sessionID, callID: "question-1" }, { args: { questions: [] } }))
      .rejects.toThrow(TURN_MOVE_QUESTION_REFUSAL)
    await plugin["chat.message"]({ sessionID, agent: "agent-1" })
    expect(questionRequiresNormalChat(sessionID)).toBe(false)
    await expect(plugin["tool.execute.before"]({ tool: "question", sessionID, callID: "question-1" }, { args: { questions: [] } })).resolves.toBeUndefined()
  })

  test("leaves non-question tools and other sessions unchanged", async () => {
    armTurnMoveBoundary(sessionID)
    await expect(plugin["tool.execute.before"]({ tool: "read", sessionID, callID: "read-1" }, { args: {} })).resolves.toBeUndefined()
    await expect(plugin["tool.execute.before"]({ tool: "question", sessionID: "other-session", callID: "question-2" }, { args: {} })).resolves.toBeUndefined()
  })
})

afterAll(() => configureHostLease({ reset: true }))
