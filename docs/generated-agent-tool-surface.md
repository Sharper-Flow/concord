# Generated Concord agent tool surface

Source manifest: `2.3.0` / `sha256:a6c8354d4797ea89515f9311a3c5423f549269e3b4f203de1b0976872cf1c6fa`
Payload schema digest: `sha256:5643b0f69e4d4d0e5567f6540aa0bb9e5477a27f3292edd5413e6586e87bd5a4`
Supported surface versions: `2.3.0`; envelope schema: `1.0`

| Operation | Kind | Query | Capability | Consequence | Availability |
|---|---|---|---|---|---|
| `concord_product_view.resolve` | `read` | `PM1.Q1` | `product_read` | `read` | `always` |
| `concord_product_view.snapshot` | `read` | `PM1.Q2` | `product_read` | `read` | `always` |
| `concord_product_view.portfolio` | `read` | `C14.ProductRows` | `product_read` | `read` | `always` |
| `concord_work_browse.list` | `read` | `PM1.Q3` | `product_read` | `read` | `always` |
| `concord_work_browse.blocked` | `read` | `PM1.Q4` | `product_read` | `read` | `always` |
| `concord_work_browse.ready` | `read` | `PM1.Q5` | `product_read` | `read` | `always` |
| `concord_work_browse.scope` | `read` | `PM1.Q6` | `product_read` | `read` | `always` |
| `concord_work_trace.history` | `read` | `PM1.Q7` | `product_read` | `read` | `always` |
| `concord_work_trace.relations` | `read` | `PM1.Q8` | `product_read` | `read` | `always` |
| `concord_work_trace.continuity` | `read` | `C19.Continuity` | `product_read` | `read` | `always` |
| `concord_knowledge.search` | `read` | `PM1.Q9` | `product_read` | `read` | `always` |
| `concord_knowledge.resolve_note` | `read` | `PM1.Q10` | `product_read` | `read` | `always` |
| `concord_work_define.capture` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_define.revise_intent` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_transition.lifecycle` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.workflow_action` | `mutation` | `—` | `work_transition` | `workflow_action` | `workflow_definition` |
| `concord_work_relate.set_memberships` | `mutation` | `—` | `work_relate` | `scope` | `always` |
| `concord_work_relate.link` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.unlink` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_work_relate.supersede` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_relate.restore_superseded` | `mutation` | `—` | `work_relate` | `supersession` | `always` |
| `concord_work_compact.publish` | `mutation` | `—` | `work_compact` | `publication` | `always` |
| `concord_work_compact.reconcile` | `mutation` | `—` | `work_compact` | `recovery` | `always` |
