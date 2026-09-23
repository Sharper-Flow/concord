// Connected regression for repeated session_vacate of one work item by one
// session. The cycle runs the real core binary against a real store and the
// adapter's own host session mover and readback: worktree_claim moves the
// session into the claimed worktree, session_vacate records the operation and
// moves the session to the derived main checkout, work_start resumes
// read-only through work-resume, and a second vacate records its own event.
// A same-key retry after the host lands in main refuses before another move
// or occupancy change. The original Project claim survives both vacates, and
// the same work ID then claims in a second Project.
import { afterAll, afterEach, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey } from "node:crypto"
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { configureConcordAdapter, invokeConcordOperation, work_start, work_transition } from "./concord"
import { configureCoreBinary } from "./dispatch"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, MOVE_SESSION_ROUTE, SESSION_ROUTE } from "./move-session"
import { configureHostLease } from "./host-lease"
import { clearClaimedWorktree, resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"

// The cycle drives the real core through its own runner, so argv[0] is
// replaced there. Bind the nominal path the transport resolves instead of
// the unstamped repository placeholder (CD-0111 D1), as the dispatch route
// test does.
configureCoreBinary("concord")

const PRODUCT_ID = "product-reoccupy"
const PROJECT_1 = "project-reoccupy-1"
const PROJECT_2 = "project-reoccupy-2"
const SESSION_ID = "reoccupy-session"
const MESSAGE_ID = "reoccupy-message"
const AGENT = "concord-implement"
const LANE_AGENTS = ["concord-research", "concord-implement", "concord-design", "concord-review", "concord-verify"]
const PRIVATE_SEED = new Uint8Array(32).fill(9)
const PRIVATE_KEY_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex")

function publicKeyBase64(): string {
  const privateKey = createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(PRIVATE_SEED)]), format: "der", type: "pkcs8" })
  return createPublicKey(privateKey).export({ format: "der", type: "spki" }).subarray(-32).toString("base64")
}

async function runProcess(argv: string[], input = "", cwd?: string, env: Record<string, string> = {}): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const child = Bun.spawn(argv, { cwd, env: { ...process.env, ...env }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
  await child.stdin.write(input)
  await child.stdin.end()
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  return { exitCode, stdout, stderr }
}

async function git(cwd: string, ...args: string[]): Promise<string> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
  return result.stdout.trim()
}

async function repositoryFixture(root: string, name: string): Promise<{ repo: string }> {
  const repo = join(root, name)
  await mkdir(join(repo, "docs"), { recursive: true })
  await Bun.write(join(repo, "docs", "concord-knowledge-index.v1.json"), JSON.stringify({
    schema_version: "1.2",
    supported_kinds: [],
    indexed_kinds: [],
    knowledge_roots: [],
    domain_registry: {
      schema_version: "1.0",
      product_key: PRODUCT_ID,
      root_domain_id: `product-root:${PRODUCT_ID}`,
      domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
    },
    records: [],
  }, null, 2))
  await Bun.write(join(repo, "opencode.jsonc"), JSON.stringify({ instructions: ["https://example.invalid/synthetic-instructions"] }))
  await Bun.write(join(repo, "README.md"), "synthetic vacate reoccupy fixture\n")
  await git(repo, "init", "--quiet", "--initial-branch=main")
  await git(repo, "config", "user.email", "test@example.invalid")
  await git(repo, "config", "user.name", "Synthetic Test")
  await git(repo, "add", ".")
  await git(repo, "commit", "--quiet", "-m", "fixture")
  await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
  await git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
  await git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
  return { repo }
}

function dbValue(dbPath: string, sql: string, ...params: string[]): any {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).get(...params)
  } finally {
    db.close()
  }
}

function dbRows(dbPath: string, sql: string, ...params: string[]): any[] {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).all(...params)
  } finally {
    db.close()
  }
}

