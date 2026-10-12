// The host's session-directory answer is not durable across a move. work_start
// and worktree_claim confirm the landing with a strict host read-back, and the
// same route can answer the previous directory minutes later while the
// effective session directory regresses before it converges. The dispatch
// path's retarget guard compares two host answers, so a stable stale answer
// passes both. The armed claim is the adapter's own record of the last
// confirmed landing, which the host's answer must still agree with when a
// dispatch opens its authorization window.
//
// A metadata-only work_start move never reaches that confirmed
// landing: the host accepted the retarget but the session's tool context still
// runs in the pre-move directory, so the declared refusal arms nothing. The
// unlanded claim records that refused move so the dispatch gate fails closed
// for the session until the tool context resolves inside the claimed worktree.

const MAX_ARMED_CLAIMS = 512

const armedClaims = new Map<string, string>()

const unlandedClaims = new Map<string, string>()

const pendingVacateDestinations = new Map<string, string>()

// recordUnlandedClaimedWorktree records the claimed worktree of a work_start
// move that refused as metadata-only, whose tool context has not landed.
// A later confirmed landing for the same session replaces the record.
//
// The map carries no cap eviction. An unlanded record is the fail-closed
// state of a refused move, and its only exits are a confirmed landing
// (armClaimedWorktree), vacate (clearClaimedWorktree), or a host process
// restart. Evicting one by cap would forget an unresolved move and let
// dispatch authorize on lost in-memory state, so the map is lifecycle-bounded
// instead: one entry per session with a refused move outstanding.
export function recordUnlandedClaimedWorktree(sessionID: string, directory: string): void {
  if (!sessionID || !directory) return
  unlandedClaims.delete(sessionID)
  unlandedClaims.set(sessionID, directory)
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

// recordPendingVacateDestination records the registered main checkout of the
// relocation request a session_vacate committed for one session, so a later
// session_vacate from that session first moves the host session there and
// resolves the core call from it (CD-0190 D3). A newer committed request for
// the same session replaces the recorded destination.
//
// The map carries no cap eviction. An entry is the pending state of a
// committed relocation request, and its only exits are the verified landing
// (clearClaimedWorktree) or a host process restart. Evicting one by cap would
// forget a committed request and leave its occupancy rows without a reachable
// recovery, so the map is lifecycle-bounded instead: one entry per session
// with a committed request outstanding.
export function recordPendingVacateDestination(sessionID: string, directory: string): void {
  if (!sessionID || !directory) return
  pendingVacateDestinations.delete(sessionID)
  pendingVacateDestinations.set(sessionID, directory)
}

// pendingVacateDestination answers the recorded registered main checkout for
// one session, or null when the adapter holds no remembered destination. Null
// is also the state after a host process restart.
export function pendingVacateDestination(sessionID: string): string | null {
  return sessionID ? pendingVacateDestinations.get(sessionID) ?? null : null
}

// clearClaimedWorktree drops the records when session_vacate has returned the
// session to the registered main checkout. The verified landing completes the
// pending relocation request, so its remembered destination goes with the
// armed claim.
export function clearClaimedWorktree(sessionID: string): void {
  if (sessionID) {
    armedClaims.delete(sessionID)
    unlandedClaims.delete(sessionID)
    pendingVacateDestinations.delete(sessionID)
  }
}

// resetClaimedWorktrees drops every record. Production code never calls it:
// a host process restart is the production equivalent. It exists so a test
// file cannot leak an armed claim into another file's dispatch checks, which
// share this module instance inside one test run.
export function resetClaimedWorktrees(): void {
  armedClaims.clear()
  unlandedClaims.clear()
  pendingVacateDestinations.clear()
}

// armedClaimedWorktree answers the armed directory for one session, or null
// when nothing is armed. A null answer is also the state after a host
// process restart.
export function armedClaimedWorktree(sessionID: string): string | null {
  return sessionID ? armedClaims.get(sessionID) ?? null : null
}
