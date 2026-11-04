package store

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// canonicalJSON orders object keys, preserves numeric literals and rejects
// additional JSON values so comparisons retain their exact meaning.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty json")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("json carries more than one value")
	}
	return json.Marshal(value)
}
