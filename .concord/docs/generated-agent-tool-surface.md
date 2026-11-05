# Generated Concord agent tool surface

Manifest digest: `sha256:e3522e248de3202e19b5918973bae29cd51aecc72483ae3fdb33eaf735d45245`
Payload schema digest: `sha256:b28edd58339712ce2570a460d1361c25b1c8165e76b2fe73a64274a546bb5389`
Envelope schema: `1.0`

| Operation | Kind | Query | Capability | Consequence | Availability |
|---|---|---|---|---|---|
| `concord_product_view.resolve` | `read` | `PM1.Q1` | `product_read` | `read` | `always` |
| `concord_product_view.snapshot` | `read` | `PM1.Q2` | `product_read` | `read` | `always` |
| `concord_work_browse.list` | `read` | `PM1.Q3` | `product_read` | `read` | `always` |
| `concord_work_trace.history` | `read` | `PM1.Q7` | `product_read` | `read` | `always` |
| `concord_work_trace.relations` | `read` | `PM1.Q8` | `product_read` | `read` | `always` |
| `concord_work_trace.continuity` | `read` | `C19.Continuity` | `product_read` | `read` | `always` |
| `concord_work_trace.resume` | `read` | `C19.Resume` | `product_read` | `read` | `always` |
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
