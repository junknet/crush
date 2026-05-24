package proto

// Session represents a session in the proto layer.
type Session struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parent_session_id"`
	Title            string  `json:"title"`
	Mode             string  `json:"mode"`
	MessageCount     int64   `json:"message_count"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	SummaryMessageID string  `json:"summary_message_id"`
	Cost             float64 `json:"cost"`
	Todos            []Todo  `json:"todos,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	// Path is the session's working directory; Alive reports whether a driver
	// (a TUI) is currently attached. These power the session-primary view that
	// replaces the workspace grouping on clients.
	Path  string `json:"path,omitempty"`
	Alive bool   `json:"alive"`
}

// Todo represents a single todo entry on a session in the proto layer.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form"`
}
