package main

import "sync"

// chatBusyInput stores input submitted while a model turn is running.
type chatBusyInput struct {
	// mu serializes prompt collection from chat with core steering drains.
	mu sync.Mutex

	// entries stores prompts and commands in the order the user submitted
	// them while the model turn was active.
	entries []chatBusyEntry
}

// chatBusyEntry is one queued input item captured during active work.
type chatBusyEntry struct {
	// result stores slash commands that should be replayed after the turn.
	result chatLineResult

	// steering reports whether line is eligible for in-turn steering.
	steering bool

	// consumed reports whether core already admitted this steering prompt.
	consumed bool
}

// AddSteering records a user prompt that may steer the active turn.
func (b *chatBusyInput) AddSteering(line string) {
	b.mu.Lock()
	b.entries = append(b.entries, chatBusyEntry{
		result: chatLineResult{
			Line: line,
			OK:   true,
		},
		steering: true,
	})
	b.mu.Unlock()
}

// AddPending records input that should be processed after the active turn.
func (b *chatBusyInput) AddPending(result chatLineResult) {
	b.mu.Lock()
	b.entries = append(b.entries, chatBusyEntry{
		result: result,
	})
	b.mu.Unlock()
}

// DrainSteering returns unconsumed steering prompts for the next model call.
func (b *chatBusyInput) DrainSteering() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var prompts []string
	for index := range b.entries {
		entry := &b.entries[index]
		if entry.consumed {
			continue
		}
		if !entry.steering {
			break
		}
		entry.consumed = true
		prompts = append(prompts, entry.result.Line)
	}

	return prompts
}

// Pending returns inputs that were not admitted as steering during the turn.
func (b *chatBusyInput) Pending() []chatLineResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	var results []chatLineResult
	for _, entry := range b.entries {
		if entry.consumed {
			continue
		}
		results = append(results, entry.result)
	}

	return results
}
