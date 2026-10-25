package store

import "testing"

func TestCurrentPayloadRejectsRetiredResizeFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		payload string
	}{
		{"creation", "work.created", `{"work_kind":"task","title":"capture","priority":0,"resize_of":"work:old"}`},
		{"override", WorkSizeOverridden, `{"model_sequence":1,"size":"crucial","reason":"override","carried_from_event_id":"override:old"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := Event{EventID: "effect:retired-field", Kind: tc.kind, Payload: []byte(tc.payload)}
			assertFailureKind(t, eventKindRegistry[tc.kind].ValidatePayload(event), KindInvalidPayload)
		})
	}
}
