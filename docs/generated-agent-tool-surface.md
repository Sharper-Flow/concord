# Generated Concord agent tool surface

Manifest digest: `sha256:c148344bbf011768ed2fb5c563cae339f17d4525bf53183215b3b77073f954b0`
Payload schema digest: `sha256:c0e28fe7d047993b8af9b9620601e326e519b900746cd54e2718de39ddb9f211`
Envelope schema: `1.0`

| Operation | Kind | Query | Capability | Consequence | Availability |
|---|---|---|---|---|---|
| `concord_product_view.resolve` | `read` | `PM1.Q1` | `product_read` | `read` | `always` |
| `concord_product_view.snapshot` | `read` | `PM1.Q2` | `product_read` | `read` | `always` |
| `concord_product_view.portfolio` | `read` | `C14.ProductRows` | `product_read` | `read` | `always` |
| `concord_product_view.blocked_sessions` | `read` | `PM1.Q12` | `product_read` | `read` | `always` |
| `concord_work_browse.list` | `read` | `PM1.Q3` | `product_read` | `read` | `always` |
| `concord_work_browse.blocked` | `read` | `PM1.Q4` | `product_read` | `read` | `always` |
| `concord_work_browse.ready` | `read` | `PM1.Q5` | `product_read` | `read` | `always` |
| `concord_work_browse.scope` | `read` | `PM1.Q6` | `product_read` | `read` | `always` |
| `concord_work_browse.resource_claims` | `read` | `PM1.Q13` | `product_read` | `read` | `always` |
| `concord_work_browse.messages` | `read` | `PM1.Q14` | `product_read` | `read` | `always` |
| `concord_work_trace.history` | `read` | `PM1.Q7` | `product_read` | `read` | `always` |
| `concord_work_trace.relations` | `read` | `PM1.Q8` | `product_read` | `read` | `always` |
| `concord_work_trace.continuity` | `read` | `C19.Continuity` | `product_read` | `read` | `always` |
| `concord_work_trace.research` | `read` | `PM1.Q11` | `product_read` | `read` | `always` |
| `concord_knowledge.search` | `read` | `PM1.Q9` | `product_read` | `read` | `always` |
| `concord_knowledge.resolve_note` | `read` | `PM1.Q10` | `product_read` | `read` | `always` |
| `concord_work_define.capture` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_define.revise_intent` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_define.research_pack_create` | `mutation` | `—` | `research` | `research` | `always` |
| `concord_work_define.research_revision_append` | `mutation` | `—` | `research` | `research` | `always` |
| `concord_work_define.research_finding_record` | `mutation` | `—` | `research` | `research` | `always` |
| `concord_work_define.research_source_record` | `mutation` | `—` | `research` | `research` | `always` |
| `concord_work_define.research_freshness_set` | `mutation` | `—` | `research` | `research` | `always` |
| `concord_work_define.observation_record` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_initiative.create` | `mutation` | `—` | `work_initiative` | `intent` | `always` |
| `concord_work_initiative.add_entry` | `mutation` | `—` | `work_initiative` | `relation` | `always` |
| `concord_work_initiative.remove_entry` | `mutation` | `—` | `work_initiative` | `relation` | `always` |
| `concord_work_initiative.reorder_entry` | `mutation` | `—` | `work_initiative` | `relation` | `always` |
| `concord_work_initiative.change_requiredness` | `mutation` | `—` | `work_initiative` | `relation` | `always` |
| `concord_work_initiative.revise_narrative` | `mutation` | `—` | `work_initiative` | `intent` | `always` |
| `concord_work_initiative.entries` | `read` | `C21.InitiativeEntries` | `product_read` | `read` | `always` |
| `concord_work_transition.lifecycle` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.workflow_action` | `mutation` | `—` | `work_transition` | `workflow_action` | `workflow_definition` |
| `concord_work_transition.worktree_claim` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.worktree_reclaim` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_relate.set_memberships` | `mutation` | `—` | `work_relate` | `scope` | `always` |
| `concord_work_relate.link` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.unlink` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.supersede` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_relate.restore_superseded` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_relate.resolve_overlap` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.resource_claim` | `mutation` | `—` | `work_relate` | `claim` | `always` |
| `concord_work_relate.resource_release` | `mutation` | `—` | `work_relate` | `claim` | `always` |
| `concord_work_relate.message_send` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.message_withdraw` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_compact.publish` | `mutation` | `—` | `work_compact` | `publication` | `always` |
| `concord_work_compact.reconcile` | `mutation` | `—` | `work_compact` | `recovery` | `always` |
| `concord_work_compact.lesson_publish` | `mutation` | `—` | `work_compact` | `publication` | `always` |
| `concord_domain.list` | `read` | `C22.DomainList` | `product_read` | `read` | `always` |
| `concord_domain.detail` | `read` | `C22.DomainDetail` | `product_read` | `read` | `always` |
| `concord_domain.active_work` | `read` | `C22.DomainActiveWork` | `product_read` | `read` | `always` |
| `concord_domain.attachments` | `read` | `C22.DomainAttachments` | `product_read` | `read` | `always` |
| `concord_domain.overlaps` | `read` | `C22.DomainOverlaps` | `product_read` | `read` | `always` |
