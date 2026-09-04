package main

import (
	"bytes"
	"encoding/json"
)

// The CLI's required-field table is the boundary the adapter writes across, and
// until now it existed only as Go values and rendered help text. An adapter that
// omitted a required field learned so at runtime, from a live evidence write
// that refused after the worker had already run — the failure mode behind issue
// #781's successor. Projecting the table into a committed contract lets the
// adapter's own tests check their payloads before release, and the golden test
// beside this file keeps the projection equal to commandSpecs.
const commandContractPath = "contracts/cli-commands.v1.json"

const commandContractSchemaVersion = "1.0"

type commandContractField struct {
	Name   string   `json:"name"`
	Nested []string `json:"nested,omitempty"`
}

type commandContractEntry struct {
	Canonical      string                 `json:"canonical"`
	TwoWord        string                 `json:"two_word,omitempty"`
	RequiredFields []commandContractField `json:"required_fields"`
}

type commandContract struct {
	SchemaVersion string                 `json:"schema_version"`
	Commands      []commandContractEntry `json:"commands"`
}

// commandContractProjection derives the contract from commandSpecs. It reads the
// same table the router and the help text read, so a command that gains or loses
// a required field changes this projection in the same commit.
func commandContractProjection() commandContract {
	commands := make([]commandContractEntry, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		fields := make([]commandContractField, 0, len(spec.RequiredFields))
		for _, required := range spec.RequiredFields {
			fields = append(fields, commandContractField{Name: required.Name, Nested: required.Nested})
		}
		commands = append(commands, commandContractEntry{Canonical: spec.Canonical, TwoWord: spec.TwoWord, RequiredFields: fields})
	}
	return commandContract{SchemaVersion: commandContractSchemaVersion, Commands: commands}
}

// commandContractBytes renders the projection exactly as the committed file
// stores it, so the golden comparison is a byte comparison.
func commandContractBytes() ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(commandContractProjection()); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
