package main

import "testing"

// TestChatBusyInputDrainsSteeringPreservesPending verifies steering prompts
// can be consumed while slash commands remain queued for the chat loop.
func TestChatBusyInputDrainsSteeringPreservesPending(t *testing.T) {
	busy := &chatBusyInput{}
	busy.AddSteering("steer now")
	busy.AddPending(chatLineResult{
		Line: "/status",
		OK:   true,
	})
	busy.AddSteering("follow later")

	steering := busy.DrainSteering()
	if len(steering) != 2 {
		t.Fatalf("steering count = %d", len(steering))
	}
	if steering[0] != "steer now" || steering[1] != "follow later" {
		t.Fatalf("unexpected steering prompts: %#v", steering)
	}

	pending := busy.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d", len(pending))
	}
	if pending[0].Line != "/status" {
		t.Fatalf("unexpected pending result: %#v", pending[0])
	}
}
