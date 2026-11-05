# Generated Concord agent tool surface

<<<<<<< HEAD
<<<<<<< HEAD
Manifest digest: `sha256:aae7f5479d895a1abca94384a956be16f2592952519fce867d9771069d92b3b5`
Payload schema digest: `sha256:c28eb92fbb1dbfda7413ff1c5577c9b539eaf6c038191f923edf63eac5b3586f`
=======
Manifest digest: `sha256:5efe42b21bb323a37391068a9c29b1bedb19bd394ea4d6e54b55c026bb61edc1`
Payload schema digest: `sha256:3bc08ba6fb7a9b61063c3c7ce854622b367f8292fe08699541ceff4624d3963b`
>>>>>>> 7d719c31f (chore(contracts): regenerate agent tool surface after CON-1738 rebase)
=======
Manifest digest: `sha256:7999bcd47359fd1050fe99f37e0407e3b664945b9ac9c60411f60e29460f80aa`
Payload schema digest: `sha256:4da0c93a7ec97395b7e2fedfb74f0d5d084998ee8273a5e6107e6c521342f200`
>>>>>>> 7c5f10f33 (fix(store): complete clarify fixtures, one-code-point answers and contract prose)
Envelope schema: `1.0`

| Operation | Kind | Query | Capability | Consequence | Availability |
|---|---|---|---|---|---|
| `concord_product_view.resolve` | `read` | `PM1.Q1` | `product_read` | `read` | `always` |
| `concord_product_view.snapshot` | `read` | `PM1.Q2` | `product_read` | `read` | `always` |
| `concord_work_browse.list` | `read` | `PM1.Q3` | `product_read` | `read` | `always` |
| `concord_work_trace.history` | `read` | `PM1.Q7` | `product_read` | `read` | `always` |
| `concord_work_trace.relations` | `read` | `PM1.Q8` | `product_read` | `read` | `always` |
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
