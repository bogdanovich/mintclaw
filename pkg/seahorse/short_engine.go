package seahorse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	toolpolicy "github.com/bogdanovich/mintclaw/pkg/tools/policy"
)

// Config holds engine configuration.
type Config struct {
	DBPath                   string                           `json:"dbPath"`
	IgnoreSessionPatterns    []string                         `json:"ignoreSessionPatterns,omitempty"`
	StatelessSessionPatterns []string                         `json:"statelessSessionPatterns,omitempty"`
	FreshTailMaxTokens       int                              `json:"freshTailMaxTokens,omitempty"`
	HistoryMaxTokens         int                              `json:"historyMaxTokens,omitempty"`
	SummaryMaxTokens         int                              `json:"summaryMaxTokens,omitempty"`
	RecentTailTurns          int                              `json:"recentTailTurns,omitempty"`
	MaxRetrievalScope        string                           `json:"maxRetrievalScope,omitempty"`
	ResultRetentionPolicy    toolpolicy.ResultRetentionPolicy `json:"-"`
	SummaryPolicy            SummaryPolicy                    `json:"-"`
}

// CompleteFn is the LLM completion function type.
type CompleteFn func(ctx context.Context, prompt string, opts CompleteOptions) (string, error)

// CompleteOptions holds LLM completion parameters.
type CompleteOptions struct {
	Model       string
	MaxTokens   int
	Temperature float64
}

// IngestResult is the result of message ingestion.
type IngestResult struct {
	MessageCount int `json:"messageCount"`
	TokenCount   int `json:"tokenCount"`
}

// AssembleInput controls context assembly.
type AssembleInput struct {
	Budget int    `json:"budget"`
	Query  string `json:"query,omitempty"`
}

// AssembleResult contains assembled context.
type AssembleResult struct {
	Messages []Message             `json:"messages"`
	Summary  string                `json:"summary"` // formatted XML summaries + system prompt addition
	Budget   *AssembleBudgetReport `json:"budget,omitempty"`
}

// AssembleBudgetReport describes bounded context selection and pressure.
type AssembleBudgetReport struct {
	TotalBudget              int      `json:"totalBudget"`
	HistoryBudget            int      `json:"historyBudget"`
	SummaryBudget            int      `json:"summaryBudget"`
	SourceHistoryTokens      int      `json:"sourceHistoryTokens"`
	SourceSummaryTokens      int      `json:"sourceSummaryTokens"`
	SelectedHistoryTokens    int      `json:"selectedHistoryTokens"`
	SelectedSummaryTokens    int      `json:"selectedSummaryTokens"`
	RequestedRecentTailTurns int      `json:"requestedRecentTailTurns"`
	RecentTailTurns          int      `json:"recentTailTurns"`
	RecentTailTokens         int      `json:"recentTailTokens"`
	RecentTailOverflowTokens int      `json:"recentTailOverflowTokens"`
	RecentTailDegraded       bool     `json:"recentTailDegraded"`
	Truncated                bool     `json:"truncated"`
	NeedsCompaction          bool     `json:"needsCompaction"`
	PressureReasons          []string `json:"pressureReasons,omitempty"`
}

const numSessionShards = 256

// Engine is the main short-term memory engine.
type Engine struct {
	store             *Store
	compaction        *CompactionEngine
	compactionMu      sync.Mutex
	assembler         *Assembler
	assemblerMu       sync.Mutex
	retrieval         *RetrievalEngine
	config            Config
	complete          CompleteFn
	ignorePatterns    []*regexp.Regexp
	statelessPatterns []*regexp.Regexp
	sessionShards     [numSessionShards]struct {
		mu sync.Mutex
	}
}

// CompactionEngine handles LLM-based summarization (defined in short_compaction.go).
type CompactionEngine struct {
	store      *Store
	config     Config
	complete   CompleteFn
	condensing sync.Map // map[int64]*condensedRun — dedup and join condensed work
}

// Assembler handles budget-aware context assembly (defined in short_assembler.go).
type Assembler struct {
	store  *Store
	config Config
}

