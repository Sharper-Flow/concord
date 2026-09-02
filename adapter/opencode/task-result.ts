// CD-0102 D5: reading the host's task result wrapper.
//
// The host renders a finished worker as
// `<task id="SESSION" state="STATE">[<summary>..</summary>]<task_result>BODY</task_result></task>`,
// and an errored one with a `task_error` element in place of `task_result`
// (packages/opencode/src/tool/task.ts renderOutput).
//
// This wrapper is the adapter's only channel to the worker session identifier on
// the native route, and that identifier is what keeps CD-0058 D1 and CD-0017 D5
// readback evidence reachable: the adapter exports that session to learn which
// model and which agent actually executed.
export interface TaskResultRead {
  sessionID: string
  state: string
  text: string
}

// The host composes the wrapper from its own values, so a strict match is
// correct here: a body that does not carry exactly one identified task element
// and one result element is not a host task result, and admitting a loose match
// would let worker prose that merely names those elements stand in for one.
const TASK_OPEN = /<task\s+id="([^"]+)"\s+state="([^"]+)"\s*>/
const RESULT_BODY = /<(task_result|task_error)>\n?([\s\S]*?)\n?<\/\1>/

export function readTaskResult(raw: string): TaskResultRead | null {
  if (!raw) return null
  const open = TASK_OPEN.exec(raw)
  if (!open) return null
  const body = RESULT_BODY.exec(raw)
  if (!body) return null
  return { sessionID: open[1], state: open[2], text: body[2] }
}
