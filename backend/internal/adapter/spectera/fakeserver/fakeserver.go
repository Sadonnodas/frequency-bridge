// Package fakeserver implements a minimal SSCv2 server for development and
// testing of the spectera adapter without real hardware.
//
// Conforms to the wire shapes documented in Sennheiser's "3rd Party API"
// guide v6.0 (March 2026), specifically:
//
//   - Subscriptions start EMPTY. The client GETs /api/ssc/state/subscriptions
//     to receive an open event with the sessionUUID, then PUTs an array of
//     resource paths to /api/ssc/state/subscriptions/{sessionUUID} (full set)
//     or .../add (incremental) to register interest.
//   - SSE messages are resource-keyed JSON objects:
//
//	    { "/api/audio/inputs/1": { "id": 1, "name": "...", ... } }
//
//     Each notification is equivalent to a GET of the keyed resource.
//   - Event types used: open, message, close. Event prefixes follow the
//     EventSource spec: `event: <type>\ndata: <json>\n\n`.
//
// What this fake DOES NOT yet match (documented for slice 2 follow-up):
//
//   - Real Spectera /api/audio/inputs field shape is unknown from the public
//     guide; we use a simplified (id, name, frequency, mute, gain_db,
//     encrypted) shape. Slice 2 aligns with the OpenAPI YAML on first device
//     contact.
//   - Real Spectera channel model is rf channels + audio links + mts paired
//     mobile devices. We model audio inputs only.
//   - Subscription set-via-PUT-on-the-collection-path semantics aren't
//     simulated; we only support exact-path subscriptions.
package fakeserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// Input is the subset of /api/audio/inputs/{id} we model. Frequency is in kHz
// (e.g. 562275 == 562.275 MHz) — matches GET /api/rf/channels in the spec.
type Input struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Frequency int    `json:"frequency"`
	Mute      bool   `json:"mute"`
	GainDB    int    `json:"gain_db"`
	Encrypted bool   `json:"encrypted"`
}

type Server struct {
	username, password string

	mu     sync.RWMutex
	inputs map[int]*Input

	subsMu sync.Mutex
	subs   map[string]*subscription

	mux *http.ServeMux
}

type subscription struct {
	sessionUUID string
	events      chan map[string]any

	mu    sync.Mutex
	paths map[string]struct{}
}

