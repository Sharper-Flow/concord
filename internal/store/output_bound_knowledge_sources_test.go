package store

// Output-bound ledger rows for the knowledge family: the durable
// knowledge surface (domain registry, law, git notes, research
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
// schema maxima are redundant caps the parent removes. Rows of kind "unproved"
// are deliberate failures: the real derivation is named, and no safe source
// expresses it yet, so the row must stay RED rather than claim an invented
// bound.

func init() {
	// ------------------------------------------------------------------
	// workflow architecture binding (shared contract scope)
	// ------------------------------------------------------------------
	// The binding arrays are the admitted contract shape: approval admission
	// validates the shape (validateWorkflowArchitectureBindingShape), the
	// fold tables carry one row per admitted element, replay re-runs the
	// same shape validation on every decode, and
	// readWorkflowArchitectureBinding rebuilds each array one row per stored
	// element. There is no independent writer beside the admitted input, so
	// each row pairs the input-reachable node with the counting constant.

	// Affected Domain IDs: at most workflowArchitectureBindingMaxDomains
	// unique admitted IDs.
	registerOutputBound("/$defs/architecture_binding/properties/affected_domain_ids/maxItems",
		maxBound(
			inputBound("/$defs/architecture_binding/properties/affected_domain_ids/maxItems"),
			goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxDomains", boundItems),
		))

	// Domain modifications: same admitted shape, same counting constant.
	registerOutputBound("/$defs/architecture_binding/properties/domain_modifies/maxItems",
		maxBound(
			inputBound("/$defs/architecture_binding/properties/domain_modifies/maxItems"),
			goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxDomains", boundItems),
		))

	// Domain relation modifications: canonical tuples, one per admitted row.
	registerOutputBound("/$defs/architecture_binding/properties/domain_relation_modifies/maxItems",
		maxBound(
			inputBound("/$defs/architecture_binding/properties/domain_relation_modifies/maxItems"),
			goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxRelations", boundItems),
		))

	// Law additions: unique law IDs, one per admitted reservation-backed row.
	registerOutputBound("/$defs/architecture_binding/properties/law_additions/maxItems",
		maxBound(
			inputBound("/$defs/architecture_binding/properties/law_additions/maxItems"),
			goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxLawAdditions", boundItems),
		))

	// Verification obligations: unique law/obligation tuples.
	registerOutputBound("/$defs/architecture_binding/properties/verification_obligations/maxItems",
		maxBound(
			inputBound("/$defs/architecture_binding/properties/verification_obligations/maxItems"),
			goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxObligations", boundItems),
		))

	// The canonical_note_result (PM1.Q10) and knowledge_page (PM1.Q9) output
	// bounds retired with the knowledge search and its served-projection
	// surface (law-folder-authority.md, Committed reads: the law index and
	// derived projections confer no authority and are not retained); their
	// schema definitions are no longer output-reachable and carry no
	// registration.

	// ------------------------------------------------------------------
	// domain detail result (C22.DomainDetail)
	// ------------------------------------------------------------------

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

	// ------------------------------------------------------------------
	// domain ID primitive
	// ------------------------------------------------------------------

	// Every store producer of a domain_id-typed value enforces the same
	// 256: declared registry Domains carry the maxManifestID code-point
	// parse guard and the domains CHECK; workflow contract Domain tables
	// carry CHECK length 1..256 in the final DDL; archived Work Domain
	// rows carry the same CHECK; compaction scope arrays re-run
	// validManifestID on replay; research scope IDs are bounded at 256 by
	// an inline guard in validateResearchScopes. The two expressible
	// sources are composed; the rest agree at the same value.
	registerOutputBound("/$defs/domain_id/maxLength",
		maxBound(
			goInlineBound("internal/store/knowledge_record.go", "validManifestID", "utf8.RuneCountInString(value)", boundCharacters),
			goInlineBound("internal/store/research_operations.go", "validateResearchScopes", "utf8.RuneCountInString(id)", boundCharacters),
		))

	// ------------------------------------------------------------------
	// domain law record (embedded in C22.DomainDetail current law)
	// ------------------------------------------------------------------

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

	// ------------------------------------------------------------------
	// domain list result (C22.DomainList)
	// ------------------------------------------------------------------

	// The page is sliced to the admitted limit; domainListMaxLimit refuses
	// anything outside 1..100 before the query, and the registry itself
	// holds at most maxManifestDomains Domains.
	registerOutputBound("/$defs/domain_list_result/properties/domains/maxItems",
		goConstantBound("internal/store/domain_reads.go", "domainListMaxLimit", boundItems))

	// ------------------------------------------------------------------
	// domain observation (CD-0068, write-seam-only table)
	// ------------------------------------------------------------------

	// Ref items ride the fold validation only: validateObservationBounds
	// admits each ref up to 256 code points through its inline comparison pair,
	// and the domain_observations refs CHECK bounds only the array count.
	registerOutputBound("/$defs/domain_observation/properties/refs/items/maxLength",
		goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(ref)", boundCharacters))

	// The refs array is folded under the same count guard the CD-0030
	// work-observation fold applies before the row exists.
	registerOutputBound("/$defs/domain_observation/properties/refs/maxItems", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(refs)", boundItems))

	// The statement is folded under the same full-string code-point guard the
	// CD-0030 work-observation fold applies before the row exists.
	registerOutputBound("/$defs/domain_observation/properties/statement/maxLength", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(statement)", boundCharacters))

	// Tag items ride the same fold validation: at most 32 code points per tag.
	registerOutputBound("/$defs/domain_observation/properties/tags/items/maxLength",
		goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(tag)", boundCharacters))

	// The tags array is folded under the same count guard.
	registerOutputBound("/$defs/domain_observation/properties/tags/maxItems", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(tags)", boundItems))

	// ------------------------------------------------------------------
	// domain registry (embedded in C22 results)
	// ------------------------------------------------------------------

	// The registry content hash is stored under CHECK length 71 with the
	// sha256 prefix in the final DDL.
	registerOutputBound("/$defs/domain_registry/properties/content_hash/maxLength", pkHexDigestConstructionBound("internal/store/knowledge_record.go", "domainRegistryContentHash"))

	// The registry Product ID is the operation subject ID: products.id
	// carries no length CHECK and the product.created fold validates only
	// display and stage fields, so the producer is the shape admitted by
	// the shared id primitive on every operation input.
	registerOutputBound("/$defs/domain_registry/properties/product_id/maxLength",
		schemaBound("contracts/agent-tool-surface-payloads.schema.json", "/$defs/id/maxLength"))

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

	// ------------------------------------------------------------------
	// domain relation (embedded in C22.DomainDetail)
	// ------------------------------------------------------------------

	// Governing law IDs project one row per manifest relation entry, each
	// resolving to exactly one accepted law, and the manifest bounds each
	// relation's array at maxManifestArray entries; the consumer maximum
	// below must rise to admit it.
	registerOutputBound("/$defs/domain_relation/properties/governing_law_ids/maxItems",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestArray", boundItems))

	// ------------------------------------------------------------------
	// domain summary (embedded in C22 results)
	// ------------------------------------------------------------------

	// Domain names are stored under CHECK length 1..256 characters; the
	// manifest parse guard (maxManifestTitle code points) applies the same
	// value before projection.
	registerOutputBound("/$defs/domain_summary/properties/name/maxLength", goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Name)", boundCharacters))

	// Domain purposes are stored under CHECK length 1..4096 characters;
	// the manifest parse guard (maxManifestSummary code points) applies
	// the same value before projection.
	registerOutputBound("/$defs/domain_summary/properties/purpose/maxLength", goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Purpose)", boundCharacters))

	// ------------------------------------------------------------------
	// research finding (PM1 research content)
	// ------------------------------------------------------------------

	// The store write path inserts every declared citation with no count
	// guard and the link table carries no count CHECK, so the recorded
	// citation list is byte-ceiling only; the read limit is caller
	// supplied and refuses, never truncates, so it is not a producer
	// bound.
	registerOutputBound("/$defs/research_finding/properties/source_ids/maxItems",
		unboundedOutput("internal/store/research_mutations.go:RecordResearchFindingWithinTxUpsert inserts every req.Finding.SourceIDs entry with no count guard and active_research_finding_sources carries no count CHECK in the final DDL, so the recorded citation list grows with the write and projects verbatim", "internal/agent/runtime.go:readSizeRefusal"))

	// The finding statement has no upper bound on any writer: validateFinding
	// requires non-empty only (internal/store/research_operations.go:127-132),
	// active_research_findings CHECKs length > 0 only, and
	// copyResearchRevisionContent (research_operations.go:364) plus the
	// research rebuild carry retained rows to new revisions without
	// revalidation — the same raw persisted text the work-context view embeds
	// verbatim. Byte-ceiling only.
	registerOutputBound("/$defs/research_finding/properties/statement/maxLength",
		unboundedOutput("validateFinding (internal/store/research_operations.go:127-132) requires the statement non-empty only, active_research_findings CHECKs length > 0 only, and copyResearchRevisionContent (research_operations.go:364) plus the research rebuild carry retained rows without revalidation, so the persisted statement projects verbatim with no complete-text bound", "internal/agent/runtime.go:readSizeRefusal"))

	// ------------------------------------------------------------------
	// research pack (PM1.Q11)
	// ------------------------------------------------------------------

	// Consumer accepted_at values are RFC3339Nano UTC strings written at
	// the bind site in replaceResearchConsumerPinTx; the fixed layout
	// renders at most 35 characters.
	registerOutputBound("/$defs/research_pack/properties/consumers/items/properties/accepted_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_reliance.go", "replaceResearchConsumerPinTx", "time.RFC3339Nano"))

	// The consumers page holds at most the admitted read limit:
	// ReadResearchPack clamps req.Limit at 1000 and readResearchPackTx
	// refuses a page beyond the limit instead of truncating. The consumer
	// maximum below must rise to admit the 1000 the clamp admits.
	registerOutputBound("/$defs/research_pack/properties/consumers/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// Pack created_at is the RFC3339Nano UTC render at the create site.
	registerOutputBound("/$defs/research_pack/properties/created_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_mutations.go", "createResearchPackWithinRawTx", "time.RFC3339Nano"))

	// The revisions page holds at most the admitted read limit, clamped
	// and refused the same way as the consumers page.
	registerOutputBound("/$defs/research_pack/properties/revisions/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// Pack updated_at is the RFC3339Nano UTC render at the bump site.
	registerOutputBound("/$defs/research_pack/properties/updated_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_operations.go", "bumpResearchPack", "time.RFC3339Nano"))

	// ------------------------------------------------------------------
	// research pack descriptor (PM1.Q11 owner pages)
	// ------------------------------------------------------------------

	// Descriptor created_at reads active_research_packs.created_at,
	// written at the create site.
	registerOutputBound("/$defs/research_pack_descriptor/properties/created_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_mutations.go", "createResearchPackWithinRawTx", "time.RFC3339Nano"))

	// Descriptor updated_at reads active_research_packs.updated_at,
	// written at the bump site.
	registerOutputBound("/$defs/research_pack_descriptor/properties/updated_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_operations.go", "bumpResearchPack", "time.RFC3339Nano"))

	// ------------------------------------------------------------------
	// research read result (PM1.Q11)
	// ------------------------------------------------------------------

	// The v2 owner page holds at most the clamped limit:
	// ResearchPacksByOwner clamps limit at 1000 and truncates to it. The
	// consumer maximum below must rise to admit the clamp.
	registerOutputBound("/$defs/research_read_result/oneOf/2/properties/packs/maxItems",
		goInlineBound("internal/store/research_reads.go", "ResearchPacksByOwner", "limit", boundItems))

	// Consumer accepted_at in the v2 pack shape: the same bind-site render.
	registerOutputBound("/$defs/research_read_result/properties/consumers/items/properties/accepted_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_reliance.go", "replaceResearchConsumerPinTx", "time.RFC3339Nano"))

	// Consumers page in the v2 pack shape: the same clamp and refusal.
	registerOutputBound("/$defs/research_read_result/properties/consumers/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// Pack created_at in the v2 shape: the same create-site render.
	registerOutputBound("/$defs/research_read_result/properties/created_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_mutations.go", "createResearchPackWithinRawTx", "time.RFC3339Nano"))

	// The v2 owner packs page: the same clamp and slice.
	registerOutputBound("/$defs/research_read_result/properties/packs/maxItems",
		goInlineBound("internal/store/research_reads.go", "ResearchPacksByOwner", "limit", boundItems))

	// Revisions page: full pack reads refuse above the clamped limit and
	// single-revision selections return exactly one.
	registerOutputBound("/$defs/research_read_result/properties/revisions/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// Pack updated_at in the v2 shape: the same bump-site render.
	registerOutputBound("/$defs/research_read_result/properties/updated_at/maxLength",
		pkTimeLayoutWidthBound("internal/store/research_operations.go", "bumpResearchPack", "time.RFC3339Nano"))

	// ------------------------------------------------------------------
	// research retire result
	// ------------------------------------------------------------------

	// One result per admitted candidate in request order; the write
	// admission bounds candidates at 100 on research_retire's input
	// (uniqueItems) and the store refuses outside 1..100.
	registerOutputBound("/$defs/research_retire_result/properties/candidates/maxItems",
		schemaBound("contracts/agent-tool-surface-payloads.schema.json", "/$defs/work_define_research_retire_input/properties/candidates/maxItems"))

	// The research-retire result keeps its own evidence, separate from the
	// audit-reclaim rows: RetireResearchPacksResult declares no members for
	// either key, so marshaling the struct can never emit them.
	registerOutputBound("/$defs/research_retire_result/properties/next_valid_intents/maxItems",
		pkStructWithoutFieldBound("internal/store/research_retirement.go", "RetireResearchPacksResult", "next_valid_intents"))
	registerOutputBound("/$defs/research_retire_result/properties/work_pins/maxItems",
		pkStructWithoutFieldBound("internal/store/research_retirement.go", "RetireResearchPacksResult", "work_pins"))

	// ------------------------------------------------------------------
	// research revision (PM1.Q11 content)
	// ------------------------------------------------------------------

	// Revision created_at is the RFC3339Nano UTC render at the create and
	// append sites; both carry the same fixed layout.
	registerOutputBound("/$defs/research_revision/properties/created_at/maxLength",
		maxBound(
			pkTimeLayoutWidthBound("internal/store/research_mutations.go", "createResearchPackWithinRawTx", "time.RFC3339Nano"),
			pkTimeLayoutWidthBound("internal/store/research_mutations.go", "appendResearchRevisionWithinRawTx", "time.RFC3339Nano"),
		))

	// The findings page holds at most the clamped read limit and
	// readFindingsTx refuses beyond it.
	registerOutputBound("/$defs/research_revision/properties/findings/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// The revision question has no upper bound on any writer:
	// normalizeRevision requires question and method non-empty only
	// (internal/store/research_operations.go:74-76), the CHECK is length > 0
	// only, and carry-forward/rebuild re-project retained revisions without
	// revalidation. Byte-ceiling only.
	registerOutputBound("/$defs/research_revision/properties/question/maxLength",
		unboundedOutput("normalizeRevision (internal/store/research_operations.go:74-76) requires the question non-empty only, active_research_revisions CHECKs length > 0 only, and copyResearchRevisionContent plus the research rebuild carry retained revisions without revalidation, so the persisted question projects verbatim with no complete-text bound", "internal/agent/runtime.go:readSizeRefusal"))

	// The sources page holds at most the clamped read limit and
	// readSourcesTx refuses beyond it.
	registerOutputBound("/$defs/research_revision/properties/sources/maxItems",
		goInlineBound("internal/store/research_reads.go", "ReadResearchPack", "req.Limit", boundItems))

	// ------------------------------------------------------------------
	// research scopes (finding scope, read and natively embedded)
	// ------------------------------------------------------------------

	// The store guard validateResearchScopes refuses the 101st scope ID of
	// a kind through its inline comparison, so the store admits 100 per
	// kind and the retained twin the reads replay carries that maximum. The
	// wire admission on research_scopes_input bounds the agent surface only;
	// the shared definition keeps the cap packet dispatch admits.
	registerRetainedTwinBound("/$defs/research_scopes/properties/domain_ids/maxItems",
		goInlineBound("internal/store/research_operations.go", "validateResearchScopes", "len(*pair.values)", boundItems))
	registerRetainedTwinBound("/$defs/research_scopes/properties/product_ids/maxItems",
		goInlineBound("internal/store/research_operations.go", "validateResearchScopes", "len(*pair.values)", boundItems))
	registerRetainedTwinBound("/$defs/research_scopes/properties/project_ids/maxItems",
		goInlineBound("internal/store/research_operations.go", "validateResearchScopes", "len(*pair.values)", boundItems))
	registerRetainedTwinBound("/$defs/research_scopes/properties/tag_ids/maxItems",
		goInlineBound("internal/store/research_operations.go", "validateResearchScopes", "len(*pair.values)", boundItems))

	// ------------------------------------------------------------------
	// research source (PM1.Q11 content)
	// ------------------------------------------------------------------

	// Source fields are the retained record projected verbatim:
	// validateSource checks the text fields non-empty only and the table
	// CHECKs are length > 0, so no admission bounds any retained field,
	// and copyResearchRevisionContent plus the research rebuild carry
	// retained sources without revalidation; only the envelope byte
	// ceilings apply, shared by the read and mutation refusals.

	researchSourceFields := unboundedOutput("validateSource (internal/store/research_operations.go:133-138) checks the source text fields non-empty only, active_research_sources CHECKs length > 0 only, and copyResearchRevisionContent plus the research rebuild carry retained sources without revalidation, so the persisted text projects verbatim with no complete-text bound", "internal/agent/runtime.go:readSizeRefusal", "internal/agent/mutations.go:mutationResult")

	registerRetainedTwinBound("/$defs/research_source/properties/accessed_at/maxLength", researchSourceFields)

	registerRetainedTwinBound("/$defs/research_source/properties/locator/maxLength", researchSourceFields)

	registerRetainedTwinBound("/$defs/research_source/properties/published_at/maxLength", researchSourceFields)

	registerRetainedTwinBound("/$defs/research_source/properties/publisher_or_author/maxLength", researchSourceFields)

	registerRetainedTwinBound("/$defs/research_source/properties/title/maxLength", researchSourceFields)
}
