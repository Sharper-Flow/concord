package store

import (
	"database/sql/driver"

	sqlite "modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"concord_routing_policy_manifest_digest",
		0,
		func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return RoutingPolicyManifestDigest, nil
		},
	)
}
