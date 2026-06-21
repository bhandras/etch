package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// IndexFileName is the session directory file that stores session
	// summaries.
	IndexFileName = "index.jsonl"

	// EventSessionStarted records the creation of a new session log.
	EventSessionStarted = "session.started"

	// EventUserMessage records a user-authored message admitted to the
	// session.
	EventUserMessage = "message.user"

	// EventAssistantMessage records an assistant-authored message admitted
	// to the session.
	EventAssistantMessage = "message.assistant"

	// EventToolMessage records a tool result admitted to the session.
	EventToolMessage = "message.tool"

	// EventContextSummary records an append-only compaction summary.
	EventContextSummary = "context.summary"

	// EventModelUsage records provider-reported token usage for one model
	// call.
	EventModelUsage = "model.usage"

	// EventModelResponse records provider response identity for one model
	// call.
	EventModelResponse = "model.response"

	// RoleUser identifies user messages in message event payloads.
	RoleUser = "user"

	// RoleAssistant identifies assistant messages in message event
	// payloads.
	RoleAssistant = "assistant"

	// RoleTool identifies tool result messages in message event payloads.
	RoleTool = "tool"

	// ContentText identifies plain-text content parts in message payloads.
	ContentText = "text"

	// filePermissions keeps session logs readable only by the local user.
	filePermissions = 0o600

	// dirPermissions keeps session directories private by default.
	dirPermissions = 0o700

	// titleLimit is the maximum number of runes kept in a session index
	// title.
	titleLimit = 60
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

	// ToolCalls stores tool calls requested by an assistant message.
	ToolCalls []ToolCallData `json:"toolCalls,omitempty"`

	// ToolCallID links a tool result back to its requested call.
	ToolCallID string `json:"toolCallId,omitempty"`

	// Name records the tool name for tool result messages.
	Name string `json:"name,omitempty"`
}

// ToolCallData is the durable session form of a model-requested tool call.
type ToolCallData struct {
	// ID is the provider-assigned call identifier.
	ID string `json:"id"`

	// Name is the tool name to execute.
	Name string `json:"name"`

	// Arguments stores the raw JSON argument object.
	Arguments string `json:"arguments"`
}

// SummaryData is the payload stored in a context.summary event.
type SummaryData struct {
	// Summary is the model-written checkpoint for older session history.
	Summary string `json:"summary"`

	// RangeStartID is the first event covered by the summary.
	RangeStartID string `json:"rangeStartId"`

	// RangeEndID is the last event covered by the summary.
	RangeEndID string `json:"rangeEndId"`

	// FirstKeptEventID is the first raw event retained after the summary.
	FirstKeptEventID string `json:"firstKeptEventId"`

	// Model records the summarization model name when known.
	Model string `json:"model,omitempty"`

	// Trigger records why compaction started, such as manual or auto.
	Trigger string `json:"trigger,omitempty"`

	// TokensBefore records the approximate projected context size before
	// compaction.
	TokensBefore int `json:"tokensBefore,omitempty"`

	// ReadFiles stores files observed through read-only file tools and not
	// later modified.
	ReadFiles []string `json:"readFiles,omitempty"`

	// ModifiedFiles stores files observed through mutation tools.
	ModifiedFiles []string `json:"modifiedFiles,omitempty"`
}

// UsageData is provider-reported token usage for one model call.
type UsageData struct {
	// InputTokens is the number of prompt or input tokens.
	InputTokens int `json:"inputTokens"`

	// CachedInputTokens is the subset of input tokens served from cache.
	CachedInputTokens int `json:"cachedInputTokens,omitempty"`

	// OutputTokens is the number of completion or output tokens.
	OutputTokens int `json:"outputTokens"`

	// ReasoningOutputTokens is the subset of output tokens used for hidden
	// reasoning when the provider reports it.
	ReasoningOutputTokens int `json:"reasoningOutputTokens,omitempty"`

	// TotalTokens is the provider-reported total token count.
	TotalTokens int `json:"totalTokens"`
}

// ResponseData is provider-reported response identity for one model call.
type ResponseData struct {
	// ProviderResponseID is the provider's stable response identifier when
	// it exposes one.
	ProviderResponseID string `json:"providerResponseId"`
}

// Add returns the element-wise sum of two usage counters.
func (u UsageData) Add(other UsageData) UsageData {
	return UsageData{
		InputTokens:       u.InputTokens + other.InputTokens,
		CachedInputTokens: u.CachedInputTokens + other.CachedInputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens +
			other.ReasoningOutputTokens,
		TotalTokens: u.TotalTokens + other.TotalTokens,
	}
}

// Empty reports whether usage contains no provider counters.
func (u UsageData) Empty() bool {
	return u.InputTokens == 0 && u.CachedInputTokens == 0 &&
		u.OutputTokens == 0 && u.ReasoningOutputTokens == 0 &&
		u.TotalTokens == 0
}

