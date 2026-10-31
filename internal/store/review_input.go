package store

// ReviewInput carries the last recorded candidate and current obligations.
type ReviewInput struct {
	InputCandidate ReviewCandidate    `json:"input_candidate"`
	Dependencies   ReviewDependencies `json:"dependencies"`
}
