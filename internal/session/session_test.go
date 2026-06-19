package session

import (
	"encoding/json"
	"testing"
)

// TestCreateWritesSessionStart verifies that a new store immediately persists
// its durable session header.
func TestCreateWritesSessionStart(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if started.Type != EventSessionStarted {
		t.Fatalf("expected session start event, got %q", started.Type)
	}
	if store.LastID() != started.ID {
		t.Fatalf("last id mismatch: want %q got %q", started.ID,
			store.LastID())
	}

	events, err := ReadAll(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}

	var data StartedData
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.CWD != "/work/project" {
		t.Fatalf("unexpected cwd: %q", data.CWD)
	}
}

// TestAppendChainsMessageParents verifies that appended messages can form a
// parent-linked turn chain.
func TestAppendChainsMessageParents(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	user, err := store.Append(
		EventUserMessage, started.ID, TextMessage(RoleUser, "hello"),
	)
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := store.Append(
		EventAssistantMessage, user.ID,
		TextMessage(RoleAssistant, "hello"),
	)
	if err != nil {
		t.Fatal(err)
	}

	events, err := ReadAll(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected three events, got %d", len(events))
	}
	if events[1].ParentID != started.ID {
		t.Fatalf("user parent mismatch: want %q got %q", started.ID,
			events[1].ParentID)
	}
	if events[2].ParentID != user.ID {
		t.Fatalf("assistant parent mismatch: want %q got %q", user.ID,
			events[2].ParentID)
	}
	if store.LastID() != assistant.ID {
		t.Fatalf("last id mismatch: want %q got %q", assistant.ID,
			store.LastID())
	}
}
