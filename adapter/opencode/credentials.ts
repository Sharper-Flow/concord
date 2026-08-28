import { createPrivateKey } from "node:crypto"

// Credential access for the adapter's Ed25519 client identity. It lives apart
// from the tool adapter because worker-evidence assertions use it in dispatch.ts.
// Keeping it dependency-free means the lane dispatcher does not pull the plugin
// runtime in to sign an assertion.

export interface CredentialStore { getPrivateKey(clientRef: string): Promise<Uint8Array> }

export class SecretToolCredentialStore implements CredentialStore {
  async getPrivateKey(clientRef: string): Promise<Uint8Array> {
    const child = Bun.spawn(["secret-tool", "lookup", "service", "concord", "account", clientRef], { stdin: "ignore", stdout: "pipe", stderr: "pipe" })
    const output = (await new Response(child.stdout).text()).trim()
    const code = await child.exited
    if (code !== 0 || !output) throw new Error("credential service unavailable")
    const value = output.replace(/^base64:/, "")
    try { return Uint8Array.from(Buffer.from(value, "base64")) } catch { throw new Error("credential value is not valid base64") }
  }
}

export function privateKeyObject(raw: Uint8Array) {
  if (raw.length !== 32) throw new Error("credential is not an Ed25519 seed")
  const prefix = Buffer.from("302e020100300506032b657004220420", "hex")
  return createPrivateKey({ key: Buffer.concat([prefix, Buffer.from(raw)]), format: "der", type: "pkcs8" })
}

export function b64(value: Uint8Array) { return Buffer.from(value).toString("base64") }

export function randomNonce() { return crypto.randomUUID().replaceAll("-", "") + crypto.randomUUID().replaceAll("-", "") }

export function clientRef() { return process.env.CONCORD_CLIENT_REF ?? "opencode" }
