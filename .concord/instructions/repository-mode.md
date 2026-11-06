# Repository mode

Before you stage, commit or push in a repository, know the repository mode of
the Project you work in. Read it from the product view resolve read
(`projects[].repository_mode`) or `concord project resolve`.

On: `.concord/` and Concord's agent files are ordinary Product files. Commit
them like any other.

Off, or a repository the resolver cannot resolve: never stage, commit or push
`.concord/` or Concord's agent files such as `.opencode/opencode.json`. The
installer's link keeps those paths out of git through the clone's local
exclude; do not undo that by staging them anyway. An off-mode repository has no
Product law unless the operator designates a separate knowledge-home repository
for it (CD-0200 D1); otherwise follow these instructions and the repository's
own documentation (CD-0255).
