package sessionruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	DefaultSocketPath = "/tmp/kelos-session/runtime.sock"
	DefaultStateDir   = "/workspace/.kelos/session"
	DefaultWorkingDir = "/workspace/repo"
)

// Config configures the resident Session runtime.
type Config struct {
	SocketPath  string
	StateDir    string
	WorkingDir  string
	AgentType   string
	Model       string
	Effort      string
	PluginDir   string
	Environment []string
}

type turnRequest struct {
	id   string
	text string
}

type pendingInput struct {
	questions map[string]struct{}
	answers   map[string][]string
	result    chan pendingInputResult
	resolved  bool
}

type pendingInputResult struct {
	answers   map[string][]string
	cancelled bool
}

// Server owns one provider process, event stream, and local client socket.
type Server struct {
	config   Config
	journal  *Journal
	provider Provider

	turns      chan turnRequest
	nextTurnID atomic.Int64
	activeMu   sync.Mutex
	activeTurn string

	inputMu       sync.Mutex
	pendingInputs map[string]*pendingInput
	nextInputID   atomic.Int64
}

// NewServer constructs a Session server around injected provider and journal implementations.
func NewServer(config Config, journal *Journal, provider Provider) *Server {
	return &Server{
		config:        config,
		journal:       journal,
		provider:      provider,
		turns:         make(chan turnRequest, 32),
		pendingInputs: map[string]*pendingInput{},
	}
}

// Run prepares the agent image and serves the Session until ctx is cancelled.
func Run(ctx context.Context, config Config) error {
	if err := os.MkdirAll(config.WorkingDir, 0750); err != nil {
		return fmt.Errorf("creating Session working directory: %w", err)
	}
	if err := os.MkdirAll(config.StateDir, 0700); err != nil {
		return fmt.Errorf("creating Session state directory: %w", err)
	}
	if err := runAgentSetup(ctx, config.WorkingDir); err != nil {
		return err
	}

	journal := NewJournal()
	provider, err := NewProvider(ctx, ProviderConfig{
		AgentType:   config.AgentType,
		WorkingDir:  config.WorkingDir,
		StateDir:    config.StateDir,
		Model:       config.Model,
		Effort:      config.Effort,
		PluginDir:   config.PluginDir,
		Environment: config.Environment,
	})
	if err != nil {
		journal.Close()
		return err
	}

	return NewServer(config, journal, provider).Serve(ctx)
}

func runAgentSetup(ctx context.Context, workingDir string) error {
	command := exec.CommandContext(ctx, "/kelos_entrypoint.sh")
	command.Dir = workingDir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = replaceProcessEnv(os.Environ(), "KELOS_SESSION_SETUP_ONLY", "1")
	if err := command.Run(); err != nil {
		return fmt.Errorf("preparing Session agent environment: %w", err)
	}
	return nil
}

func replaceProcessEnv(current []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(current)+1)
	for _, entry := range current {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

// Serve listens for local clients and keeps provider turns alive independently of them.
func (s *Server) Serve(ctx context.Context) error {
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	if err := os.MkdirAll(filepath.Dir(s.config.SocketPath), 0700); err != nil {
		return fmt.Errorf("creating Session socket directory: %w", err)
	}
	if err := os.Remove(s.config.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale Session socket: %w", err)
	}
	listener, err := net.Listen("unix", s.config.SocketPath)
	if err != nil {
		return fmt.Errorf("listening on Session socket: %w", err)
	}
	if err := os.Chmod(s.config.SocketPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("securing Session socket: %w", err)
	}

	providerDone := s.provider.Done()
	go s.runTurns(serveCtx)
	go func() {
		select {
		case <-serveCtx.Done():
		case <-providerDone:
		}
		_ = listener.Close()
	}()

	log.Printf("Session runtime ready socket=%s provider=%s", s.config.SocketPath, s.config.AgentType)
	defer func() {
		_ = s.provider.Close()
		s.journal.Close()
		_ = os.Remove(s.config.SocketPath)
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return nil
			}
			select {
			case <-providerDone:
				return errors.New("Session provider stopped")
			default:
			}
			return fmt.Errorf("accepting Session client: %w", err)
		}
		go s.handleConnection(serveCtx, connection)
	}
}

func (s *Server) runTurns(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case turn := <-s.turns:
			s.runTurn(ctx, turn)
		}
	}
}

