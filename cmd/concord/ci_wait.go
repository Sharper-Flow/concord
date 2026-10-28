package main

import (
	"io"

	"github.com/sharper-flow/concord/internal/ciwait"
)

func runCiWait(raw []byte, out, errOut io.Writer) int {
	var request ciwait.Request
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "ci-wait", diagnosticText(err))
		return 1
	}
	return ciwait.Run(request, out)
}
