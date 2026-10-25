package store

// The immutable job descriptor and its retained source contract own the
// dependency snapshot. Compare only named inputs with the active contract;
// changing its version, unrelated predicates or registry hash is not drift.
// JSON trees compare payload content independently of object-key order.
// Selection, browse and ordinary/member dispatch share the job alias j.
const workerJobDependenciesCurrentPredicate = `j.recorded_payload IS NOT NULL
  AND EXISTS (SELECT 1 FROM workflow_contracts source
    WHERE source.work_id=j.work_id AND source.contract_version=j.contract_version)
  AND EXISTS (SELECT 1 FROM workflow_contracts active
    WHERE active.work_id=j.work_id AND active.superseded_by IS NULL
    AND NOT EXISTS (SELECT 1 FROM workflow_contracts other
      WHERE other.work_id=active.work_id AND other.superseded_by IS NULL AND other.contract_version<>active.contract_version)
    AND NOT EXISTS (
      SELECT 1 FROM (
        SELECT value AS predicate_id FROM json_each(j.predicate_ids)
        UNION SELECT p.value FROM json_each(j.recorded_payload,'$.acceptance_oracle.owners') owner, json_each(owner.value,'$.predicate_ids') p
        UNION SELECT p.value FROM json_each(j.recorded_payload,'$.acceptance_oracle.controls') control, json_each(control.value,'$.predicate_ids') p
      ) named
      WHERE NOT EXISTS (
        SELECT 1 FROM workflow_contract_predicates original
        JOIN workflow_contract_predicates current ON current.work_id=original.work_id
          AND current.contract_version=active.contract_version AND current.predicate_id=original.predicate_id
        WHERE original.work_id=j.work_id AND original.contract_version=j.contract_version
          AND original.predicate_id=named.predicate_id AND current.outcome_kind=original.outcome_kind
          AND NOT EXISTS (SELECT fullkey,type,atom FROM json_tree(original.outcome_payload)
            EXCEPT SELECT fullkey,type,atom FROM json_tree(current.outcome_payload))
          AND NOT EXISTS (SELECT fullkey,type,atom FROM json_tree(current.outcome_payload)
            EXCEPT SELECT fullkey,type,atom FROM json_tree(original.outcome_payload))
      )
    )
    AND NOT EXISTS (
      SELECT 1 FROM json_each(j.recorded_payload,'$.acceptance_oracle.owners') owner
      WHERE NOT EXISTS (
        SELECT 1 FROM workflow_architecture_bindings binding
        JOIN workflow_contract_affected_domains affected USING(work_id,contract_version)
        JOIN domain_registries registry ON registry.product_id=binding.product_id
        JOIN domains domain ON domain.product_id=registry.product_id AND domain.registry_content_hash=registry.content_hash
        WHERE binding.work_id=j.work_id AND binding.contract_version=active.contract_version
          AND affected.domain_id=json_extract(owner.value,'$.domain_id')
          AND domain.domain_id=affected.domain_id AND domain.status='current'
      )
      OR EXISTS (
        SELECT 1 FROM json_each(owner.value,'$.law_bindings') law
        WHERE NOT EXISTS (
          SELECT 1 FROM workflow_contract_law_revisions pin
          WHERE pin.work_id=j.work_id AND pin.contract_version=active.contract_version
            AND pin.law_id=json_extract(law.value,'$.source.law_id')
            AND pin.content_hash=json_extract(law.value,'$.source.content_hash')
        )
      )
    )
  )`
