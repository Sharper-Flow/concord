# Generated Concord agent tool surface

Source manifest: `3.7.0` / `sha256:b2a60d98e08de647ed5857243abada4accaa791309d85575bdfbaf0048b72c9b`
Payload schema digest: `sha256:a5feb75e8336ac17dc49fb49814814be1da31f96b911ebef3ed4136f9b59cad6`
Supported surface versions: `3.7.0`; envelope schema: `1.0`

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
| `concord_work_epic.create` | `mutation` | `—` | `work_epic` | `intent` | `always` |
| `concord_work_epic.add_entry` | `mutation` | `—` | `work_epic` | `relation` | `always` |
| `concord_work_epic.remove_entry` | `mutation` | `—` | `work_epic` | `relation` | `always` |
| `concord_work_epic.reorder_entry` | `mutation` | `—` | `work_epic` | `relation` | `always` |
| `concord_work_epic.change_requiredness` | `mutation` | `—` | `work_epic` | `relation` | `always` |
| `concord_work_epic.revise_narrative` | `mutation` | `—` | `work_epic` | `intent` | `always` |
| `concord_work_epic.entries` | `read` | `C21.EpicEntries` | `product_read` | `read` | `always` |
| `concord_work_transition.lifecycle` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.workflow_action` | `mutation` | `—` | `work_transition` | `workflow_action` | `workflow_definition` |
| `concord_work_transition.worktree_claim` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.worktree_reclaim` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_relate.set_memberships` | `mutation` | `—` | `work_relate` | `scope` | `always` |
| `concord_work_relate.link` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.unlink` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.supersede` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_relate.restore_superseded` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_relate.resource_claim` | `mutation` | `—` | `work_relate` | `claim` | `always` |
| `concord_work_relate.resource_release` | `mutation` | `—` | `work_relate` | `claim` | `always` |
| `concord_work_relate.message_send` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.message_withdraw` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_compact.publish` | `mutation` | `—` | `work_compact` | `publication` | `always` |
| `concord_work_compact.reconcile` | `mutation` | `—` | `work_compact` | `recovery` | `always` |
| `concord_work_compact.lesson_publish` | `mutation` | `—` | `work_compact` | `publication` | `always` |
