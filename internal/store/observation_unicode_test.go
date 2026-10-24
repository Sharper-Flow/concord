package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestObservationBoundsAgreeWithSchemaCodePoints(t *testing.T) {
	for _, char := range []string{"x", "é", "😀"} {
		for _, field := range []struct {
			name string
			max  int
		}{{"statement", 512}, {"refs", 256}, {"tags", 32}} {
			for _, count := range []int{0, 1, field.max/4 + 1, field.max/2 + 1, field.max, field.max + 1} {
				t.Run(fmt.Sprintf("%s/%s/%d", char, field.name, count), func(t *testing.T) {
					statement, refs, tags := "noticed", []string(nil), []string(nil)
					value := strings.Repeat(char, count)
					switch field.name {
					case "statement":
						statement = value
					case "refs":
						refs = []string{value}
					case "tags":
						tags = []string{value}
					}
					input := map[string]any{"work_id": "work-unicode", "idempotency_key": "unicode", "statement": statement}
					if refs != nil {
						input["refs"] = refs
					}
					if tags != nil {
						input["tags"] = tags
					}
					raw, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					wantOK := count > 0 && count <= field.max
					if err := payloadschema.Validate("work_define_observation_record_input", raw); (err == nil) != wantOK {
						t.Fatalf("schema: %v; want accepted=%v", err, wantOK)
					}
					if err := validateObservationBounds(statement, refs, tags); (err == nil) != wantOK {
						t.Fatalf("store: %v; want accepted=%v (%d code points, %d bytes)", err, wantOK, count, len(value))
					}
				})
			}
		}
	}
}

func TestObservationCollectionBoundsAgreeWithSchema(t *testing.T) {
	for _, field := range []struct {
		name string
		max  int
	}{{"refs", 16}, {"tags", 8}} {
		for _, count := range []int{0, field.max, field.max + 1} {
			t.Run(fmt.Sprintf("%s/%d", field.name, count), func(t *testing.T) {
				values := make([]string, count)
				for i := range values {
					values[i] = fmt.Sprintf("é😀%d", i)
				}
				input := map[string]any{"work_id": "work-unicode", "idempotency_key": "unicode", "statement": "noticed", field.name: values}
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				wantOK := count <= field.max
				if err := payloadschema.Validate("work_define_observation_record_input", raw); (err == nil) != wantOK {
					t.Fatalf("schema: %v; want accepted=%v", err, wantOK)
				}
				var refs, tags []string
				if field.name == "refs" {
					refs = values
				} else {
					tags = values
				}
				if err := validateObservationBounds("noticed", refs, tags); (err == nil) != wantOK {
					t.Fatalf("store: %v; want accepted=%v", err, wantOK)
				}
			})
		}
	}
}

func TestObservationUnicodeAnchorsPersistAndRefuseWithoutEffects(t *testing.T) {
	for _, anchor := range []string{"work", "domain"} {
		t.Run(anchor, func(t *testing.T) {
			var s *Store
			if anchor == "domain" {
				s = domainObservationFixture(t)
			} else {
				s = observationFixture(t)
			}
			ctx := context.Background()
			statement := strings.Repeat("😀", 512)
			refs, tags := []string{strings.Repeat("é", 256)}, []string{strings.Repeat("😀", 32)}
			for i, field := range []string{"valid", "statement", "refs", "tags", "refs_count", "tags_count"} {
				payload := map[string]any{"observation_id": fmt.Sprintf("obs:%016x", i), "statement": statement, "refs": refs, "tags": tags}
				event := Event{EventID: fmt.Sprintf("unicode-%d", i), Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "agent", OccurredAt: time.Unix(int64(i+10), 0).UTC(), PayloadVersion: 1}
				if anchor == "domain" {
					payload["observation_id"], payload["product_id"], payload["domain_id"] = observationID(i), "obs-product", "obs-domain"
					event.Kind, event.SubjectType, event.SubjectID = "domain.observation_recorded", SubjectProduct, "obs-product"
				}
				switch field {
				case "statement":
					payload[field] = statement + "é"
				case "refs":
					payload[field] = []string{refs[0] + "😀"}
				case "tags":
					payload[field] = []string{tags[0] + "é"}
				case "refs_count", "tags_count":
					name, count := "refs", 17
					if field == "tags_count" {
						name, count = "tags", 9
					}
					values := make([]string, count)
					for j := range values {
						values[j] = fmt.Sprintf("é😀%d", j)
					}
					payload[name] = values
				}
				var err error
				event.Payload, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				err = foldTestEvent(t, s, event)
				if (err == nil) != (field == "valid") {
					t.Fatalf("%s: %v", field, err)
				}
			}
			if anchor == "work" {
				page, err := s.ReadWorkObservations(ctx, WorkObservationsRequest{WorkID: "work-99"})
				if err != nil || len(page.Observations) != 1 {
					t.Fatalf("page=%+v err=%v", page, err)
				}
				o := page.Observations[0]
				if o.Statement != statement || !reflect.DeepEqual(o.Refs, refs) || !reflect.DeepEqual(o.Tags, tags) {
					t.Fatal("work observation changed its Unicode content")
				}
			} else {
				rows, err := observationsForDomain(ctx, s.db, "obs-product", "obs-domain", 10)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				o := rows[0]
				if o.Statement != statement || !reflect.DeepEqual(o.Refs, refs) || !reflect.DeepEqual(o.Tags, tags) {
					t.Fatal("Domain observation changed its Unicode content")
				}
			}
		})
	}
}