function contextFor(directory: string): any {
  return {
    sessionID: SESSION_ID,
    messageID: MESSAGE_ID,
    agent: AGENT,
    directory,
    worktree: directory,
    abort: new AbortController().signal,
  }
}

// The suite's files share one process in an order no file controls, so leave
// every module-level seam clean on both sides of the run.
afterEach(async () => {
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
})
afterAll(() => configureHostLease({ reset: true }))

const connected =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? test
    : test.skip

connected("vacate, work-resume, vacate in one session keeps one event per request", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-vacate-reoccupy-"))
  const dbPath = join(root, "concord.db")
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  const repoCheckout = join(root, "checkout")
  let binary = process.env.CONCORD_BIN ?? ""
  const previousSelectedProduct = process.env.CONCORD_SELECTED_PRODUCT_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  // A wrapping zellij pane would append a rename warning to every work_start
  // result; the cycle owns its own host seams and no pane.
  const previousZellijPane = process.env.ZELLIJ_PANE_ID
  delete process.env.ZELLIJ_PANE_ID
  try {
    if (!binary) {
      await mkdir(binRoot, { recursive: true })
      binary = join(binRoot, "concord-core")
      const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
      expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    }
    const repo1 = await repositoryFixture(root, "repo-1")
    const repo2 = await repositoryFixture(root, "repo-2")
    await mkdir(repoCheckout, { recursive: true })

    // The core verifies lane agent identity from $HOME and probes the host
    // agent registry through `opencode debug config`. Both seams are
    // fixture-owned so the cycle never depends on an installed host.
    await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
    for (const lane of LANE_AGENTS) {
      await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
    }
    const probe = join(binRoot, "opencode")
    await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
    await chmod(probe, 0o755)
    const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}`, CONCORD_DB_PATH: dbPath }

    const runCLI = (command: string, value: Record<string, unknown>, cwd?: string) => runProcess([binary, command], JSON.stringify(value), cwd, childEnv).then((result) => {
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean).pop() as string)
    })

    await runCLI("product-create", {
      product_id: PRODUCT_ID,
      display_name: "Synthetic Reoccupy Product",
      stage_maturity: "prototype",
      stage_audience_commitment: "operator_only",
      project_id: PROJECT_1,
      project_display_name: "Synthetic Reoccupy Project",
      role: "primary",
    })
    const productVersion = dbValue(dbPath, "SELECT version FROM products WHERE id=?", PRODUCT_ID).version as number
    await runCLI("project-create", {
      project_id: PROJECT_2,
      display_name: "Synthetic Reoccupy Project Two",
      product_id: PRODUCT_ID,
      role: "secondary",
      expected_product_version: productVersion,
    })
    await runCLI("project-locator-add", { project_id: PROJECT_1, locator_id: "repo-1", kind: "canonical_path", value: repo1.repo, expected_version: 1 })
    await runCLI("project-locator-add", { project_id: PROJECT_2, locator_id: "repo-2", kind: "canonical_path", value: repo2.repo, expected_version: 1 })
    const designated = dbValue(dbPath, "SELECT version FROM products WHERE id=?", PRODUCT_ID).version as number
    await runCLI("product-knowledge-home-designate", { product_id: PRODUCT_ID, project_id: PROJECT_1, locator_id: "repo-1", expected_version: designated })
    await runCLI("client-register", {
      client_ref: "opencode",
      key_id: "reoccupy-key",
      principal_ref: "operator-1",
      public_key: publicKeyBase64(),
      capabilities: ["product_read", "work_define", "work_transition"],
      product_scope: [PRODUCT_ID],
      project_scope: [PROJECT_1, PROJECT_2],
      agent_scope: [AGENT],
    })

    // The fake host holds one live session and its live directory.
    // moveSession performs the relocation, the session route reads the
    // directory and the participation metadata back, and every move is
    // recorded with the directory the session left. failNextMove models a
    // host that performs the relocation and then fails to answer, so the
    // retry of the same vacate arrives after the landing already holds.
    let sessionDirectory = repo1.repo
    let sessionMetadata: Record<string, unknown> = {}
    let failNextMove = false
    const moves: Array<{ from: string; to: string }> = []
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        return { data: { id: SESSION_ID, directory: sessionDirectory, metadata: sessionMetadata }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        const patch = body as { metadata?: Record<string, unknown>; title?: string }
        if (patch.metadata !== undefined) {
          expect(patch).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
          sessionMetadata = patch.metadata
        }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async ({ url, body }) => {
        expect(url).toBe(MOVE_SESSION_ROUTE)
        const destination = (body as { destination: { directory: string } }).destination.directory
        if (sessionDirectory !== destination) moves.push({ from: sessionDirectory, to: destination })
        sessionDirectory = destination
        if (failNextMove) {
          failNextMove = false
          return { data: "synthetic host failure after the relocation", response: new Response(null, { status: 500 }) }
        }
        return { data: null, response: new Response(null, { status: 204 }) }
      },
    })
    const runner = {
      async run(argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) {
        const child = Bun.spawn([binary, ...argv.slice(1)], { cwd: options?.cwd ?? repoCheckout, env: { ...process.env, ...childEnv }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
        if (signal?.aborted) child.kill()
        await child.stdin.write(input)
        await child.stdin.end()
        const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
        return { exitCode, stdout, stderr }
      },
    }
    configureConcordAdapter({ runner })
    configureHostLease({ reset: true })

    // parseToolResult splits the JSON envelope from any warning lines the
    // tool wrapper appends after it.
    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0])
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
    const invoke = (toolName: string, operation: string, input: Record<string, unknown>, directory: string) =>
      invokeConcordOperation(toolName, { operation, input } as any, contextFor(directory))
    const transition = async (operation: string, input: Record<string, unknown>, directory: string) =>
      parseToolResult(await work_transition.execute({ request: { operation, input } } as any, contextFor(directory)))
    const vacateEvents = () => dbRows(dbPath, "SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? ORDER BY seq", workID)
    const worktreeEntries = () => dbRows(dbPath, "SELECT c.project_id AS project_id, e.path AS path, e.state AS state, e.occupant_session_ref AS occupant FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? ORDER BY c.project_id", workID)

    // One work item holds membership in both Projects from capture, and no
    // worktree. The first work_start resume durably creates the primary
    // Project's worktree, records the session as its occupant, and the
    // mover lands the session in it.
    const captured = await invoke("concord_work_define", "capture", {
      title: "Synthetic vacate reoccupy",
      value_statement: "One session vacates, resumes, and vacates one work item.",
      kind: "bug",
      project_ids: [PROJECT_1, PROJECT_2],
      idempotency_key: "reoccupy-capture",
    }, repo1.repo)
    expect(captured.outcome, JSON.stringify(captured)).toBe("ok")
    const workID = (captured.changed_refs as Array<{ entity_kind: string; id: string }>)[0].id

    const resume = async (directory: string) => parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(directory)))

    // Entry into work: the resume read durably creates the worktree and the
    // mover lands the session in it. The old tool context cannot attest that
    // landing; only a next-turn replay from the worktree reports success.
    const unlandedEntry = await resume(repo1.repo)
    expect(unlandedEntry.outcome, JSON.stringify(unlandedEntry)).toBe("error")
    expect(unlandedEntry.error.kind).toBe("session_directory_mismatch")
    const worktree1 = unlandedEntry.worktree_path as string
    expect(moves).toEqual([{ from: repo1.repo, to: worktree1 }])
    const entered = await resume(worktree1)
    expect(entered.outcome, JSON.stringify(entered)).toBe("ok")
    expect(entered.worktree_path).toBe(worktree1)
    expect(moves).toHaveLength(1)
    expect(worktreeEntries()).toEqual([{ project_id: PROJECT_1, path: worktree1, state: "active", occupant: SESSION_ID }])

    // First vacate: the core records the operation toward the derived main
    // checkout and releases the recorded occupancy. The host performs the
    // relocation and then fails to answer, so the adapter reports the typed
    // retryable transport failure while the landing already holds.
    failNextMove = true
    const first = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-1" }, worktree1)
    expect(first.outcome, JSON.stringify(first)).toBe("error")
    expect(first.replayed).toBe(false)
    expect((first.error as any).kind).toBe("transport_failure")
    expect((first.error as any).recovery_action).toEqual({ kind: "retry_same_request" })
    expect(vacateEvents()).toHaveLength(1)
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
    ])
    expect(worktreeEntries()[0].occupant).toBe("")

    // A retry of one unchanged vacate after the landing holds moves nothing
    // and changes nothing: the host readback now reports the main checkout,
    // whose refusal the core answers before any move or occupancy write,
    // and the durable vacate history still holds exactly one operation.
    const replay = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-1" }, worktree1)
    expect(replay.outcome, JSON.stringify(replay)).toBe("error")
    expect((replay.error as any).kind).toBe("unauthorized")
    expect(moves).toHaveLength(2)
    expect(vacateEvents()).toHaveLength(1)
    expect(worktreeEntries()[0].occupant).toBe("")

    // Work resume remains read-only. A move back from main refuses while the
    // tool context still reports main; next-turn replay from the worktree
    // succeeds without another move or vacate event.
    const unlandedResume = await resume(repo1.repo)
    expect(unlandedResume.outcome, JSON.stringify(unlandedResume)).toBe("error")
    expect(unlandedResume.error.kind).toBe("session_directory_mismatch")
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
      { from: repo1.repo, to: worktree1 },
    ])
    const resumed = await resume(worktree1)
    expect(resumed.outcome, JSON.stringify(resumed)).toBe("ok")
    expect(resumed.worktree_path).toBe(worktree1)
    expect(moves).toHaveLength(3)
    expect(vacateEvents()).toHaveLength(1)
    expect(worktreeEntries()[0].occupant).toBe("")

    // Second vacate, new request identity: the same session leaves the same
    // worktree again and the core records a second, distinct operation.
    const second = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-2" }, worktree1)
    expect(second.outcome, JSON.stringify(second)).toBe("ok")
    expect(second.replayed).toBe(false)
    const events = vacateEvents()
    expect(events).toHaveLength(2)
    expect(events[0].event_id).not.toBe(events[1].event_id)
    for (const event of events) {
      expect(JSON.parse(event.payload as string)).toMatchObject({
        work_id: workID,
        project_id: PROJECT_1,
        session_ref: SESSION_ID,
        source_directory: worktree1,
        destination_directory: repo1.repo,
      })
    }

    // Both vacates preserve the original Project claim, active and
    // unoccupied. The vacated session then claims the same work item in the
    // second Project through the durable resume route; the host move that
    // would follow is outside this cycle's vacate proof.
    const backend = await runCLI("work-resume", { product_id: PRODUCT_ID, project_id: PROJECT_2, work_id: workID, session_ref: SESSION_ID }, repo2.repo)
    const worktree2 = backend.worktree.path as string
    expect(moves).toHaveLength(4)
    const entries = worktreeEntries()
    expect(entries).toHaveLength(2)
    expect(entries[0]).toMatchObject({ project_id: PROJECT_1, path: worktree1, state: "active", occupant: "" })
    expect(entries[1]).toMatchObject({ project_id: PROJECT_2, path: worktree2, state: "active", occupant: SESSION_ID })
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    clearClaimedWorktree(SESSION_ID)
    resetClaimedWorktrees()
    resetTurnMoveBoundaries()
    if (previousSelectedProduct === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previousSelectedProduct
    if (previousZellijPane === undefined) delete process.env.ZELLIJ_PANE_ID
    else process.env.ZELLIJ_PANE_ID = previousZellijPane
    await rm(root, { recursive: true, force: true })
  }
}, 300_000)
