// One shared builder for the notice a successful session move carries. The
// tool result is the owning surface: it reaches every agent in every
// repository at the moment of the move, while a repository AGENTS.md line
// reaches only that repository's agents. The notice names the new absolute
// path, redirects reads, edits, and the shell working directory under it, and
// marks the pre-turn <env> working directory and the pre-move checkout stale,
// so the agent does not keep using the old checkout for the rest of the turn.
//
// Every caller records the notice only after a landing the host read back, so
// the notice never carries unconfirmed-landing recovery: each route's refusal
// names its own replay. boundaryActive is the turn-move boundary state the
// caller reads after arming, so the notice names the boundary only when the
// native question tool and dispatch are in fact closed.
export const TURN_MOVE_BOUNDARY_NOTICE =
  "A turn-move boundary is active: the native question tool and dispatch stay closed until the next operator message. To ask the operator a question, write it in normal chat and end the turn."

export function moveNoticeText(newPath: string, boundaryActive: boolean): string {
  const notice = `Concord moved this session to ${newPath}. Use paths under ${newPath} for reads, edits, and the shell working directory for the rest of this turn. The <env> working directory and the pre-move checkout are stale until the next turn.`
  if (!boundaryActive) return notice
  return `${notice} ${TURN_MOVE_BOUNDARY_NOTICE}`
}

// The notice queue carries the move fact from the move route to the result
// the same tool call encodes. A confirmed move records; a refused move
// records nothing, and the drain empties the slot either way, so no later
// result can inherit an earlier call's notice.
const pendingNotices = new Map<string, string>()

export function recordMoveNotice(sessionID: string, text: string): void {
  pendingNotices.set(sessionID, text)
}

export function takeMoveNotice(sessionID: string): string | null {
  const text = pendingNotices.get(sessionID)
  pendingNotices.delete(sessionID)
  return text ?? null
}

export function resetMoveNotices(): void {
  pendingNotices.clear()
}
