package store

func init() {
	registerOutputBound("/$defs/worker_oracle_law_binding/properties/clause/maxLength", goConstantBound("internal/store/acceptance_oracle.go", "oracleDescriptionMaxBytes", boundBytes))
}
