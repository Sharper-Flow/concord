// One shared builder for the notice a successful session move carries. The
// tool result is the owning surface: it reaches every agent in every
// repository at the moment of the move, while a repository AGENTS.md line
// reaches only that repository's agents. The notice names the new absolute
// path, redirects reads, edits, and the shell working directory under it, and
// marks the pre-turn <env> working directory and the pre-move checkout stale,
// so the agent does not keep using the old checkout for the rest of the turn.
export function moveNoticeText(newPath: string): string {
  return `Concord moved this session to ${newPath}. Use paths under ${newPath} for reads, edits, and the shell working directory for the rest of this turn. The <env> working directory and the pre-move checkout are stale until the next turn. A turn-move boundary is active for the rest of this turn: the native question tool and dispatch stay closed until the next operator message clears it. If this landing was not confirmed, replay the worktree claim to retry the move; do not move the session by hand.`
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
