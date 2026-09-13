# Records

A record is anything durable that someone opens later: a work item, a ticket, a
commit message, a pull request body, a review comment, a decision note. A
conversation ends with the session. A record outlives it, and the reader who
opens it does not hold the context you hold now.

Name what the reader can open. Give the path, the symbol, the command, the
identifier, or the error text. "The validator rejected the change" sends nobody
anywhere. Name the validator and quote what it printed, and the reader goes
straight to it.

State what you observed before you state what it means. An observation is
checkable and a conclusion is not. A reader who doubts your conclusion can still
use your observation, so the record keeps its worth even when you were wrong.

Prefer the verb to the noun built from it. Write "the check refused the request"
rather than "refusal of the request occurred at the check". A noun made from a
verb drops the actor, and the reader cannot tell who did what to what.

Keep each sentence under about forty words, and give each thing one name
throughout. A record that calls the same file two names reads as two files.

Report the cost, the risk, and your own error in the record itself, not only in
the conversation where you found them. The reader who needs that warning most is
the one who arrives after the conversation is gone.

Leave quoted material exactly as it came: errors, log lines, commands, paths,
identifiers, and code. A paraphrased error string is no longer searchable, and
the reader cannot match it against what they see.
