Context. CD-0183 D4 admits concord_work_transition.lifecycle with target completed on an item whose workflow instance is cancelled. The route exists to repair stranded items: items whose workflow cannot advance by any declared action. In September 2026, research on work-6eb8168e5bb75606218e8b2d found 196 items closed this way across two Products, about a third of all closes. Most of those items had a live workflow at proposal, reproduce, verify, or acceptance.

Cost. The lifecycle close skips record_verdict, confirm_premise, and complete. The item then carries no verdicts and no verified_criteria, so its completion states that the work is done without the evidence that proves it.

Rule. When the workflow is live, drive it to complete. Record the delivery, dispatch or run verification, record the verdicts, answer the premise confirmation, and call complete. The complete action closes the lifecycle for you.

Use the lifecycle close on a cancelled instance only when no declared action can advance the workflow. Examples are a definition with no exit from its step, an instance that a repair already cancelled, and an item that the core refuses on every route. Name that cause in the transition reason.

Detection. Every work pin carries cancelled_instance_closes, the count of items in the pinned item's primary Project with lifecycle completed and instance cancelled. A count that rises with each delivery means coordinators use the repair route as the normal route.