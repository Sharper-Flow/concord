package store

import (
	"database/sql/driver"

	sqlite "modernc.org/sqlite"
)

// RoutingPolicyManifestDigest is the routing-policy digest migration 43 freezes
// into the post-v43 worker_attempts.routing_policy_digest DEFAULT through the
// concord_routing_policy_manifest_digest() SQLite function. Concord performs
// no model resolution (CD-0058); the value is historical data that existing
// databases replay against.
const RoutingPolicyManifestDigest = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"concord_routing_policy_manifest_digest",
		0,
		func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return RoutingPolicyManifestDigest, nil
		},
	)
}