// RetrievalEngine handles search and expansion (defined in short_retrieval.go).
type RetrievalEngine struct {
	store  *Store
	config Config
}

// EngineFactory opens one Engine using the caller's admitted storage boundary.
type EngineFactory func(context.Context, Config, CompleteFn) (*Engine, error)

// IsCorruptDatabaseError reports only typed SQLite corruption/not-a-database results.
func IsCorruptDatabaseError(err error) bool {
	var sqliteErr *modernsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	primaryCode := sqliteErr.Code() & 0xff
	return primaryCode == sqlite3.SQLITE_CORRUPT || primaryCode == sqlite3.SQLITE_NOTADB
}

// ResetCorruptDatabase removes only the admitted SQLite database and its
// sidecars, and only when the caller supplies a typed corruption diagnostic.
func ResetCorruptDatabase(dbPath string, cause error) error {
	if !IsCorruptDatabaseError(cause) {
		return fmt.Errorf("reset corrupt database: typed SQLite corruption is required: %w", cause)
	}
	if strings.TrimSpace(dbPath) == "" {
		return fmt.Errorf("reset corrupt database: database path is required")
	}
	return removeDatabaseFiles(dbPath)
}

// RebuildCorruptDatabaseContext replaces a corrupt disposable database while
// preserving this Engine and its retrieval identity. The caller must hold
// exclusive lifecycle ownership; recovery is intended for startup before any
// turn or retrieval tool can run. The replacement factory is consumed through
// the same admitted storage boundary that constructed the original engine.
func (e *Engine) RebuildCorruptDatabaseContext(
	ctx context.Context,
	cause error,
	factory EngineFactory,
) error {
	if e == nil {
		return fmt.Errorf("rebuild corrupt database: engine is required")
	}
	if !IsCorruptDatabaseError(cause) {
		return fmt.Errorf("rebuild corrupt database: typed SQLite corruption is required: %w", cause)
	}
	if ctx == nil {
		return fmt.Errorf("rebuild corrupt database: context is required")
	}
	if factory == nil {
		return fmt.Errorf("rebuild corrupt database: engine factory is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := e.Close(); err != nil {
		return fmt.Errorf("close corrupt database: %w", err)
	}
	if err := ResetCorruptDatabase(e.config.DBPath, cause); err != nil {
		return err
	}
	replacement, err := factory(ctx, e.config, e.complete)
	if err != nil {
		return fmt.Errorf("reopen rebuilt database: %w", err)
	}
	if replacement == nil {
		return fmt.Errorf("reopen rebuilt database: factory returned a nil engine")
	}
	if replacement == e {
		return fmt.Errorf("reopen rebuilt database: factory returned the closed engine")
	}
	if replacement.retrieval == nil {
		_ = replacement.Close()
		return fmt.Errorf("reopen rebuilt database: factory returned an engine without retrieval")
	}

	retrieval := e.retrieval
	if retrieval == nil {
		retrieval = replacement.retrieval
	} else {
		retrieval.store = replacement.retrieval.store
		retrieval.config = replacement.retrieval.config
	}
	e.store = replacement.store
	e.compaction = replacement.compaction
	e.assembler = replacement.assembler
	e.retrieval = retrieval
	e.config = replacement.config
	e.complete = replacement.complete
	e.ignorePatterns = replacement.ignorePatterns
	e.statelessPatterns = replacement.statelessPatterns
	return nil
}

func removeDatabaseFiles(dbPath string) error {
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove corrupt database file %q: %w", path, err)
		}
	}
	return nil
}

// AbsoluteBudgetsEnabled reports whether separate context budgets are configured.
func (e *Engine) AbsoluteBudgetsEnabled() bool {
	return e != nil && e.config.absoluteBudgetsEnabled()
}

// Store returns the underlying store for direct access.
func (r *RetrievalEngine) Store() *Store {
	return r.store
}

// NewEngine creates a new short-term memory engine.
func NewEngine(config Config, completeFn CompleteFn) (*Engine, error) {
	return NewEngineContext(context.Background(), config, completeFn)
}