func (s *Server) runTurn(ctx context.Context, turn turnRequest) {
	s.activeMu.Lock()
	s.activeTurn = turn.id
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		if s.activeTurn == turn.id {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
	}()

	s.journal.Append(Event{Type: EventUserMessage, TurnID: turn.id, Text: turn.text})
	s.journal.Append(Event{Type: EventTurnStarted, TurnID: turn.id, Status: "running"})
	sink := &turnSink{server: s, turnID: turn.id}
	err := s.provider.RunTurn(ctx, turn.text, sink)
	if s.config.AgentType == "claude-code" || s.config.AgentType == "opencode" {
		if diff := workspaceDiff(ctx, s.config.WorkingDir); diff != "" {
			sink.Emit(Event{Type: EventFileDiff, Diff: diff})
		}
	}
	if errors.Is(err, ErrTurnInterrupted) {
		sink.Emit(Event{Type: EventTurnCompleted, Status: "interrupted"})
		return
	}
	if err != nil {
		sink.Emit(Event{Type: EventError, Text: err.Error(), Status: "failed"})
		sink.Emit(Event{Type: EventTurnCompleted, Status: "failed"})
		return
	}
	sink.Emit(Event{Type: EventTurnCompleted, Status: "completed"})
}

func workspaceDiff(ctx context.Context, workingDir string) string {
	command := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--no-color")
	command.Dir = workingDir
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		return ""
	}
	const maxDiffBytes = 1024 * 1024
	if len(output) > maxDiffBytes {
		return string(output[:maxDiffBytes]) + "\n\n[diff truncated]"
	}
	return string(output)
}

func (s *Server) submitMessage(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("message must not be empty")
	}
	turn := turnRequest{
		id:   fmt.Sprintf("turn-%d", s.nextTurnID.Add(1)),
		text: text,
	}
	select {
	case s.turns <- turn:
		return nil
	default:
		return errors.New("Session message queue is full")
	}
}

func (s *Server) interruptTurn(ctx context.Context) error {
	s.activeMu.Lock()
	turnID := s.activeTurn
	s.activeMu.Unlock()
	if turnID == "" {
		return ErrNoActiveTurn
	}
	s.journal.Append(Event{Type: EventTurnInterrupting, TurnID: turnID, Status: "interrupting"})
	if err := s.provider.Interrupt(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan Event, 512)
	writerDone := make(chan error, 1)
	go func() {
		encoder := json.NewEncoder(connection)
		for {
			select {
			case <-connectionCtx.Done():
				writerDone <- nil
				return
			case event := <-out:
				if err := encoder.Encode(event); err != nil {
					writerDone <- err
					cancel()
					return
				}
			}
		}
	}()

	var subscriptionCancel func()
	defer func() {
		if subscriptionCancel != nil {
			subscriptionCancel()
		}
	}()
	subscribe := func(since int64) {
		if subscriptionCancel != nil {
			return
		}
		retained, stream, overflow, stop := s.journal.Subscribe(since)
		subscriptionCancel = stop
		disconnect := func() {
			cancel()
			_ = connection.Close()
		}
		enqueue := func(event Event) bool {
			select {
			case out <- event:
				return true
			case <-overflow:
				disconnect()
				return false
			case <-connectionCtx.Done():
				return false
			}
		}
		for _, event := range retained {
			if !enqueue(event) {
				return
			}
		}
		if !enqueue(Event{Type: EventHistoryEnd}) {
			return
		}
		go func() {
			for {
				select {
				case <-connectionCtx.Done():
					return
				case <-overflow:
					disconnect()
					return
				case event, ok := <-stream:
					if !ok {
						disconnect()
						return
					}
					select {
					case out <- event:
					case <-overflow:
						disconnect()
						return
					case <-connectionCtx.Done():
						return
					}
				}
			}
		}()
	}

	decoder := json.NewDecoder(bufio.NewReader(connection))
	for {
		var request ClientRequest
		if err := decoder.Decode(&request); err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("Session client read failed error=%v", err)
			}
			cancel()
			<-writerDone
			return
		}
		switch request.Type {
		case "subscribe":
			subscribe(request.Since)
		case "message":
			subscribe(0)
			if err := s.submitMessage(request.Text); err != nil {
				out <- Event{Type: EventError, Text: err.Error(), Status: "rejected"}
			}
		case "input":
			subscribe(0)
			if err := s.resolveInput(request.InputID, request.Answers, request.Cancel); err != nil {
				out <- Event{Type: EventError, Text: err.Error(), Status: "rejected"}
			}
		case "interrupt":
			subscribe(0)
			if err := s.interruptTurn(connectionCtx); err != nil {
				out <- Event{Type: EventError, Text: err.Error(), Status: "rejected"}
			}
		default:
			out <- Event{Type: EventError, Text: fmt.Sprintf("unsupported client request type %q", request.Type), Status: "rejected"}
		}
	}
}

