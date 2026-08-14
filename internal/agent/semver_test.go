package agent

import "testing"

func TestSemVerNegotiationIsNumericAndServerOwned(t *testing.T) {
	for _, value := range []string{"1", "1.0", "01.0.0", "1.0.0-rc"} {
		if _, err := ParseSemVer(value); err == nil {
			t.Fatalf("accepted invalid semver %q", value)
		}
	}
	if got, err := NegotiateSurfaceVersion("3.0.0-9.0.0"); err != nil || got != ManifestVersion {
		t.Fatalf("negotiated surface = %q, err=%v", got, err)
	}
	if _, err := NegotiateSurfaceVersion("1.0.0-1.0.0"); err == nil {
		t.Fatal("negotiated incompatible surface")
	}
	if _, err := NegotiateSurfaceVersion("1.10.0-1.11.0"); err == nil {
		t.Fatal("accepted lexical-only range")
	}
}

func TestEpicMajorRejectsV2Negotiation(t *testing.T) {
	if _, err := NegotiateSurfaceVersion("2.3.0-2.3.0"); err == nil {
		t.Fatal("v2 adapter negotiated across the Epic major boundary")
	}
}
