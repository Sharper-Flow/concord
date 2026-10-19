package store

// upcastEvent upcasts one event through its registered kind, as rebuild does.
func upcastEvent(event Event) (Event, error) {
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return Event{}, unknownEventKind(event.Kind)
	}
	return upcastEventWithRegistration(event, registration)
}
