import { afterEach, describe, expect, test } from "bun:test"
import { recordMoveNotice, resetMoveNotices, takeMoveNotice } from "./move-notice"

afterEach(resetMoveNotices)

describe("the notice queue never leaks across calls", () => {
  test("a drained session leaves nothing behind", async () => {
    recordMoveNotice("session-1", "one")
    expect(takeMoveNotice("session-1")).toBe("one")
    expect(takeMoveNotice("session-1")).toBeNull()
    expect(takeMoveNotice("session-2")).toBeNull()
  })
})
