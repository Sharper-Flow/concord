import { expect } from "bun:test"
import { SESSION_MESSAGES_ROUTE, type RouteClient, type RouteResult } from "./move-session"

let nextMessageID = 0

export async function acknowledgeNativeSubtask(request: Parameters<RouteClient["post"]>[0]): Promise<RouteResult> {
  expect(request.url).toBe(SESSION_MESSAGES_ROUTE)
  const body = request.body as { agent: string; noReply: boolean; parts: Record<string, unknown>[] }
  expect(body.noReply).toBe(true)
  expect(body.parts).toHaveLength(1)
  expect(body.parts[0].type).toBe("subtask")
  expect(Object.keys(body.parts[0]).sort()).toEqual(["agent", "description", "prompt", "type"])
  return { data: { info: { id: `queued-message-${++nextMessageID}`, sessionID: request.path?.id, agent: body.agent }, parts: body.parts }, response: new Response(null, { status: 200 }) }
}
