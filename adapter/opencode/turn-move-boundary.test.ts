import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureCoreBinary } from "./dispatch"
import { configureConcordAdapter } from "./concord"
import { hostControlPlane } from "./move-session"
import { moveSessionToClaimedWorktree, moveSessionToRegisteredMainCheckout, work_start } from "./concord"
import { armTurnMoveBoundary, clearTurnMoveBoundary, questionRequiresNormalChat, TURN_MOVE_QUESTION_REFUSAL } from "./turn-move-boundary"

configureCoreBinary("concord")

const sessionID = "session-boundary"
const context = (directory = "/origin") => ({
  sessionID,
  messageID: "message-boundary",
  agent: "agent-1",
  directory,
  worktree: directory,
  abort: new AbortController().signal,
}) as any

const successfulEnvelope = (destination = "/destination") => ({
  schema_version: "1.0",
  outcome: "ok",
  result: { destination_directory: destination },
}) as any

const claimArgs = (path: string) => ({ operation: "worktree_claim", input: { work_id: "work-1", path } }) as any
const vacateArgs = () => ({ operation: "session_vacate", input: { idempotency_key: "vacate-1" } }) as any

let plugin: any

beforeEach(async () => {
  clearTurnMoveBoundary(sessionID)
  hostControlPlane().bind(undefined)
  plugin = await ConcordAdapterPlugin({})
})

afterEach(() => {
  clearTurnMoveBoundary(sessionID)
  hostControlPlane().bind(undefined)
  configureConcordAdapter({ reset: true })
})

const beforeHook = () => plugin["tool.execute.before"]
const chatHook = () => plugin["chat.message"]

async function expectQuestionBlocked(): Promise<void> {
  await expect(beforeHook()({ tool: "question", sessionID, callID: "question-1" }, { args: { questions: [] } }))
    .rejects.toThrow(TURN_MOVE_QUESTION_REFUSAL)
}

async function expectQuestionAllowed(): Promise<void> {
  await expect(beforeHook()({ tool: "question", sessionID, callID: "question-1" }, { args: { questions: [] } })).resolves.toBeUndefined()
}

function bindMoveRoutes(destination: string, options: { moveStatus?: number; landed?: string } = {}): void {
  let metadata: Record<string, unknown> = {}
  hostControlPlane().bind({
    post: async () => ({ response: new Response(null, { status: options.moveStatus ?? 204 }) }),
    get: async () => ({
      data: { id: sessionID, directory: options.landed ?? destination, metadata },
      response: new Response(null, { status: 200 }),
    }),
    patch: async ({ body }: any) => {
      metadata = body.metadata
      return { response: new Response(null, { status: 200 }) }
    },
  })
}

describe("same-turn session move boundary", () => {
  test("refuses question before pending prompt creation and clears on the next chat message", async () => {
    armTurnMoveBoundary(sessionID)
    expect(questionRequiresNormalChat(sessionID)).toBe(true)
    await expectQuestionBlocked()

    await chatHook()({ sessionID, agent: "agent-1" })
    expect(questionRequiresNormalChat(sessionID)).toBe(false)
    await expectQuestionAllowed()
  })

  test("leaves non-question tools and other sessions unchanged", async () => {
    armTurnMoveBoundary(sessionID)
    await expect(beforeHook()({ tool: "read", sessionID, callID: "read-1" }, { args: {} })).resolves.toBeUndefined()
    await expect(beforeHook()({ tool: "question", sessionID: "other-session", callID: "question-2" }, { args: {} })).resolves.toBeUndefined()
  })

  test("arms only after a successful cross-directory worktree claim", async () => {
    bindMoveRoutes("/claimed")
    const result = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), successfulEnvelope())
    expect(result.outcome).toBe("ok")
    await expectQuestionBlocked()

    clearTurnMoveBoundary(sessionID)
    bindMoveRoutes("/origin")
    await moveSessionToClaimedWorktree(claimArgs("/origin"), context("/origin"), successfulEnvelope("/origin"))
    await expectQuestionAllowed()
  })

  test("does not arm after a refused or mismatched claim", async () => {
    bindMoveRoutes("/claimed", { moveStatus: 409 })
    expect((await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), successfulEnvelope())).outcome).toBe("error")
    await expectQuestionAllowed()

    bindMoveRoutes("/claimed", { landed: "/other" })
    expect((await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), successfulEnvelope())).outcome).toBe("error")
    await expectQuestionAllowed()
  })

  test("arms after a successful cross-directory session vacate", async () => {
    bindMoveRoutes("/main")
    const result = await moveSessionToRegisteredMainCheckout(vacateArgs(), context(), successfulEnvelope("/main"))
    expect(result.outcome).toBe("ok")
    await expectQuestionBlocked()
  })

  test("work_start arms after its successful cross-directory move", async () => {
    let moved = false
    let managed = false
    bindMoveRoutes("/worktree")
    hostControlPlane().bind({
      post: async ({ url }: any) => {
        if (url === "/session/{id}") return { response: new Response(null, { status: 200 }) }
        moved = true
        return { response: new Response(null, { status: 204 }) }
      },
      get: async () => ({
        data: { id: sessionID, directory: moved ? "/worktree" : "/origin", metadata: managed ? { "concord.task_scope": "managed" } : {} },
        response: new Response(null, { status: 200 }),
      }),
      patch: async () => {
        managed = true
        return { response: new Response(null, { status: 200 }) }
      },
    })
    const runner = {
      async run(argv: string[]) {
        const command = argv[1]
        if (command === "project-resolve") return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", product_ids: ["product-1"], scope_version: "1", main_worktree: false }), stderr: "" }
        if (command === "work-bootstrap") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0", operation_id: "operation-1", replayed: false, product_id: "product-1", project_id: "project-1", work_id: "work-1", work_version: 1, worktree: { set_id: "set-1", path: "/worktree", branch: "work/work-1", base_sha: "a".repeat(40), state: "active" } }), stderr: "" }
        if (command === "session-prepare") return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0", agent: "agent-1", directory: "/worktree", product_id: "product-1", work_id: "work-1", title: "Work", prompt: "Do work" }), stderr: "" }
        return { exitCode: 0, stdout: JSON.stringify({ schema_version: "1.0", manifest_digest: "sha256:" + "0".repeat(64), request_id: "session-boundary-message-boundary", origin: "core", tool: "concord_product_view", operation: "portfolio", outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, error: { kind: "internal_error", retry_safe: false, recovery_action: { kind: "contact_operator" }, effect_state: "none" } }), stderr: "" }
      },
    }
    configureConcordAdapter({ runner })
    const result = await work_start.execute({ title: "Work", value_statement: "Start work", kind: "task", task: "Do work", idempotency_key: "start-1" }, context())
    if (typeof result === "string") throw new Error("work_start returned a string ToolResult")
    expect(JSON.parse(result.output).outcome, result.output).toBe("ok")
    await expectQuestionBlocked()
  })
})
