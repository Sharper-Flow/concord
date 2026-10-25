package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const remoteLocatorUpgradeName = "project_locator_remote_identity_v2"

type remoteLocatorChange struct {
	locator ProjectLocator
	target  string
	version int64
}

func preflightRemoteLocatorUpgrade(ctx context.Context, tx *sql.Tx) error {
	var present, applied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='project_locators')`).Scan(&present); err != nil || !present {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=?)`, remoteLocatorUpgradeName).Scan(&applied); err != nil || applied {
		return err
	}
	_, err := planRemoteLocatorUpgrade(ctx, tx)
	return err
}

func planRemoteLocatorUpgrade(ctx context.Context, tx *sql.Tx) ([]remoteLocatorChange, error) {
	rows, err := tx.QueryContext(ctx, `SELECT l.locator_id,l.project_id,l.kind,l.locator_value,l.normalized_value,p.version FROM project_locators l LEFT JOIN projects p ON p.id=l.project_id ORDER BY l.locator_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	remaining := map[string]int{}
	occupied, targets := map[string]string{}, map[string]string{}
	var ordered, pending []remoteLocatorChange
	for rows.Next() {
		var c remoteLocatorChange
		l := &c.locator
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Kind, &l.Value, &l.NormalizedValue, &c.version); err != nil {
			return nil, err
		}
		remaining[l.ProjectID]++
		if l.Kind != LocatorGitRemote {
			continue
		}
		legacy, err := normalizeRemoteVersion(l.Value, 1)
		if err != nil || legacy != l.NormalizedValue {
			return nil, newFailure(KindInvalidPayload, "migrate", "recorded remote normalization differs for locator "+l.ID, false, "repair the locator with the active release before upgrade")
		}
		c.target, err = normalizeRemote(l.Value)
		if err != nil {
			return nil, wrapFailure(KindInvalidFilter, "migrate", "invalid remote for locator "+l.ID+" of Project "+l.ProjectID, false, "repair the locator with the active release before upgrade", err)
		}
		if c.target == "" {
			remaining[l.ProjectID]--
			ordered = append(ordered, c)
			continue
		}
		if err := checkRemoteCredentials(l.Value); err != nil {
			return nil, wrapFailure(KindInvalidFilter, "migrate", "invalid remote for locator "+l.ID+" of Project "+l.ProjectID, false, "remove credentials from the locator with the active release before upgrade", err)
		}
		if other := targets[c.target]; other != "" {
			return nil, newFailure(KindMembershipConflict, "migrate", "remote target collision between locators "+other+" and "+l.ID, false, "resolve the locator collision with the active release")
		}
		targets[c.target], occupied[l.NormalizedValue] = l.ID, l.ID
		pending = append(pending, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for project, count := range remaining {
		if count == 0 {
			return nil, newFailure(KindProjectionConflict, "migrate", "remote relinquishment would remove the last locator of Project "+project, false, "register a canonical_path locator with the active release before upgrade")
		}
	}
	for len(pending) > 0 {
		index := -1
		for i, c := range pending {
			if owner := occupied[c.target]; owner == "" || owner == c.locator.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, newFailure(KindMembershipConflict, "migrate", "remote key dependencies cannot be ordered", false, "resolve the locator collision with the active release")
		}
		c := pending[index]
		ordered = append(ordered, c)
		delete(occupied, c.locator.NormalizedValue)
		occupied[c.target] = c.locator.ID
		pending = append(pending[:index], pending[index+1:]...)
	}
	return ordered, nil
}

func upgradeRemoteLocators(ctx context.Context, tx *sql.Tx, at time.Time) error {
	plan, err := planRemoteLocatorUpgrade(ctx, tx)
	if err != nil || len(plan) == 0 {
		return err
	}
	op := Operation{ExpectedVersions: map[SubjectRef]int64{}}
	versions := map[string]int64{}
	for _, c := range plan {
		l := c.locator
		if versions[l.ProjectID] == 0 {
			versions[l.ProjectID] = c.version
			op.ExpectedVersions[VersionRef(SubjectProject, l.ProjectID)] = c.version
		}
		version := versions[l.ProjectID]
		p := projectLocatorPayload{ProjectID: l.ProjectID, LocatorID: l.ID, ExpectedVersion: version, ResultingVersion: version + 1}
		kind, payloadVersion := "project.locator_removed", 1
		if c.target != "" {
			kind, payloadVersion = "project.locator_updated", 2
			p.Kind, p.Value, p.NormalizedValue = l.Kind, l.Value, c.target
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		op.Events = append(op.Events, Event{EventID: remoteLocatorUpgradeName + ":" + l.ID, Kind: kind, SubjectType: SubjectProject, SubjectID: l.ProjectID, Actor: "operator", OccurredAt: at, PayloadVersion: payloadVersion, Payload: payload})
		versions[l.ProjectID]++
	}
	if _, err := applyOperationTx(ctx, tx, op, newWriteScope(tx), false); err != nil {
		return err
	}
	var legacy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_locators WHERE kind='git_remote' AND normalization_version=1)`).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		return newFailure(KindProjectionConflict, "migrate", "remote identity upgrade left a legacy locator", false, "repair the locator before retrying upgrade")
	}
	return nil
}