type turnSink struct {
	server *Server
	turnID string
}

func (s *turnSink) Emit(event Event) {
	if event.TurnID == "" {
		event.TurnID = s.turnID
	}
	s.server.journal.Append(event)
}

func (s *turnSink) RequestInput(ctx context.Context, request InputRequest) (map[string][]string, error) {
	if request.ID == "" {
		request.ID = fmt.Sprintf("input-%d", s.server.nextInputID.Add(1))
	}
	if len(request.Questions) == 0 {
		return nil, errors.New("input request must contain at least one question")
	}
	questions := make(map[string]struct{}, len(request.Questions))
	for _, question := range request.Questions {
		if strings.TrimSpace(question.ID) == "" {
			return nil, errors.New("input question ID must not be empty")
		}
		if _, exists := questions[question.ID]; exists {
			return nil, fmt.Errorf("input question ID %q is duplicated", question.ID)
		}
		questions[question.ID] = struct{}{}
	}
	pending := &pendingInput{
		questions: questions,
		answers:   map[string][]string{},
		result:    make(chan pendingInputResult, 1),
	}
	s.server.inputMu.Lock()
	if _, exists := s.server.pendingInputs[request.ID]; exists {
		s.server.inputMu.Unlock()
		return nil, fmt.Errorf("input request %q is already pending", request.ID)
	}
	s.server.pendingInputs[request.ID] = pending
	s.server.inputMu.Unlock()
	defer func() {
		s.server.inputMu.Lock()
		delete(s.server.pendingInputs, request.ID)
		s.server.inputMu.Unlock()
	}()

	s.Emit(Event{
		Type:      EventInputRequested,
		InputID:   request.ID,
		Questions: request.Questions,
		Status:    "pending",
	})
	select {
	case result := <-pending.result:
		if result.cancelled {
			return nil, ErrInputCancelled
		}
		return result.answers, nil
	case <-ctx.Done():
		s.server.inputMu.Lock()
		cancelled := !pending.resolved
		pending.resolved = true
		s.server.inputMu.Unlock()
		if cancelled {
			s.server.journal.Append(Event{Type: EventInputResolved, InputID: request.ID, Status: "cancelled"})
		}
		return nil, ctx.Err()
	}
}

func (s *Server) resolveInput(id string, answers map[string][]string, cancel bool) error {
	s.inputMu.Lock()
	pending := s.pendingInputs[id]
	if pending == nil {
		s.inputMu.Unlock()
		return fmt.Errorf("input request %q is not pending", id)
	}
	if pending.resolved {
		s.inputMu.Unlock()
		return fmt.Errorf("input request %q was already resolved", id)
	}
	if cancel {
		pending.resolved = true
		s.inputMu.Unlock()
		s.journal.Append(Event{Type: EventInputResolved, InputID: id, Status: "cancelled"})
		pending.result <- pendingInputResult{cancelled: true}
		return nil
	}
	if len(answers) == 0 {
		s.inputMu.Unlock()
		return errors.New("input response must contain at least one answer")
	}
	for questionID, values := range answers {
		if _, exists := pending.questions[questionID]; !exists {
			s.inputMu.Unlock()
			return fmt.Errorf("input question %q is not pending", questionID)
		}
		if len(values) == 0 {
			s.inputMu.Unlock()
			return fmt.Errorf("input question %q must contain an answer", questionID)
		}
		copied := append([]string(nil), values...)
		pending.answers[questionID] = copied
	}
	if len(pending.answers) < len(pending.questions) {
		s.inputMu.Unlock()
		return nil
	}
	pending.resolved = true
	resolved := make(map[string][]string, len(pending.answers))
	for questionID, values := range pending.answers {
		resolved[questionID] = append([]string(nil), values...)
	}
	s.inputMu.Unlock()
	s.journal.Append(Event{Type: EventInputResolved, InputID: id, Status: "answered"})
	pending.result <- pendingInputResult{answers: resolved}
	return nil
}