// NewEngineContext creates an engine while bounding SQLite setup and schema work.
func NewEngineContext(ctx context.Context, config Config, completeFn CompleteFn) (*Engine, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create engine: context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := config.validateBudgets(); err != nil {
		return nil, fmt.Errorf("invalid context budget config: %w", err)
	}
	if _, err := config.effectiveMaxRetrievalScope(); err != nil {
		return nil, fmt.Errorf("invalid retrieval policy config: %w", err)
	}
	if err := config.ResultRetentionPolicy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid tool result retention config: %w", err)
	}
	if err := config.SummaryPolicy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid summary policy config: %w", err)
	}
	dir := filepath.Dir(config.DBPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", config.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Configure SQLite for concurrent access
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous = NORMAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set synchronous: %w", err)
	}

	if err := runSchemaContext(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}

	store := &Store{db: db}

	// Prepend hardcoded ignore patterns (spec lines 1326-1328)
	ignorePatterns := make([]string, 0, 1+len(config.IgnoreSessionPatterns))
	ignorePatterns = append(ignorePatterns, "heartbeat")
	ignorePatterns = append(ignorePatterns, config.IgnoreSessionPatterns...)

	retrieval := &RetrievalEngine{store: store, config: config}

	return &Engine{
		store:             store,
		compaction:        nil,
		assembler:         nil,
		retrieval:         retrieval,
		config:            config,
		complete:          completeFn,
		ignorePatterns:    compileSessionPatterns(ignorePatterns),
		statelessPatterns: compileSessionPatterns(config.StatelessSessionPatterns),
	}, nil
}

// compileSessionPattern converts a glob pattern to a compiled regex.
// Pattern rules:
//   - *  matches any sequence of non-colon characters ([^:]*)
//   - ** matches any sequence of characters including colons (.*)
//   - All other characters are treated literally
//   - Pattern is anchored (^...$)
func compileSessionPattern(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteByte('^')

	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			b.WriteString(".*")
			i += 2
			continue
		}
		if pattern[i] == '*' {
			b.WriteString("[^:]*")
			i++
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}

	b.WriteByte('$')
	return regexp.MustCompile(b.String())
}

// compileSessionPatterns compiles multiple glob patterns into regex patterns.
func compileSessionPatterns(patterns []string) []*regexp.Regexp {
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if p == "" {
			continue
		}
		result = append(result, compileSessionPattern(p))
	}
	return result
}

// shouldIgnoreSession returns true if the session key matches any ignore pattern.
func (e *Engine) shouldIgnoreSession(sessionKey string) bool {
	for _, p := range e.ignorePatterns {
		if p.MatchString(sessionKey) {
			return true
		}
	}
	return false
}

// isStatelessSession returns true if the session key matches any stateless pattern.
func (e *Engine) isStatelessSession(sessionKey string) bool {
	for _, p := range e.statelessPatterns {
		if p.MatchString(sessionKey) {
			return true
		}
	}
	return false
}

