// The host's session-directory answer is not durable across a move. work_start
// and worktree_claim confirm the landing with a strict host read-back, and the
// same route can answer the previous directory minutes later while the
// effective session directory regresses before it converges. The dispatch
// path's retarget guard compares two host answers, so a stable stale answer
// passes both. The armed claim is the adapter's own record of the last
// confirmed landing, which the host's answer must still agree with when a
// dispatch opens its authorization window.
//
// A metadata-only work_start move (issue #1322) never reaches that confirmed
// landing: the host accepted the retarget but the session's tool context still
// runs in the pre-move directory, so the declared refusal arms nothing. The
// unlanded claim records that refused move so the dispatch gate fails closed
// for the session until the tool context resolves inside the claimed worktree.

const MAX_ARMED_CLAIMS = 512

const armedClaims = new Map<string, string>()

const unlandedClaims = new Map<string, string>()

// recordUnlandedClaimedWorktree records the claimed worktree of a work_start
// move that refused as metadata-only, whose tool context has not landed.
// A later confirmed landing for the same session replaces the record.
export function recordUnlandedClaimedWorktree(sessionID: string, directory: string): void {
  if (!sessionID || !directory) return
  unlandedClaims.delete(sessionID)
  unlandedClaims.set(sessionID, directory)
  while (unlandedClaims.size > MAX_ARMED_CLAIMS) {
    const oldest = unlandedClaims.keys().next()
    if (oldest.done) break
    unlandedClaims.delete(oldest.value)
  }
}

// unlandedClaimedWorktree answers the recorded unlanded directory for one
// session, or null when the session has no refused move outstanding.
export function unlandedClaimedWorktree(sessionID: string): string | null {
  return sessionID ? unlandedClaims.get(sessionID) ?? null : null
}

// armClaimedWorktree records the confirmed claimed worktree for one session.
// A newer confirmed claim for the same session replaces the recorded directory.
// The confirmed landing supersedes any unlanded record for the session.
export function armClaimedWorktree(sessionID: string, directory: string): void {
  if (!sessionID || !directory) return
  unlandedClaims.delete(sessionID)
  armedClaims.delete(sessionID)
  armedClaims.set(sessionID, directory)
  while (armedClaims.size > MAX_ARMED_CLAIMS) {
    const oldest = armedClaims.keys().next()
    if (oldest.done) break
    armedClaims.delete(oldest.value)
  }
}

// clearClaimedWorktree drops the records when session_vacate has returned the
// session to the registered main checkout.
export function clearClaimedWorktree(sessionID: string): void {
  if (sessionID) {
    armedClaims.delete(sessionID)
    unlandedClaims.delete(sessionID)
  }
}

// resetClaimedWorktrees drops every record. Production code never calls it:
// a host process restart is the production equivalent. It exists so a test
// file cannot leak an armed claim into another file's dispatch checks, which
// share this module instance inside one test run.
export function resetClaimedWorktrees(): void {
  armedClaims.clear()
  unlandedClaims.clear()
}

// armedClaimedWorktree answers the armed directory for one session, or null
// when nothing is armed. Null dispatches exactly as before the record existed,
// which is also the state after a host process restart.
export function armedClaimedWorktree(sessionID: string): string | null {
  return sessionID ? armedClaims.get(sessionID) ?? null : null
}