// New constructs a fake Spectera server with the given basic-auth credentials.
func New(username, password string) *Server {
	s := &Server{
		username: username,
		password: password,
		inputs:   make(map[int]*Input),
		subs:     make(map[string]*subscription),
		mux:      http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/audio/inputs", s.requireAuth(s.handleListInputs))
	s.mux.HandleFunc("GET /api/audio/inputs/{id}", s.requireAuth(s.handleGetInput))
	s.mux.HandleFunc("GET /api/ssc/state/subscriptions", s.requireAuth(s.handleSubscribe))
	s.mux.HandleFunc("PUT /api/ssc/state/subscriptions/{id}", s.requireAuth(s.handleSetSubscription))
	s.mux.HandleFunc("PUT /api/ssc/state/subscriptions/{id}/add", s.requireAuth(s.handleAddSubscription))
	s.mux.HandleFunc("PUT /api/ssc/state/subscriptions/{id}/remove", s.requireAuth(s.handleRemoveSubscription))
	s.mux.HandleFunc("DELETE /api/ssc/state/subscriptions/{id}", s.requireAuth(s.handleUnsubscribe))
}

// Test helpers -------------------------------------------------------------

// AddInput inserts (or overwrites) an input. Pushes the input as a resource
// update to every subscription that includes its path.
func (s *Server) AddInput(id int, name string, frequencyKHz int) *Input {
	in := &Input{ID: id, Name: name, Frequency: frequencyKHz}
	s.mu.Lock()
	s.inputs[id] = in
	s.mu.Unlock()
	s.notifyResource(inputPath(id))
	return in
}

// UpdateInput mutates an input under lock and pushes one resource event for
// the (now-current) input. Per SSCv2 spec the notification carries the full
// resource state, so we don't need per-field events.
func (s *Server) UpdateInput(id int, fn func(*Input)) {
	s.mu.Lock()
	in, ok := s.inputs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	fn(in)
	in.ID = id
	s.mu.Unlock()
	if !ok {
		return
	}
	s.notifyResource(inputPath(id))
}

// SubscriberCount returns the number of currently-attached SSE subscribers.
func (s *Server) SubscriberCount() int {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	return len(s.subs)
}

// SubscriptionPaths returns a snapshot of paths a particular session is
// subscribed to. Returns nil if the session is gone.
func (s *Server) SubscriptionPaths(sessionUUID string) []string {
	s.subsMu.Lock()
	sub, ok := s.subs[sessionUUID]
	s.subsMu.Unlock()
	if !ok {
		return nil
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	out := make([]string, 0, len(sub.paths))
	for p := range sub.paths {
		out = append(out, p)
	}
	return out
}

// notifyResource sends the current value of `path` to every subscription
// whose path-set contains it.
func (s *Server) notifyResource(path string) {
	value, ok := s.resolveResource(path)
	if !ok {
		return
	}
	msg := map[string]any{path: value}

	s.subsMu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subsMu.Unlock()

	for _, sub := range subs {
		sub.mu.Lock()
		_, subscribed := sub.paths[path]
		sub.mu.Unlock()
		if !subscribed {
			continue
		}
		select {
		case sub.events <- msg:
		default:
			// Drop on slow consumer; slice 2 will surface this via metrics.
		}
	}
}

// resolveResource returns the current value JSON-encodable for the given
// resource path, or false if the resource doesn't exist or isn't supported.
func (s *Server) resolveResource(path string) (any, bool) {
	if id, ok := matchInputPath(path); ok {
		s.mu.RLock()
		defer s.mu.RUnlock()
		in, ok := s.inputs[id]
		if !ok {
			return nil, false
		}
		copyIn := *in
		return &copyIn, true
	}
	return nil, false
}

// HTTP handlers ------------------------------------------------------------

func (s *Server) handleListInputs(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	out := make([]*Input, 0, len(s.inputs))
	for _, in := range s.inputs {
		copyIn := *in
		out = append(out, &copyIn)
	}
	s.mu.RUnlock()
	sortInputsByID(out)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleGetInput(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	in, ok := s.inputs[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(in)
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sessionUUID := uuid.NewString()
	sub := &subscription{
		sessionUUID: sessionUUID,
		events:      make(chan map[string]any, 64),
		paths:       make(map[string]struct{}),
	}

	s.subsMu.Lock()
	s.subs[sessionUUID] = sub
	s.subsMu.Unlock()

	defer func() {
		s.subsMu.Lock()
		if existing, present := s.subs[sessionUUID]; present && existing == sub {
			delete(s.subs, sessionUUID)
			close(sub.events)
		}
		s.subsMu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Content-Location", subscriptionPath(sessionUUID))
	w.WriteHeader(http.StatusOK)

	// Open event per SSCv2 spec.
	openPayload := map[string]any{
		"path":        subscriptionPath(sessionUUID),
		"sessionUUID": sessionUUID,
	}
	openJSON, _ := json.Marshal(openPayload)
	fmt.Fprintf(w, "event: open\ndata: %s\n\n", openJSON)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-sub.events:
			if !open {
				fmt.Fprint(w, "event: close\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleSetSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, ok := s.lookupSubscription(id)
	if !ok {
		http.Error(w, "session not found", http.StatusUnprocessableEntity)
		return
	}
	var paths []string
	if err := json.NewDecoder(r.Body).Decode(&paths); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	added, errs := s.applyPathsTransaction(sub, paths, replaceMode)
	if len(errs) > 0 {
		writePathError(w, errs[0])
		return
	}
	w.WriteHeader(http.StatusOK)
	for _, p := range added {
		s.notifyResource(p)
	}
}

func (s *Server) handleAddSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, ok := s.lookupSubscription(id)
	if !ok {
		http.Error(w, "session not found", http.StatusUnprocessableEntity)
		return
	}
	var paths []string
	if err := json.NewDecoder(r.Body).Decode(&paths); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	added, errs := s.applyPathsTransaction(sub, paths, addMode)
	if len(errs) > 0 {
		writePathError(w, errs[0])
		return
	}
	w.WriteHeader(http.StatusOK)
	for _, p := range added {
		s.notifyResource(p)
	}
}

func (s *Server) handleRemoveSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, ok := s.lookupSubscription(id)
	if !ok {
		http.Error(w, "session not found", http.StatusUnprocessableEntity)
		return
	}
	var paths []string
	if err := json.NewDecoder(r.Body).Decode(&paths); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	sub.mu.Lock()
	for _, p := range paths {
		delete(sub.paths, p)
	}
	sub.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.subsMu.Lock()
	sub, ok := s.subs[id]
	if ok {
		delete(s.subs, id)
		close(sub.events)
	}
	s.subsMu.Unlock()
	if !ok {
		http.Error(w, "not found", http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pathMode int

const (
	addMode pathMode = iota
	replaceMode
)

type pathError struct {
	Path  string `json:"path"`
	Error int    `json:"error"`
}

// applyPathsTransaction validates each path can be resolved; on first failure
// it returns the existing subscription unchanged (transactional semantics per
// spec). Otherwise it updates the path set and returns the set of newly-added
// paths (so the caller can push initial values for them).
func (s *Server) applyPathsTransaction(sub *subscription, paths []string, mode pathMode) ([]string, []pathError) {
	for _, p := range paths {
		if _, ok := s.resolveResource(p); !ok {
			return nil, []pathError{{Path: p, Error: http.StatusNotFound}}
		}
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if mode == replaceMode {
		// Reset path set, but recompute "added" against the OLD set so
		// already-subscribed paths don't re-emit duplicates.
		old := sub.paths
		sub.paths = make(map[string]struct{}, len(paths))
		var added []string
		for _, p := range paths {
			sub.paths[p] = struct{}{}
			if _, was := old[p]; !was {
				added = append(added, p)
			}
		}
		return added, nil
	}
	var added []string
	for _, p := range paths {
		if _, exists := sub.paths[p]; exists {
			continue
		}
		sub.paths[p] = struct{}{}
		added = append(added, p)
	}
	return added, nil
}

func writePathError(w http.ResponseWriter, e pathError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(e)
}

func (s *Server) lookupSubscription(sessionUUID string) (*subscription, bool) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	sub, ok := s.subs[sessionUUID]
	return sub, ok
}

// Auth ---------------------------------------------------------------------

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != s.username || p != s.password {
			authFail.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="spectera"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

var authFail atomic.Uint64

// AuthFailures returns the cumulative count of unauthorized requests.
func AuthFailures() uint64 { return authFail.Load() }

// Helpers ------------------------------------------------------------------

func sortInputsByID(s []*Input) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].ID > s[j].ID; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func inputPath(id int) string             { return "/api/audio/inputs/" + strconv.Itoa(id) }
func subscriptionPath(uuid string) string { return "/api/ssc/state/subscriptions/" + uuid }

func matchInputPath(path string) (int, bool) {
	const prefix = "/api/audio/inputs/"
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	id, err := strconv.Atoi(path[len(prefix):])
	if err != nil {
		return 0, false
	}
	return id, true
}

// BasicAuthHeader is exported so tests can construct auth headers easily.
func BasicAuthHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}
