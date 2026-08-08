# Concord OpenCode adapter

Install `concord.ts` as a global OpenCode custom tool under
`~/.config/opencode/tools/concord.ts` (or project-local `.opencode/tools/`). Keep
the generated contract files beside it.

Grant bootstrap requires the OS Secret Service and `secret-tool`, with the
registered Ed25519 private key stored under the Concord credential identity.
The adapter never stores grants or keys in a workspace, arguments, logs, or
tool output. Missing credentials, an incompatible core, malformed stdout, or a
failed transport returns a typed failure and does not guess an effect.
