// Package api wires HTTP and WebSocket routes for the Frequency Bridge backend.
//
// Phase 0 only provides the routing skeleton; WebSocket fan-out, RPC handling,
// and the embedded UI come online in later phases.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/Sadonnodas/frequency-bridge/internal/web"
)

type Config struct {
	Bind string
}

type Server struct {
	cfg Config
	mux *http.ServeMux
}

func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /ws", s.handleWebSocket)
	s.mux.Handle("/", web.Handler())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleWebSocket(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "websocket endpoint not yet implemented", http.StatusNotImplemented)
}
