package model

import (
	"context"
	"fmt"
)

const (
	// RoleUser identifies user messages in model requests.
	RoleUser = "user"

	// RoleAssistant identifies assistant messages in model requests.
	RoleAssistant = "assistant"

	// EventTextDelta reports streamed assistant text.
	EventTextDelta = "text_delta"

	// EventDone reports that a stream completed normally.
	EventDone = "done"
)

// Message is one provider-neutral chat message.
type Message struct {
	// Role identifies the speaker that produced Content.
	Role string

	// Content stores the plain text for this first executable slice.
	Content string
}

// Request is the provider-neutral input passed to a model client.
type Request struct {
	// Messages contains the ordered model context for the turn.
	Messages []Message
}

// Event is one streamed model event emitted by a client.
type Event struct {
	// Type identifies the stream event kind.
	Type string

	// Text stores assistant text for EventTextDelta events.
	Text string
}

// Client streams model events for one request.
type Client interface {
	// Stream starts a model response for req and closes the returned
	// channel when no more events are available.
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// EchoClient is a deterministic model client that repeats the latest user
// message.
type EchoClient struct{}

// Stream emits the latest user message as a single text delta followed by done.
func (EchoClient) Stream(ctx context.Context, req Request) (<-chan Event,
	error) {

	text, err := latestUserMessage(req.Messages)
	if err != nil {
		return nil, err
	}

	events := make(chan Event, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			return

		case events <- Event{Type: EventTextDelta, Text: text}:
		}

		select {
		case <-ctx.Done():
			return

		case events <- Event{Type: EventDone}:
		}
	}()

	return events, nil
}

// latestUserMessage returns the last user-authored message from a request.
func latestUserMessage(messages []Message) (string, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Content, nil
		}
	}

	return "", fmt.Errorf("model request has no user message")
}
