package store

import (
	"fmt"
	"testing"
)

func CurrentEventPayloadVersion(kind string) int {
	registration, ok := registeredEventKind(kind)
	if !ok {
		panic(fmt.Sprintf("event %q is not registered", kind))
	}
	return registration.CurrentVersion
}

func TestCurrentEventPayloadVersionMatchesRegistry(t *testing.T) {
	for kind, registration := range eventKindRegistry {
		t.Run(kind, func(t *testing.T) {
			if got := CurrentEventPayloadVersion(kind); got != registration.CurrentVersion {
				t.Fatalf("payload version = %d, want registry version %d", got, registration.CurrentVersion)
			}
		})
	}
}

func TestCurrentEventPayloadVersionPanicsForUnregisteredKind(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unregistered event kind did not panic")
		}
	}()
	CurrentEventPayloadVersion("unregistered.event")
}
