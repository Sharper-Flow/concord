import { expect, test } from "bun:test"
import { join } from "node:path"
import { runOwnedSuite } from "./owned-suite"

const routeDeclaration = Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0 ? test : test.skip

routeDeclaration("resumes clarification after a ticket exchange", async () => {
  const previous = process.env.TEST_CONCORD_TICKET_CASE
  process.env.TEST_CONCORD_TICKET_CASE = "^resumes clarification after a ticket exchange$"
  try {
    const result = await runOwnedSuite(join(import.meta.dir, "ticket_frontend_clarification_end_to_end.case.ts"))
    expect(result.exitCode, `${result.stdout}\n${result.stderr}`).toBe(0)
  } finally {
    if (previous === undefined) delete process.env.TEST_CONCORD_TICKET_CASE
    else process.env.TEST_CONCORD_TICKET_CASE = previous
  }
}, 300_000)
