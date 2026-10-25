package store

import (
	"context"
	"slices"
)

// The immutable lane identity, not the role name or capability class, grants
// repository edits. A stale Review identity cannot acquire current edit rights.
func workerLaneEdits(id string, digest string) bool {
	lane, err := LookupLane(id, digest)
	return err == nil && slices.Contains(lane.Capabilities, "edit_scoped_files") && (!slices.Contains(lane.Capabilities, "inspect_diff") || lane.Digest == digest)
}

func workerLaneRepairs(id string, digest string) bool {
	lane, err := LookupLane(id, digest)
	return err == nil && lane.Digest == digest && slices.Contains(lane.Capabilities, "edit_scoped_files") && slices.Contains(lane.Capabilities, "inspect_diff")
}

func workerAttemptLane(ctx context.Context, q queryer, attempt string) (LaneDefinition, error) {
	var id, digest string
	if err := q.QueryRowContext(ctx, `SELECT lane_id,lane_digest FROM worker_attempts WHERE attempt_id=?`, attempt).Scan(&id, &digest); err != nil {
		return LaneDefinition{}, err
	}
	lane, err := LookupLane(id, digest)
	if err == nil && !workerLaneEdits(id, digest) {
		lane.Capabilities = slices.DeleteFunc(lane.Capabilities, func(c string) bool { return c == "edit_scoped_files" })
	}
	return lane, err
}