// fnv32 computes FNV-1a 32-bit hash for session key sharding.
func fnv32(key string) uint32 {
	h := uint32(2166136261)
	for _, c := range key {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// getSessionMutex returns the sharded mutex for a session key.
func (e *Engine) getSessionMutex(sessionKey string) *sync.Mutex {
	h := fnv32(sessionKey)
	shard := h % numSessionShards
	return &e.sessionShards[shard].mu
}

// Ingest adds messages to a conversation identified by sessionKey.
func (e *Engine) Ingest(ctx context.Context, sessionKey string, messages []Message) (*IngestResult, error) {
	if e.shouldIgnoreSession(sessionKey) {
		return nil, nil
	}
	if e.isStatelessSession(sessionKey) {
		return nil, nil
	}

	mu := e.getSessionMutex(sessionKey)
	mu.Lock()
	defer mu.Unlock()

	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	var totalTokens int
	for _, msg := range messages {
		totalTokens += msg.TokenCount
	}
	if err := e.store.appendMessages(ctx, conv.ConversationID, messages); err != nil {
		return nil, fmt.Errorf("append messages: %w", err)
	}

	logger.InfoCF("seahorse", "ingest", map[string]any{
		"conv_id":  conv.ConversationID,
		"messages": len(messages),
		"tokens":   totalTokens,
	})
	return &IngestResult{
		MessageCount: len(messages),
		TokenCount:   totalTokens,
	}, nil
}

// Close releases resources.
func (e *Engine) Close() error {
	if e.store != nil && e.store.db != nil {
		return e.store.db.Close()
	}
	return nil
}

// GetRetrieval returns the retrieval engine for tool implementations.
func (e *Engine) GetRetrieval() *RetrievalEngine {
	return e.retrieval
}

// Assemble builds budget-constrained context for a session.
func (e *Engine) Assemble(ctx context.Context, sessionKey string, input AssembleInput) (*AssembleResult, error) {
	if e.shouldIgnoreSession(sessionKey) {
		return nil, nil
	}

	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initAssemblerOnce()
	return e.assembler.Assemble(ctx, conv.ConversationID, input)
}

// Compact compresses conversation history for a session.
func (e *Engine) Compact(ctx context.Context, sessionKey string, input CompactInput) (*CompactResult, error) {
	if e.shouldIgnoreSession(sessionKey) || e.isStatelessSession(sessionKey) {
		return &CompactResult{}, nil
	}

	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initCompactionOnce()
	return e.compaction.Compact(ctx, conv.ConversationID, input)
}

// CompactUntilUnder aggressively compacts until context is under budget.
// Used for emergency compaction after LLM overflow (retry reason).
func (e *Engine) CompactUntilUnder(ctx context.Context, sessionKey string, budget int) (*CompactResult, error) {
	if e.shouldIgnoreSession(sessionKey) || e.isStatelessSession(sessionKey) {
		return &CompactResult{}, nil
	}

	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initCompactionOnce()
	return e.compaction.CompactUntilUnder(ctx, conv.ConversationID, budget)
}

// initCompactionOnce lazily initializes the compaction engine.
func (e *Engine) initCompactionOnce() {
	if e.compaction == nil {
		e.compactionMu.Lock()
		defer e.compactionMu.Unlock()
		if e.compaction == nil {
			e.compaction = &CompactionEngine{
				store:    e.store,
				config:   e.config,
				complete: e.complete,
			}
		}
	}
}

// initAssemblerOnce lazily initializes the assembler.
func (e *Engine) initAssemblerOnce() {
	if e.assembler == nil {
		e.assemblerMu.Lock()
		defer e.assemblerMu.Unlock()
		if e.assembler == nil {
			e.assembler = &Assembler{store: e.store, config: e.config}
		}
	}
}

// ClearSession removes all stored data for a session (messages, summaries, context).
// If the session has no prior seahorse record, it is a no-op.
func (e *Engine) ClearSession(ctx context.Context, sessionKey string) error {
	conv, err := e.store.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		return err
	}
	if conv == nil {
		return nil // session never ingested, nothing to clear
	}
	return e.store.ClearConversation(ctx, conv.ConversationID)
}

// Bootstrap reconciles a session's canonical JSONL history with the derived database.
// A proven append ingests only its delta; every other difference clears all derived
// messages, summaries, and context before rebuilding.
func (e *Engine) Bootstrap(ctx context.Context, sessionKey string, messages []Message) error {
	if e.shouldIgnoreSession(sessionKey) {
		return nil
	}
	if e.isStatelessSession(sessionKey) {
		return nil
	}
	if len(messages) == 0 {
		conv, err := e.store.GetConversationBySessionKey(ctx, sessionKey)
		if err != nil {
			return fmt.Errorf("bootstrap: get empty conversation: %w", err)
		}
		if conv == nil {
			return nil
		}
		if err := e.store.replaceConversationMessages(ctx, conv.ConversationID, nil); err != nil {
			return fmt.Errorf("bootstrap: clear empty canonical history: %w", err)
		}
		return nil
	}

	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return fmt.Errorf("bootstrap: get conversation: %w", err)
	}

	// Load one extra row so a canonical history shorter than SQLite is detected.
	dbMsgs, err := e.store.GetMessages(ctx, conv.ConversationID, len(messages)+1, 0)
	if err != nil {
		return fmt.Errorf("bootstrap: get messages: %w", err)
	}

	prefixMatches := true
	compareLen := min(len(dbMsgs), len(messages))
	for i := range compareLen {
		if messagesMatch(dbMsgs[i], messages[i]) {
			continue
		}
		prefixMatches = false
		logger.InfoCF("seahorse", "bootstrap: mismatch detected", map[string]any{
			"conv_id":        conv.ConversationID,
			"index":          i,
			"db_role":        dbMsgs[i].Role,
			"db_content":     truncate(dbMsgs[i].Content, 50),
			"db_parts":       len(dbMsgs[i].Parts),
			"db_model_name":  dbMsgs[i].ModelName,
			"msg_role":       messages[i].Role,
			"msg_content":    truncate(messages[i].Content, 50),
			"msg_parts":      len(messages[i].Parts),
			"msg_model_name": messages[i].ModelName,
		})
		break
	}

	if prefixMatches && len(dbMsgs) <= len(messages) {
		delta := messages[len(dbMsgs):]
		if len(delta) == 0 {
			return nil
		}
		if _, err := e.Ingest(ctx, sessionKey, delta); err != nil {
			return fmt.Errorf("bootstrap: ingest delta: %w", err)
		}
		return nil
	}

	logger.InfoCF("seahorse", "bootstrap: canonical history diverged, rebuilding", map[string]any{
		"conv_id":   conv.ConversationID,
		"db_count":  len(dbMsgs),
		"msg_count": len(messages),
	})
	if err := e.store.replaceConversationMessages(ctx, conv.ConversationID, messages); err != nil {
		return fmt.Errorf("bootstrap: rebuild canonical history: %w", err)
	}
	totalTokens := 0
	for _, message := range messages {
		totalTokens += message.TokenCount
	}
	logger.InfoCF("seahorse", "ingest", map[string]any{
		"conv_id":  conv.ConversationID,
		"messages": len(messages),
		"tokens":   totalTokens,
	})

	return nil
}

