package model

import (
	"context"
	"testing"
)

// TestEchoClientStreamsLatestUserMessage verifies the fake provider behavior
// that the core relies on for dependency-free tests.
func TestEchoClientStreamsLatestUserMessage(t *testing.T) {
	client := EchoClient{}
	events, err := client.Stream(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Content: "first"},
			{Role: RoleAssistant, Content: "ignored"},
			{Role: RoleUser, Content: "second"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []Event
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 {
		t.Fatalf("expected two stream events, got %d", len(got))
	}
	if got[0].Type != EventTextDelta || got[0].Text != "second" {
		t.Fatalf("unexpected text event: %#v", got[0])
	}
	if got[1].Type != EventDone {
		t.Fatalf("unexpected done event: %#v", got[1])
	}
}

// TestEchoClientRejectsRequestsWithoutUserMessage keeps invalid request shape
// failures close to the model boundary.
func TestEchoClientRejectsRequestsWithoutUserMessage(t *testing.T) {
	client := EchoClient{}
	_, err := client.Stream(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected missing user message error")
	}
}
