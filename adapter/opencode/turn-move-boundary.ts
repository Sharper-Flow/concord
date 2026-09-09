// OpenCode 1.18.30 can route a same-turn question response through the
// session's new directory after a move. Keep that turn closed to the native
// question tool until the next operator message.

const MAX_ARMED_SESSIONS = 512

export const TURN_MOVE_QUESTION_REFUSAL =
  "A cross-directory session move completed in this turn. Do not use the native question tool. Ask the operator in normal chat instead, then stop this turn."

const armedSessions = new Set<string>()

export function armTurnMoveBoundary(sessionID: string): void {
  if (!sessionID) return
  armedSessions.delete(sessionID)
  armedSessions.add(sessionID)
  while (armedSessions.size > MAX_ARMED_SESSIONS) {
    const oldest = armedSessions.keys().next()
    if (oldest.done) break
    armedSessions.delete(oldest.value)
  }
}

export function clearTurnMoveBoundary(sessionID: string): void {
  if (sessionID) armedSessions.delete(sessionID)
}

export function questionRequiresNormalChat(sessionID: string): boolean {
  return !!sessionID && armedSessions.has(sessionID)
}
