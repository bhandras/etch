package main

import (
	"errors"
	"testing"
)

// TestChatBusyInputStopsSteeringAtPendingInput verifies slash commands and
// other pending inputs preserve user order by blocking later steering prompts.
func TestChatBusyInputStopsSteeringAtPendingInput(t *testing.T) {
	busy := &chatBusyInput{}
	busy.AddSteering("steer now")
	busy.AddPending(chatLineResult{
		Line: "/status",
		OK:   true,
	})
	busy.AddSteering("follow later")

	steering := busy.DrainSteering()
	if len(steering) != 1 {
		t.Fatalf("steering count = %d", len(steering))
	}
	if steering[0] != "steer now" {
		t.Fatalf("unexpected steering prompts: %#v", steering)
	}

	pending := busy.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending count = %d", len(pending))
	}
	if pending[0].Line != "/status" || pending[1].Line != "follow later" {
		t.Fatalf("unexpected pending results: %#v", pending)
	}
}

// TestCollectBusyChatInputCancelsOnlyOnEscape verifies active turn collection
// keeps Ctrl+C as a pending chat interruption while ESC cancels active work.
func TestCollectBusyChatInputCancelsOnlyOnEscape(t *testing.T) {
	busy := &chatBusyInput{}
	if collectBusyChatInput(chatLineResult{
		Err: errChatInputCanceled,
	}, busy) != true {

		t.Fatalf("escape cancellation was not reported")
	}
	if pending := busy.Pending(); len(pending) != 0 {
		t.Fatalf("escape cancellation was queued: %#v", pending)
	}

	if collectBusyChatInput(chatLineResult{
		Err: errChatInputInterrupted,
	}, busy) != false {

		t.Fatalf("Ctrl+C interruption canceled active work")
	}
	pending := busy.Pending()
	if len(pending) != 1 ||
		!errors.Is(pending[0].Err, errChatInputInterrupted) {

		t.Fatalf("Ctrl+C interruption was not preserved: %#v", pending)
	}
}
