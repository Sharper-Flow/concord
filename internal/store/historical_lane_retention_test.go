package store

import "testing"

func TestRecordedReleasedLaneGenerationStillResolves(t *testing.T) {
	for id, digest := range map[string]string{
		"research":  "sha256:69f0271ffb9e14808aaf5d420e19f49928fe660a7336ad254f4f19486e90e2c3",
		"implement": "sha256:8e773bc93eea48493500b58f118deee9f91efb26a0213ea1e415cac7b716cbaa",
		"design":    "sha256:a2061098223cc1b9b31c62ed9ad2d92c97ef50c25f20802706fae026e4318804",
		"review":    "sha256:139ba516f8744a6bfc9d1b26bda766dd270d12625a7bcc4610894141d216c5a6",
		"verify":    "sha256:1fda1ccd5f1e9f9add9dc5ecbe6776be9d5a4c83a377edbd7e2b8b4aaf5dfb71",
	} {
		t.Run(id, func(t *testing.T) {
			lane, err := LookupLane(id, 1, digest)
			if err != nil || lane.ID != id || lane.Version != 1 {
				t.Fatalf("released lane %s:1 %s must resolve, got %+v: %v", id, digest, lane, err)
			}
		})
	}
}
