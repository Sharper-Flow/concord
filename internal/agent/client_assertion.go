package agent

import (
	"strconv"
	"strings"
)

// canonicalClientAssertion frames signed fields by UTF-8 byte length. The
// surface-specific prefix separates evidence writes from worker execution.
func canonicalClientAssertion(prefix string, names, values []string) []byte {
	var b strings.Builder
	b.WriteString(prefix)
	for i, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len([]byte(values[i]))))
		b.WriteByte(':')
		b.WriteString(values[i])
		b.WriteByte('|')
	}
	return []byte(b.String())
}
