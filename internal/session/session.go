package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// EventSessionStarted records the creation of a new session log.
	EventSessionStarted = "session.started"

	// EventUserMessage records a user-authored message admitted to the
	// session.
	EventUserMessage = "message.user"

	// EventAssistantMessage records an assistant-authored message admitted
	// to the session.
	EventAssistantMessage = "message.assistant"

	// RoleUser identifies user messages in message event payloads.
	RoleUser = "user"

	// RoleAssistant identifies assistant messages in message event
	// payloads.
	RoleAssistant = "assistant"

	// ContentText identifies plain-text content parts in message payloads.
	ContentText = "text"

	// filePermissions keeps session logs readable only by the local user.
	filePermissions = 0o600

	// dirPermissions keeps session directories private by default.
	dirPermissions = 0o700
)

// Event is one durable JSONL record in a session log.
type Event struct {
	// Type identifies the event schema stored in Data.
	Type string `json:"type"`

	// ID is the stable unique identifier for this event.
	ID string `json:"id"`

	// ParentID links this event to the previous event in the current
	// branch.
	ParentID string `json:"parentId,omitempty"`

	// Time records when the event was created.
	Time time.Time `json:"time"`

	// Data stores the event-specific payload.
	Data json.RawMessage `json:"data"`
}

// StartedData is the payload stored in a session.started event.
type StartedData struct {
	// CWD records the working directory active when the session began.
	CWD string `json:"cwd"`
}

// ContentPart is one typed piece of message content.
type ContentPart struct {
	// Type identifies how Text should be interpreted.
	Type string `json:"type"`

	// Text stores the plain text for ContentText parts.
	Text string `json:"text"`
}

// MessageData is the payload stored in user and assistant message events.
type MessageData struct {
	// Role identifies the speaker that produced the message.
	Role string `json:"role"`

	// Content stores the ordered message parts for the speaker turn.
	Content []ContentPart `json:"content"`
}

// Store appends events to one session file and tracks the current leaf event.
type Store struct {
	path   string
	file   *os.File
	lastID string
}

// Create opens a new session log in dir and writes its session.started event.
func Create(dir string, cwd string) (*Store, *Event, error) {
	if err := os.MkdirAll(dir, dirPermissions); err != nil {
		return nil, nil, fmt.Errorf("create session dir: %w", err)
	}

	sessionID, err := NewID()
	if err != nil {
		return nil, nil, fmt.Errorf("create session id: %w", err)
	}

	path := filepath.Join(dir, sessionID+".jsonl")
	file, err := os.OpenFile(
		path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND,
		filePermissions,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create session file: %w", err)
	}

	store := &Store{
		path: path,
		file: file,
	}
	event, err := store.Append(EventSessionStarted, "", StartedData{
		CWD: cwd,
	})
	if err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return nil, nil, fmt.Errorf("append session start: "+
				"%w; close session file: %v", err, closeErr)
		}

		return nil, nil, fmt.Errorf("append session start: %w", err)
	}

	return store, event, nil
}

// Path returns the filesystem path backing the session log.
func (s *Store) Path() string {
	return s.path
}

// LastID returns the most recently appended event ID.
func (s *Store) LastID() string {
	return s.lastID
}

// Append writes one event to the log and advances the store's current leaf.
func (s *Store) Append(eventType string, parentID string, data any) (*Event,
	error) {

	id, err := NewID()
	if err != nil {
		return nil, fmt.Errorf("create event id: %w", err)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal event data: %w", err)
	}

	event := &Event{
		Type:     eventType,
		ID:       id,
		ParentID: parentID,
		Time:     time.Now().UTC(),
		Data:     raw,
	}
	if err := writeEvent(s.file, event); err != nil {
		return nil, err
	}

	s.lastID = event.ID

	return event, nil
}

// Close flushes and closes the underlying session file.
func (s *Store) Close() error {
	if s.file == nil {
		return nil
	}
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("close session file: %w", err)
	}
	s.file = nil

	return nil
}

// ReadAll loads every event from a JSONL session log path.
func ReadAll(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode session event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session log: %w", err)
	}

	return events, nil
}

// TextMessage creates a single-part plain text message payload.
func TextMessage(role string, text string) MessageData {
	return MessageData{
		Role: role,
		Content: []ContentPart{{
			Type: ContentText,
			Text: text,
		}},
	}
}

// NewID returns a random hex identifier suitable for session and event IDs.
func NewID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("read random id bytes: %w", err)
	}

	return hex.EncodeToString(bytes[:]), nil
}

// writeEvent appends one JSON-encoded event followed by a newline.
func writeEvent(file *os.File, event *Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}