// truncate shortens a string for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// messagesMatch compares two messages by role, payload, and canonical metadata.
// TokenCount is intentionally ignored because bootstrap may re-estimate it differently.
func messagesMatch(a, b Message) bool {
	if a.Role != b.Role {
		return false
	}
	if a.ReasoningContent != b.ReasoningContent {
		return false
	}
	if a.ModelName != b.ModelName {
		return false
	}
	if !messageCreatedAtMatches(a.CreatedAt, b.CreatedAt) {
		return false
	}
	// If either message has Parts, compare Parts
	if len(a.Parts) > 0 || len(b.Parts) > 0 {
		return partsMatch(a.Parts, b.Parts)
	}
	// Simple text messages: compare Content
	return a.Content == b.Content
}

// messageCreatedAtMatches compares source timestamps exactly after normalization.
// Internal ordering uses message IDs and context ordinals, so it must not turn
// an unknown source time into an implicit match for a synthetic timestamp.
func messageCreatedAtMatches(a, b time.Time) bool {
	na := normalizeMessageCreatedAt(a)
	nb := normalizeMessageCreatedAt(b)
	return na.Equal(nb)
}

// partsMatch compares two slices of MessagePart for equality.
func partsMatch(a, b []MessagePart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type {
			return false
		}
		switch a[i].Type {
		case "text":
			if a[i].Text != b[i].Text {
				return false
			}
		case "tool_use":
			if a[i].Name != b[i].Name || a[i].Arguments != b[i].Arguments || a[i].ToolCallID != b[i].ToolCallID {
				return false
			}
		case "tool_result":
			if a[i].ToolCallID != b[i].ToolCallID ||
				a[i].Text != b[i].Text ||
				a[i].ToolResultStatus != b[i].ToolResultStatus {
				return false
			}
		case "media":
			if a[i].MediaURI != b[i].MediaURI || a[i].MimeType != b[i].MimeType {
				return false
			}
		}
	}
	return true
}
