package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/zeebo/xxh3"
)

type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
	TodoStatusFailed     TodoStatus = "failed"
)

type Mode string

const (
	ModeExecute Mode = "execute"
	ModePlan    Mode = "plan"
)

func (mode Mode) IsPlan() bool {
	return mode == ModePlan
}

// HashID returns the XXH3 hash of a session ID (UUID) as a hex string.
func HashID(id string) string {
	h := xxh3.New()
	h.WriteString(id)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type Todo struct {
	Content     string     `json:"content"`
	Status      TodoStatus `json:"status"`
	ActiveForm  string     `json:"active_form"`
	CompletedAt int64      `json:"completed_at,omitempty"`
}

// HasIncompleteTodos returns true if there are any non-completed todos.
func HasIncompleteTodos(todos []Todo) bool {
	for _, todo := range todos {
		if todo.Status != TodoStatusCompleted {
			return true
		}
	}
	return false
}

type Session struct {
	ID               string
	ParentSessionID  string
	Title            string
	Mode             Mode
	MessageCount     int64
	PromptTokens     int64
	CompletionTokens int64
	SummaryMessageID string
	Cost             float64
	Todos            []Todo
	CreatedAt        int64
	UpdatedAt        int64
	WorkingDir       string
}

type Service interface {
	pubsub.Subscriber[Session]
	Create(ctx context.Context, title string, mode Mode) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	GetLast(ctx context.Context) (Session, error)
	List(ctx context.Context) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error
	Rename(ctx context.Context, id string, title string) error
	Delete(ctx context.Context, id string) error

	// Agent tool session management
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	IsAgentToolSession(sessionID string) bool

	PrepareDerivedSession(ctx context.Context, sessionID, derivedID string) error
}

type service struct {
	*pubsub.Broker[Session]
	db         *sql.DB
	q          *db.Queries
	workingDir string
}

func (s *service) Create(ctx context.Context, title string, mode Mode) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:    uuid.New().String(),
		Title: title,
		Mode:  string(mode),
		WorkingDir: sql.NullString{
			String: s.workingDir,
			Valid:  s.workingDir != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	event.SessionCreated()
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           title,
		Mode:            string(ModeExecute),
		WorkingDir: sql.NullString{
			String: s.workingDir,
			Valid:  s.workingDir != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           "Generate a title",
		Mode:            string(ModeExecute),
		WorkingDir: sql.NullString{
			String: s.workingDir,
			Valid:  s.workingDir != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)

	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session messages: %w", err)
	}
	if err = qtx.DeleteSessionFiles(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session files: %w", err)
	}
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.DeletedEvent, session)
	event.SessionDeleted()
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if s.workingDir != "" && dbSession.WorkingDir.String != s.workingDir {
		return Session{}, sql.ErrNoRows
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) GetLast(ctx context.Context) (Session, error) {
	dbSession, err := s.q.GetLastSession(ctx, sql.NullString{
		String: s.workingDir,
		Valid:  s.workingDir != "",
	})
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	todosJSON, err := marshalTodos(session.Todos)
	if err != nil {
		return Session{}, err
	}

	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:               session.ID,
		Title:            session.Title,
		Mode:             string(session.Mode),
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
		Todos: sql.NullString{
			String: todosJSON,
			Valid:  todosJSON != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session = s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

// UpdateTitleAndUsage updates only the title and usage fields atomically.
// This is safer than fetching, modifying, and saving the entire session.
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error {
	return s.q.UpdateSessionTitleAndUsage(ctx, db.UpdateSessionTitleAndUsageParams{
		ID:               sessionID,
		Title:            title,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Cost:             cost,
	})
}

// Rename updates only the title of a session without touching updated_at or
// usage fields.
func (s *service) Rename(ctx context.Context, id string, title string) error {
	return s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:    id,
		Title: title,
	})
}

func (s *service) List(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListSessions(ctx, sql.NullString{
		String: s.workingDir,
		Valid:  s.workingDir != "",
	})
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

func (s *service) fromDBItem(item db.Session) Session {
	todos, err := unmarshalTodos(item.Todos.String)
	if err != nil {
		slog.Error("Failed to unmarshal todos", "session_id", item.ID, "error", err)
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = "Untitled Session"
	}
	return Session{
		ID:               item.ID,
		ParentSessionID:  item.ParentSessionID.String,
		Title:            title,
		Mode:             Mode(item.Mode),
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		SummaryMessageID: item.SummaryMessageID.String,
		Cost:             item.Cost,
		Todos:            todos,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
		WorkingDir:       item.WorkingDir.String,
	}
}

func marshalTodos(todos []Todo) (string, error) {
	if len(todos) == 0 {
		return "", nil
	}
	data, err := json.Marshal(todos)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalTodos(data string) ([]Todo, error) {
	if data == "" {
		return []Todo{}, nil
	}
	var todos []Todo
	if err := json.Unmarshal([]byte(data), &todos); err != nil {
		return []Todo{}, err
	}
	return todos, nil
}

func NewService(q *db.Queries, conn *sql.DB, workingDir string) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		Broker:     broker,
		db:         conn,
		q:          q,
		workingDir: workingDir,
	}
}

// CreateAgentToolSessionID creates a session ID for agent tool sessions using the format "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID parses an agent tool session ID into its components
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsAgentToolSession checks if a session ID follows the agent tool session format
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}

// PrepareDerivedSession creates a copy of the parent session and its messages
// for isolated background work.
func (s *service) PrepareDerivedSession(ctx context.Context, sessionID, derivedID string) error {
	// Delete derived session if it already exists.
	_ = s.q.DeleteSessionMessages(ctx, derivedID)
	_ = s.q.DeleteSession(ctx, derivedID)

	// Fetch parent session
	parent, err := s.q.GetSessionByID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get parent session: %w", err)
	}

	// Create derived session row.
	_, err = s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              derivedID,
		ParentSessionID: sql.NullString{String: sessionID, Valid: true},
		Title:           "Derived: " + parent.Title,
		Mode:            parent.Mode,
		WorkingDir: sql.NullString{
			String: parent.WorkingDir.String,
			Valid:  parent.WorkingDir.String != "",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create derived session: %w", err)
	}

	// Copy messages to the derived session.
	msgs, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to list messages for copy: %w", err)
	}

	for _, msg := range msgs {
		_, err = s.q.CreateMessage(ctx, db.CreateMessageParams{
			ID:               msg.ID + "-derived",
			SessionID:        derivedID,
			Role:             msg.Role,
			Parts:            msg.Parts,
			Model:            msg.Model,
			Provider:         msg.Provider,
			IsSummaryMessage: msg.IsSummaryMessage,
		})
		if err != nil {
			return fmt.Errorf("failed to copy message %s: %w", msg.ID, err)
		}
	}

	return nil
}
