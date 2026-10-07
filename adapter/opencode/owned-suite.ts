// Thin-launcher support: run one assertion-bearing .case.ts suite under the
// external owner (fixture-root-owner.py). The launcher holds the owner's
// stdin pipe open for its whole life, so if the launcher process dies by any
// path — timeout, teardown exception, signal, SIGKILL — the pipe closes and
// the owner sees EOF and cleans up on its own. The owner forwards the inner
// run's assertion output through, and its own exit status preserves the inner
// run's status (including 130/143 signal exits) unless a removal failure
// after a passing inner run must fail the launcher instead.
import { join } from "node:path"

export interface OwnedSuiteResult {
  exitCode: number
  stdout: string
  stderr: string
}

async function tee(stream: ReadableStream<Uint8Array> | undefined, write: (text: string) => void): Promise<string> {
  if (!stream) return ""
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let text = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    const chunk = decoder.decode(value, { stream: true })
    text += chunk
    write(chunk)
  }
  return text
}

// Spawn the owner for one case file and await its composed status. The
// launcher's stdin pipe stays open for the lifetime of this process; nothing
// here closes it.
export async function runOwnedSuite(caseFile: string): Promise<OwnedSuiteResult> {
  const owner = Bun.spawn(["python3", join(import.meta.dir, "fixture-root-owner.py"), caseFile], {
    cwd: import.meta.dir,
    env: { ...process.env, CONCORD_OWNER_BUN: process.execPath },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exited] = await Promise.all([
    tee(owner.stdout, (text) => process.stdout.write(text)),
    tee(owner.stderr, (text) => process.stderr.write(text)),
    owner.exited,
  ])
  const exitCode = exited ?? (owner.signalCode ? 128 : -1)
  return { exitCode, stdout, stderr }
}