// IndexEntry is one summary row in the local session index.
type IndexEntry struct {
	// ID is the session identifier and JSONL file basename.
	ID string `json:"id"`

	// Path stores the session JSONL path.
	Path string `json:"path"`

	// CreatedAt records when the session file was created.
	CreatedAt time.Time `json:"createdAt"`

	// CWD records the working directory active when the session began.
	CWD string `json:"cwd"`

	// Title is a short human-readable label derived from the initial
	// prompt.
	Title string `json:"title"`
}

// Store appends events to one session file and tracks the current leaf event.
type Store struct {
	dir    string
	id     string
	path   string
	file   *os.File
	lastID string
}

// Create opens a new session log in dir, writes its session.started event, and
// appends a summary row to the local index.
func Create(dir string, cwd string, title string) (*Store, *Event, error) {
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
		dir:  dir,
		id:   sessionID,
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

	entry := IndexEntry{
		ID:        sessionID,
		Path:      path,
		CreatedAt: event.Time,
		CWD:       cwd,
		Title:     TitleFromPrompt(title),
	}
	if err := appendIndexEntry(dir, entry); err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return nil, nil, fmt.Errorf("append session index: "+
				"%w; close session file: %v", err, closeErr)
		}

		return nil, nil, fmt.Errorf("append session index: %w", err)
	}

	return store, event, nil
}

// Open opens an existing session log for appending and restores its last
// event as the current leaf.
func Open(path string) (*Store, []Event, error) {
	events, err := ReadAll(path)
	if err != nil {
		return nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, fmt.Errorf("session log is empty")
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, filePermissions)
	if err != nil {
		return nil, nil, fmt.Errorf("open session for append: %w", err)
	}

	store := &Store{
		dir: filepath.Dir(path),
		id: strings.TrimSuffix(
			filepath.Base(path), filepath.Ext(path),
		),
		path:   path,
		file:   file,
		lastID: events[len(events)-1].ID,
	}

	return store, events, nil
}

// ID returns the stable identifier for the session log.
func (s *Store) ID() string {
	return s.id
}

// Dir returns the directory containing the session log and index.
func (s *Store) Dir() string {
	return s.dir
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

// List reads the session index from dir and returns every known session entry.
func List(dir string) ([]IndexEntry, error) {
	file, err := os.Open(indexPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("open session index: %w", err)
	}
	defer file.Close()

	var entries []IndexEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry IndexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("decode session index entry: %w",
				err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session index: %w", err)
	}

	return entries, nil
}

// Resolve finds the one index entry whose ID has the supplied prefix.
func Resolve(dir string, prefix string) (*IndexEntry, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, fmt.Errorf("session id prefix must not be empty")
	}

	entries, err := List(dir)
	if err != nil {
		return nil, err
	}

	var matched *IndexEntry
	for i := range entries {
		if !strings.HasPrefix(entries[i].ID, prefix) {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("session id prefix %q is "+
				"ambiguous", prefix)
		}
		matched = &entries[i]
	}
	if matched == nil {
		return nil, fmt.Errorf("session id prefix %q not found", prefix)
	}

	return matched, nil
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

// AssistantToolCallMessage creates an assistant message carrying tool calls.
func AssistantToolCallMessage(text string, calls []ToolCallData) MessageData {
	return MessageData{
		Role: RoleAssistant,
		Content: []ContentPart{
			{
				Type: ContentText,
				Text: text,
			},
		},
		ToolCalls: calls,
	}
}

// ToolMessage creates a tool result message payload.
func ToolMessage(callID string, name string, text string) MessageData {
	return MessageData{
		Role:       RoleTool,
		ToolCallID: callID,
		Name:       name,
		Content: []ContentPart{
			{
				Type: ContentText,
				Text: text,
			},
		},
	}
}

// TitleFromPrompt creates the short title used in the session index.
func TitleFromPrompt(prompt string) string {
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" {
		return "untitled"
	}

	runes := []rune(title)
	if len(runes) <= titleLimit {
		return title
	}

	return string(runes[:titleLimit-3]) + "..."
}

// NewID returns a random hex identifier suitable for session and event IDs.
func NewID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("read random id bytes: %w", err)
	}

	return hex.EncodeToString(bytes[:]), nil
}

// indexPath returns the session index path for a session directory.
func indexPath(dir string) string {
	return filepath.Join(dir, IndexFileName)
}

// appendIndexEntry appends one summary row to the session index.
func appendIndexEntry(dir string, entry IndexEntry) error {
	file, err := os.OpenFile(
		indexPath(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		filePermissions,
	)
	if err != nil {
		return fmt.Errorf("open session index: %w", err)
	}
	defer file.Close()

	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal session index entry: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write session index entry: %w", err)
	}

	return nil
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
