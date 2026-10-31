package store

import "strings"

// testOracleCommit pins one synthetic harness commit.
func testOracleCommit() string { return strings.Repeat("1c9f", 10) }

// itoa keeps fixture identities inside the bounded reference grammar
// without importing strconv into every reader of this file.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
