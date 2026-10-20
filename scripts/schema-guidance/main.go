// Command schema-guidance projects field teaching from a canonical schema.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func main() {
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		fail(err)
	}
	if err := payloadschema.AnnotateGuidance(document, os.Args[1:]); err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(document); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
