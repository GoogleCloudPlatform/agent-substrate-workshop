// Package agent is the workshop actor: a small HTTP server that Substrate runs inside a
// gVisor sandbox. It keeps a counter in memory and, when DataDir is set, a counter and the
// chat history on the actor's durableDir, so attendees can see what survives suspend/resume.
package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
)

const (
	countFile   = "count"
	historyFile = "history.json"
	maxContext  = 20 // most recent messages sent to the model per turn
)

// Config is everything main.go decides. Zero values are valid: no DataDir keeps state in
// memory only, no ModelURL answers in echo mode.
type Config struct {
	Greeting string
	DataDir  string
	ModelURL string
	Client   *http.Client
	Now      func() time.Time
}

// State is what GET /state reports. DiskCount is -1 when DataDir is unset.
type State struct {
	Greeting  string    `json:"greeting"`
	BootID    string    `json:"bootID"`
	StartedAt time.Time `json:"startedAt"`
	MemCount  int       `json:"memCount"`
	DiskCount int       `json:"diskCount"`
	Turns     int       `json:"turns"`
	Hostname  string    `json:"hostname"`
}

// Server holds the actor's state. One mutex serializes requests: an actor serves one
// conversation, and holding the lock across the model call keeps history ordered.
type Server struct {
	cfg      Config
	mu       sync.Mutex
	bootID   string
	started  time.Time
	memCount int
	history  []chatapi.Message
}

// New builds a Server. BootID is random per process, so it changes on a cold boot but is
// restored unchanged from a FULL memory snapshot.
func New(cfg Config) (*Server, error) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, bootID: newBootID(), started: cfg.Now().UTC()}
	if cfg.DataDir == "" {
		return s, nil
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s.history = s.loadHistory()
	return s, nil
}

func newBootID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Handler returns the actor's HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("POST /chat", s.handleChat)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memCount++
	disk, err := s.bumpDiskCount()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s (mem=%d disk=%d boot=%s)\n", s.cfg.Greeting, s.memCount, disk, s.bootID)
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, State{
		Greeting:  s.cfg.Greeting,
		BootID:    s.bootID,
		StartedAt: s.started,
		MemCount:  s.memCount,
		DiskCount: s.readDiskCount(),
		Turns:     countTurns(s.history),
		Hostname:  host,
	})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatapi.ChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, chatapi.Error{Error: `body must be {"message": "<non-empty text>"}`})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := append(append([]chatapi.Message(nil), s.history...), chatapi.Message{Role: "user", Content: req.Message})
	reply, backend, err := s.generate(r.Context(), msgs)
	if err != nil {
		log.Printf("model call failed: %v", err)
		writeJSON(w, http.StatusBadGateway, chatapi.Error{Error: "model call failed: " + err.Error()})
		return
	}
	s.history = append(msgs, chatapi.Message{Role: "assistant", Content: reply})
	if err := s.saveHistory(); err != nil {
		log.Printf("persist history: %v", err)
	}
	writeJSON(w, http.StatusOK, chatapi.ChatResponse{Reply: reply, Turn: countTurns(s.history), Backend: backend})
}

// generate asks the model for a reply. Without a ModelURL it echoes, so Modules 2-3 work
// before the gateway exists.
func (s *Server) generate(ctx context.Context, msgs []chatapi.Message) (reply, backend string, err error) {
	if s.cfg.ModelURL == "" {
		return "echo: " + msgs[len(msgs)-1].Content, "echo", nil
	}
	if len(msgs) > maxContext {
		msgs = msgs[len(msgs)-maxContext:]
	}
	body, err := json.Marshal(chatapi.ModelRequest{Messages: msgs})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.ModelURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var mr chatapi.ModelResponse
	if err := json.Unmarshal(b, &mr); err != nil {
		return "", "", fmt.Errorf("decode model response: %w", err)
	}
	return mr.Reply, mr.Backend, nil
}

// bumpDiskCount increments the durable counter. Caller holds s.mu.
func (s *Server) bumpDiskCount() (int, error) {
	if s.cfg.DataDir == "" {
		return -1, nil
	}
	n := s.readDiskCount() + 1
	if err := writeFileAtomic(filepath.Join(s.cfg.DataDir, countFile), []byte(strconv.Itoa(n))); err != nil {
		return 0, fmt.Errorf("persist count: %w", err)
	}
	return n, nil
}

func (s *Server) readDiskCount() int {
	if s.cfg.DataDir == "" {
		return -1
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, countFile))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

// loadHistory reads history from the durableDir. A file it cannot parse (for example one
// cut short by a crash) is moved aside so the actor still boots.
func (s *Server) loadHistory() []chatapi.Message {
	p := filepath.Join(s.cfg.DataDir, historyFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var h []chatapi.Message
	if err == nil {
		err = json.Unmarshal(b, &h)
	}
	if err != nil {
		log.Printf("history unreadable (%v); moving it to %s.corrupt and starting fresh", err, p)
		_ = os.Rename(p, p+".corrupt")
		return nil
	}
	return h
}

func (s *Server) saveHistory() error {
	if s.cfg.DataDir == "" {
		return nil
	}
	b, err := json.Marshal(s.history)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.cfg.DataDir, historyFile), b)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func countTurns(h []chatapi.Message) int {
	n := 0
	for _, m := range h {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
