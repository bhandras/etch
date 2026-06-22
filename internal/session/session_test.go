package session

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestCreateWritesSessionStart verifies that a new store immediately persists
// its durable session header.
func TestCreateWritesSessionStart(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project", "hello")
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

// TestCreateWithOptionsWritesRelationshipMetadata verifies child sessions keep
// enough durable metadata to be traced back to their parent task call.
func TestCreateWithOptionsWritesRelationshipMetadata(t *testing.T) {
	store, _, err := CreateWithOptions(t.TempDir(), CreateOptions{
		CWD:              "/work/project",
		Title:            "inspect parser",
		ParentSessionID:  "parent_session",
		ParentToolCallID: "call_task",
		SubagentProfile:  "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	events, err := ReadAll(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var data StartedData
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.ParentSessionID != "parent_session" ||
		data.ParentToolCallID != "call_task" ||
		data.SubagentProfile != "review" {

		t.Fatalf("unexpected start metadata: %#v", data)
	}
}

// TestAppendChainsMessageParents verifies that appended messages can form a
// parent-linked turn chain.
func TestAppendChainsMessageParents(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project", "hello")
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

// TestAppendSerializesConcurrentWriters verifies Store protects its JSONL file
// when future callers append from more than one goroutine.
func TestAppendSerializesConcurrentWriters(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project", "hello")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const appends = 16
	var wg sync.WaitGroup
	errs := make(chan error, appends)
	for i := 0; i < appends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Append(
				EventUserMessage, started.ID,
				TextMessage(RoleUser, "hello"),
			)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	events, err := ReadAll(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != appends+1 {
		t.Fatalf("expected %d events, got %d", appends+1, len(events))
	}
}

// TestOpenAppendsToExistingSession verifies that continuation reuses the
// existing log and parent chain.
func TestOpenAppendsToExistingSession(t *testing.T) {
	store, started, err := Create(t.TempDir(), "/work/project", "hello")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Append(
		EventUserMessage, started.ID, TextMessage(RoleUser, "hello"),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, events, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(events) != 2 {
		t.Fatalf("expected two loaded events, got %d", len(events))
	}
	if reopened.LastID() != user.ID {
		t.Fatalf("last id mismatch: want %q got %q", user.ID,
			reopened.LastID())
	}

	assistant, err := reopened.Append(
		EventAssistantMessage, reopened.LastID(),
		TextMessage(RoleAssistant, "hi"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if assistant.ParentID != user.ID {
		t.Fatalf("assistant parent mismatch: want %q got %q", user.ID,
			assistant.ParentID)
	}
}

// TestReadAllSkipsTornTrailingLine verifies a crash-truncated final JSONL row
// does not make the entire session unreadable.
func TestReadAllSkipsTornTrailingLine(t *testing.T) {
	store, _, err := Create(t.TempDir(), "/work/project", "hello")
	if err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, filePermissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"message.user"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected only intact event, got %d", len(events))
	}
}

// TestCreateAppendsIndexEntry verifies that session creation also records the
// summary metadata used by list and show commands.
func TestCreateAppendsIndexEntry(t *testing.T) {
	dir := t.TempDir()
	store, _, err := Create(dir, "/work/project", "hello from the index")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	entries, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one index entry, got %d", len(entries))
	}
	if entries[0].ID != store.ID() {
		t.Fatalf("index id mismatch: want %q got %q", store.ID(),
			entries[0].ID)
	}
	if entries[0].Path != store.Path() {
		t.Fatalf("index path mismatch: want %q got %q", store.Path(),
			entries[0].Path)
	}
	if entries[0].Title != "hello from the index" {
		t.Fatalf("unexpected title: %q", entries[0].Title)
	}
}

// TestResolveFindsUniquePrefix verifies that callers can use short session IDs
// when the prefix is unambiguous.
func TestResolveFindsUniquePrefix(t *testing.T) {
	dir := t.TempDir()
	store, _, err := Create(dir, "/work/project", "hello")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	entry, err := Resolve(dir, store.ID()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != store.ID() {
		t.Fatalf("resolve mismatch: want %q got %q", store.ID(),
			entry.ID)
	}
}

// TestTitleFromPromptNormalizesWhitespace verifies that titles are compact and
// bounded before they enter the local index.
func TestTitleFromPromptNormalizesWhitespace(t *testing.T) {
	title := TitleFromPrompt("  hello\n\nfrom\tprompt  ")
	if title != "hello from prompt" {
		t.Fatalf("unexpected title: %q", title)
	}

	long := TitleFromPrompt(strings.Repeat("x", titleLimit+20))
	if len([]rune(long)) != titleLimit {
		t.Fatalf("unexpected truncated length: %d", len([]rune(long)))
	}
	if long[len(long)-3:] != "..." {
		t.Fatalf("expected ellipsis suffix, got %q", long)
	}

	unicode := TitleFromPrompt(strings.Repeat("é", titleLimit))
	if !strings.HasSuffix(unicode, "...") {
		t.Fatalf("expected unicode title ellipsis, got %q", unicode)
	}
	if len(unicode) > titleLimit {
		t.Fatalf("unicode title exceeded byte limit: %d", len(unicode))
	}
}
