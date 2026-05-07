// Package api wires HTTP and WebSocket routes for the Frequency Bridge backend.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/Sadonnodas/frequency-bridge/internal/web"
)

type Config struct {
	Bind      string
	WSHandler http.Handler
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
	if s.cfg.WSHandler != nil {
		s.mux.Handle("GET /ws", s.cfg.WSHandler)
	} else {
		s.mux.HandleFunc("GET /ws", s.wsNotConfigured)
	}
	s.mux.Handle("/", web.Handler())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) wsNotConfigured(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "websocket endpoint not configured", http.StatusNotImplemented)
}
