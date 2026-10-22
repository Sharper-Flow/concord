package store

import (
	"context"
	"slices"
)

// The immutable lane identity, not the role name or capability class, grants
// repository edits. Historical non-editing Review identities stay read-only.
func workerLaneEdits(id string, version int64, digest string) bool {
	lane, err := LookupLane(id, version, digest)
	return err == nil && slices.Contains(lane.Capabilities, "edit_scoped_files") && (!slices.Contains(lane.Capabilities, "inspect_diff") || lane.Digest == digest)
}

func workerLaneRepairs(id string, version int64, digest string) bool {
	lane, err := LookupLane(id, version, digest)
	return err == nil && lane.Digest == digest && slices.Contains(lane.Capabilities, "edit_scoped_files") && slices.Contains(lane.Capabilities, "inspect_diff")
}

func workerAttemptLane(ctx context.Context, q queryer, attempt string) (LaneDefinition, error) {
	var id, digest string
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE attempt_id=?`, attempt).Scan(&id, &version, &digest); err != nil {
		return LaneDefinition{}, err
	}
	lane, err := LookupLane(id, version, digest)
	if err == nil && !workerLaneEdits(id, version, digest) {
		lane.Capabilities = slices.DeleteFunc(lane.Capabilities, func(c string) bool { return c == "edit_scoped_files" })
	}
	return lane, err
}
