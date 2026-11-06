// The typed fail-closed error for the adapter envelope contract.
// An adapter-origin envelope that fails the generated contract, or a factory
// call that names no admitted tuple, is a programming defect: it is thrown at
// the factory and at every adapter-origin host exit, rethrown unchanged by
// every catch that can receive it, and never serialized, relabeled as a
// transport failure, or mapped to a no-effect fallback. The class lives in
// its own import-free module, which concord.ts imports and re-exports for
// consumers to identify the same defect type. It is deliberately not an
// AdapterFailure: the failure classes feed envelope construction, while this
// defect reports that construction itself broke its contract.
export class AdapterContractDefect extends Error {
  constructor(message: string) {
    super(message)
    this.name = "AdapterContractDefect"
  }
}
