// CD-0097 D5. The host wraps a worker result as
// <task id="SESSION" state="STATE"><task_result>BODY</task_result></task>,
// and that wrapper is the only place the worker session identifier reaches the
// adapter on the native route. Readback evidence depends on reading it.
import { describe, expect, test } from "bun:test"
import { readTaskResult } from "./task-result"

const wrap = (id: string, state: string, tag: string, text: string) =>
  [`<task id="${id}" state="${state}">`, `<${tag}>`, text, `</${tag}>`, "</task>"].join("\n")

describe("task result wrapper", () => {
  test("reads the worker session identifier and the body", () => {
    const read = readTaskResult(wrap("ses_abc123", "completed", "task_result", '{"schema_version":"1.0"}'))
    expect(read).not.toBeNull()
    expect(read!.sessionID).toBe("ses_abc123")
    expect(read!.state).toBe("completed")
    expect(read!.text).toBe('{"schema_version":"1.0"}')
  })

  test("preserves a multi-line body verbatim", () => {
    const body = 'line one\n{"schema_version":"1.0"}\n\nline three'
    expect(readTaskResult(wrap("ses_1", "completed", "task_result", body))!.text).toBe(body)
  })

  test("reads an error result and reports its state", () => {
    const read = readTaskResult(wrap("ses_err", "error", "task_error", "the worker tool call failed"))
    expect(read!.state).toBe("error")
    expect(read!.text).toBe("the worker tool call failed")
  })

  test("a summary element does not become the body", () => {
    const raw = [
      '<task id="ses_2" state="completed">',
      "<summary>did the thing</summary>",
      "<task_result>",
      "the report",
      "</task_result>",
      "</task>",
    ].join("\n")
    const read = readTaskResult(raw)
    expect(read!.text).toBe("the report")
  })

  test("refuses a body that is not a task wrapper", () => {
    expect(readTaskResult("just some prose")).toBeNull()
    expect(readTaskResult("")).toBeNull()
    expect(readTaskResult('<task id="ses_3" state="completed">no result element</task>')).toBeNull()
  })

  test("refuses a wrapper with no session identifier", () => {
    expect(readTaskResult("<task state=\"completed\">\n<task_result>\nx\n</task_result>\n</task>")).toBeNull()
  })
})
