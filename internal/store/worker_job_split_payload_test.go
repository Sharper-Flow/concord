package store

import (
	"strings"
	"testing"
)

func TestWorkerJobSplitPayloadShape(t *testing.T) {
	version, resulting := int64(1), int64(2)
	for _, tc := range []struct {
		name   string
		mutate func(*WorkerJobSplitPayload)
		want   string
	}{
		{"complete", func(*WorkerJobSplitPayload) {}, ""},
		{"duplicate allocation owner", func(p *WorkerJobSplitPayload) { p.Allocation = append(p.Allocation, p.Allocation[0]) }, "at most once"},
		{"duplicate path", func(p *WorkerJobSplitPayload) {
			p.Allocation[0].Paths = append(p.Allocation[0].Paths, p.Allocation[0].Paths[0])
		}, "paths must be unique"},
		{"duplicate evidence", func(p *WorkerJobSplitPayload) {
			p.Allocation[0].EvidenceRefs = append(p.Allocation[0].EvidenceRefs, p.Allocation[0].EvidenceRefs[0])
		}, "distinct references"},
		{"unrecorded allocation owner", func(p *WorkerJobSplitPayload) { p.Allocation[0].JobID = "job:other" }, "no other job"},
		{"single successor", func(p *WorkerJobSplitPayload) { p.Successors = p.Successors[:1] }, "2 to 64"},
		{"same successor twice", func(p *WorkerJobSplitPayload) { p.Successors[1] = p.Successors[0] }, "distinct new jobs"},
		{"predecessor successor", func(p *WorkerJobSplitPayload) { p.Successors[0] = p.Predecessor }, "none the predecessor"},
		{"later successor revision", func(p *WorkerJobSplitPayload) { p.Successors[0].Revision = 2 }, "revision 1"},
		{"bad base", func(p *WorkerJobSplitPayload) { p.BaseCommit = "main" }, "raw git commit oid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			payload := WorkerJobSplitPayload{
				WorkflowVersionFields: WorkflowVersionFields{WorkID: "split-shape", ExpectedVersion: &version, ResultingVersion: &resulting},
				Predecessor:           WorkerJobBinding{JobID: "job:predecessor", Revision: 1, Digest: digest},
				Successors:            []WorkerJobBinding{{JobID: "job:successor-a", Revision: 1, Digest: digest}, {JobID: "job:successor-b", Revision: 1, Digest: digest}},
				Allocation:            []WorkerJobSplitAllocation{{JobID: "job:successor-a", Paths: []string{"internal/store/worker_jobs.go"}, EvidenceRefs: []string{"evidence:allocation"}}},
			}
			tc.mutate(&payload)
			err := validateWorkerJobSplitPayload(Event{Kind: WorkerJobSplit, SubjectType: SubjectWorkItem, SubjectID: "split-shape"}, payload)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("shape error = %v, want %q", err, tc.want)
			}
		})
	}
}
