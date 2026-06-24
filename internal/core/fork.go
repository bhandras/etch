package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"etch/internal/session"
)

// forkedHistoryForEvents prepends inherited parent context when events belong
// to a forked child session.
func forkedHistoryForEvents(events []session.Event) ([]session.Event, error) {
	fork, ok, err := forkMetadata(events)
	if err != nil || !ok {
		return events, err
	}
	parent, err := session.ReadAll(fork.ForkSessionPath)
	if err != nil {
		return nil, fmt.Errorf("read fork parent session %s: %w",
			fork.ForkSessionPath, err)
	}
	prefix, err := forkPrefix(parent, fork.ForkBeforeEventID)
	if err != nil {
		return nil, err
	}
	history := make([]session.Event, 0, len(prefix)+len(events))
	history = append(history, prefix...)
	history = append(history, events...)

	return history, nil
}

// forkMetadata returns the fork metadata stored on a session start event.
func forkMetadata(events []session.Event) (session.StartedData, bool, error) {
	if len(events) == 0 || events[0].Type != session.EventSessionStarted {
		return session.StartedData{}, false, nil
	}
	var started session.StartedData
	if err := json.Unmarshal(events[0].Data, &started); err != nil {
		return session.StartedData{}, false,
			fmt.Errorf("decode session start %s: %w", events[0].ID,
				err)
	}
	if strings.TrimSpace(started.ForkSessionPath) == "" ||
		strings.TrimSpace(started.ForkBeforeEventID) == "" {
		return session.StartedData{}, false, nil
	}

	return started, true, nil
}

// forkPrefix returns parent events before boundary that are safe to replay.
func forkPrefix(parent []session.Event,
	beforeEventID string) ([]session.Event, error) {

	var prefix []session.Event
	for _, event := range parent {
		if event.ID == beforeEventID {
			return prefix, nil
		}
		if forkInheritableEvent(event.Type) {
			prefix = append(prefix, event)
		}
	}

	return nil, fmt.Errorf("fork boundary event %s not found",
		beforeEventID)
}

// forkInheritableEvent reports whether event type belongs in child context.
func forkInheritableEvent(eventType string) bool {
	return session.IsMessageEvent(eventType) ||
		eventType == session.EventContextSummary
}
