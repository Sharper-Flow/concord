## Summary

<!-- What changed, and why? -->

Related to <issue key>

<!-- One non-closing line naming this work item's confirmed Linear issue
     (CD-0171 D8). Do not use a closing phrase such as Fixes: the Concord
     outbox is the only writer of issue status. verify-pr-linear-link
     refuses a work/work-* pull request without this line. -->

## Scope and authority impact

- [ ] This change preserves accepted Product law and contracts.
- [ ] Any consequential change has an issue or proposal linked below.
- Issue/proposal:

## Verification

- [ ] `gofmt -l .`
- [ ] `go vet ./...`
- [ ] `go test ./...` for the packages this change touches
- [ ] `python3 scripts/check-doc-links.py`
- [ ] `python3 scripts/check-public-content.py`
- [ ] `python3 scripts/check-json.py`

## Notes for reviewers

<!-- Risks, deferred work, migration notes, or public-content considerations. -->
