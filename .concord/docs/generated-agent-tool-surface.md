# Generated Concord agent tool surface

Manifest digest: `sha256:e1f236ef79b7df614eb468a6c887d433d57c20477f5adabef45f11ab2aa450e9`
Payload schema digest: `sha256:939454e5660bb3d23082b737d74f7ffc03ef0415e82645cd41a5442ab1eb3624`
Envelope schema: `1.0`

| Operation | Kind | Query | Capability | Consequence | Availability |
|---|---|---|---|---|---|
| `concord_product_view.resolve` | `read` | `PM1.Q1` | `product_read` | `read` | `always` |
| `concord_product_view.snapshot` | `read` | `PM1.Q2` | `product_read` | `read` | `always` |
| `concord_work_browse.list` | `read` | `PM1.Q3` | `product_read` | `read` | `always` |
| `concord_work_browse.blocked` | `read` | `PM1.Q4` | `product_read` | `read` | `always` |
| `concord_work_browse.ready` | `read` | `PM1.Q5` | `product_read` | `read` | `always` |
| `concord_work_browse.scope` | `read` | `PM1.Q6` | `product_read` | `read` | `always` |
| `concord_work_trace.history` | `read` | `PM1.Q7` | `product_read` | `read` | `always` |
| `concord_work_trace.relations` | `read` | `PM1.Q8` | `product_read` | `read` | `always` |
| `concord_work_trace.continuity` | `read` | `C19.Continuity` | `product_read` | `read` | `always` |
| `concord_work_trace.mutation_result` | `read` | `C19.MutationResult` | `product_read` | `read` | `always` |
| `concord_work_trace.project_retirement` | `read` | `CD-0182.R1` | `product_read` | `read` | `always` |
| `concord_work_define.capture` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_define.revise_intent` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_define.issue_link_record` | `mutation` | `—` | `work_define` | `intent` | `always` |
| `concord_work_transition.lifecycle` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.remove` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.workflow_action` | `mutation` | `—` | `work_transition` | `workflow_action` | `always` |
| `concord_work_transition.project_handoff_record` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_transition.project_handoff_consume` | `mutation` | `—` | `work_transition` | `lifecycle` | `always` |
| `concord_work_relate.set_memberships` | `mutation` | `—` | `work_relate` | `scope` | `always` |
| `concord_work_relate.resolve_overlap` | `mutation` | `—` | `work_relate` | `relation` | `always` |
| `concord_domain.list` | `read` | `C22.DomainList` | `product_read` | `read` | `always` |
| `concord_domain.detail` | `read` | `C22.DomainDetail` | `product_read` | `read` | `always` |
