package store

// Output-bound ledger rows for the knowledge family: the durable
// knowledge surface (domain registry, law, git notes,
// packs, architecture bindings of contract scope, and domain observations).
//
// Every finite row cites a live source: a published schema node, a Go
// constant, a full-string Go guard or construction, the shared derivation
// helpers, or the pk* family-local derivations defined in
// output_bound_pages_sources_test.go. SQLite length(TEXT) counts a
// NUL-terminated prefix, so it bounds no complete output string here; the
// one retained JSON-array citation (workflow_contracts.law_modifies) is a
// json_valid + json_array_length CHECK over the complete parsed value.
// Composite rows use maxBound only for alternative writers of the same value
// and sumBound only for values assembled from parts. Rows registered as
// "unbounded, byte-ceiling only" name the producer that accumulates without a
// finite guard and the typed refusal that owns the envelope ceiling; their
// schema maxima are redundant caps the parent removes.

func init() {
	// Current law appends every accepted law homed in the domain with no
	// read-side LIMIT. The projection admits at most maxManifestRecords
	// accepted law records and law_domain_homes holds one home per law, so
	// the true capacity is maxManifestRecords items; the consumer maximum
	// below must rise to admit it or the read needs its own bound.
	registerOutputBound("/$defs/domain_detail_result/properties/current_law/maxItems",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestRecords", boundItems))

	// Observations: the declared open window is enforced on the write fold
	// (the 65th open row refuses), on replay, and as the read clamp passed
	// to observationsForDomain.
	registerOutputBound("/$defs/domain_detail_result/properties/observations/maxItems",
		goConstantBound("internal/store/domain_observations.go", "DomainObservationOpenWindow", boundItems))

	// Relations appends every domain_architecture_relations row touching the
	// domain, source or target, with no read-side cap. Each registry domain
	// declares at most maxManifestRelations relations and the registry
	// admits maxManifestDomains domains, so a hub domain can carry the
	// product of the two constants; the consumer maximum below must rise to
	// admit it.
	registerOutputBound("/$defs/domain_detail_result/properties/relations/maxItems",
		pkProductBound(
			goConstantBound("internal/store/knowledge_record.go", "maxManifestDomains", boundItems),
			goConstantBound("internal/store/knowledge_record.go", "maxManifestRelations", boundItems),
		))

	// Every store producer of a domain_id-typed value enforces the same
	// 256-character bound through registry and storage validation.
	registerOutputBound("/$defs/domain_id/maxLength",
		goInlineBound("internal/store/knowledge_record.go", "validManifestID", "utf8.RuneCountInString(value)", boundCharacters))

	// Applicability rows project 1:1 from the manifest record's
	// applies_to_domain_ids, which validateManifestStringArray bounds at
	// maxManifestArray unique declared Domains.
	registerOutputBound("/$defs/domain_law_record/properties/applies_to/maxItems",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestArray", boundItems))

	// Law record paths project 1:1 from manifest records bounded at
	// maxManifestPath code points.
	registerOutputBound("/$defs/domain_law_record/properties/path/maxLength",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestPath", boundCharacters))

	// Law record titles project 1:1 from manifest records bounded at
	// maxManifestTitle code points.
	registerOutputBound("/$defs/domain_law_record/properties/title/maxLength",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestTitle", boundCharacters))

	// The page is sliced to the admitted limit; domainListMaxLimit refuses
	// anything outside 1..100 before the query, and the registry itself
	// holds at most maxManifestDomains Domains.
	registerOutputBound("/$defs/domain_list_result/properties/domains/maxItems",
		goConstantBound("internal/store/domain_reads.go", "domainListMaxLimit", boundItems))

	// domain observation (CD-0068, write-seam-only table)

	// Ref items ride the fold validation only: validateObservationBounds
	// admits each ref up to 256 code points through its inline comparison pair,
	// and the domain_observations refs CHECK bounds only the array count.
	registerOutputBound("/$defs/domain_observation/properties/refs/items/maxLength",
		goInlineBound("internal/store/domain_observations.go", "validateObservationBounds", "utf8.RuneCountInString(ref)", boundCharacters))

	// The refs array is folded under its count guard before the row exists.
	registerOutputBound("/$defs/domain_observation/properties/refs/maxItems", goInlineBound("internal/store/domain_observations.go", "validateObservationBounds", "len(refs)", boundItems))

	// The statement is folded under its full-string code-point guard before
	// the row exists.
	registerOutputBound("/$defs/domain_observation/properties/statement/maxLength", goInlineBound("internal/store/domain_observations.go", "validateObservationBounds", "utf8.RuneCountInString(statement)", boundCharacters))

	// Tag items ride the same fold validation: at most 32 code points per tag.
	registerOutputBound("/$defs/domain_observation/properties/tags/items/maxLength",
		goInlineBound("internal/store/domain_observations.go", "validateObservationBounds", "utf8.RuneCountInString(tag)", boundCharacters))

	// The tags array is folded under the same count guard.
	registerOutputBound("/$defs/domain_observation/properties/tags/maxItems", goInlineBound("internal/store/domain_observations.go", "validateObservationBounds", "len(tags)", boundItems))

	// The registry content hash is stored under CHECK length 71 with the
	// sha256 prefix in the final DDL.
	registerOutputBound("/$defs/domain_registry/properties/content_hash/maxLength", pkHexDigestConstructionBound("internal/store/knowledge_record.go", "domainRegistryContentHash"))

	// The registry Product ID is the operation subject ID: products.id
	// carries no length CHECK and the product.created fold validates only
	// display and stage fields, so the producer is the shape admitted by
	// the shared id primitive on every operation input.
	registerOutputBound("/$defs/domain_registry/properties/product_id/maxLength",
		schemaBound("/$defs/id/maxLength"))

	// The registry product key is stored under CHECK length 1..256; the
	// manifest parse guard validProductKey bounds it tighter at 64.
	registerOutputBound("/$defs/domain_registry/properties/product_key/maxLength", goInlineBound("internal/store/knowledge_record.go", "validProductKey", "len(value)", boundBytes))

	// The root Domain ID joins domains through the composite foreign key,
	// whose CHECK bounds it at 256 characters; the manifest derives it as
	// "product-root:" + productKey.
	registerOutputBound("/$defs/domain_registry/properties/root_domain_id/maxLength", sumBound(pkLiteralTextBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRegistry", "product-root:"), goInlineBound("internal/store/knowledge_record.go", "validProductKey", "len(value)", boundBytes)))

	// The scanned commit is the resolved git head OID: validateCommitOID
	// admits exactly the 40- and 64-hex lengths, and the column carries no
	// CHECK, so the validator is the producer bound.
	registerOutputBound("/$defs/domain_registry/properties/scanned_commit_oid/maxLength",
		pkLengthSetBound("internal/store/git_knowledge.go", "validateCommitOID", "len(oid)"))

	// Governing law IDs project one row per manifest relation entry, each
	// resolving to exactly one accepted law, and the manifest bounds each
	// relation's array at maxManifestArray entries; the consumer maximum
	// below must rise to admit it.
	registerOutputBound("/$defs/domain_relation/properties/governing_law_ids/maxItems",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestArray", boundItems))

	// Domain names are stored under CHECK length 1..256 characters; the
	// manifest parse guard (maxManifestTitle code points) applies the same
	// value before projection.
	registerOutputBound("/$defs/domain_summary/properties/name/maxLength", goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Name)", boundCharacters))

	// Domain purposes are stored under CHECK length 1..4096 characters;
	// the manifest parse guard (maxManifestSummary code points) applies
	// the same value before projection.
	registerOutputBound("/$defs/domain_summary/properties/purpose/maxLength", goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Purpose)", boundCharacters))
}
