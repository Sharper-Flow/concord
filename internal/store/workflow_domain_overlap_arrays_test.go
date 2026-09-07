package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDomainOverlapIntersectionArrays(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  []string
		right []string
		want  string
	}{
		{name: "nil", want: "[]"},
		{name: "empty", left: []string{}, right: []string{}, want: "[]"},
		{name: "disjoint", left: []string{"law:left"}, right: []string{"law:right"}, want: "[]"},
		{name: "shared", left: []string{"law:b", "law:a"}, right: []string{"law:b", "law:a", "law:a"}, want: `["law:a","law:b"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := workflowOverlapFootprint{ProductID: "product", WorkID: "left", ContractVersion: 1, AffectedDomains: []string{"domain"}, LawWrites: tc.left, DomainModifications: tc.left}
			right := workflowOverlapFootprint{ProductID: "product", WorkID: "right", ContractVersion: 1, AffectedDomains: []string{"domain"}, LawWrites: tc.right, DomainModifications: tc.right}
			overlap, ok := workflowDomainOverlapPair(left, right)
			if !ok {
				t.Fatal("shared domain did not produce an overlap")
			}
			failure := &DomainOverlapFailure{Overlaps: []WorkflowDomainOverlap{overlap}}
			boundWorkflowDomainOverlapFailure(failure)
			raw, err := json.Marshal(failure.Overlaps[0])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for field, want := range map[string]string{
				"shared_affected_domain_ids":  `["domain"]`,
				"shared_law_ids":              tc.want,
				"shared_domain_modifications": tc.want,
				"shared_relation_tuples":      "[]",
			} {
				if string(fields[field]) != want {
					t.Errorf("%s = %s, want %s", field, fields[field], want)
				}
			}
		})
	}
}
