package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/events"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

const (
	ServerVersion = "0.1.0"
	MeteringMaxHz = 20

	writeTimeout = 5 * time.Second
)

// Server upgrades HTTP requests to WebSocket and bridges the bus to clients.
type Server struct {
	bus    *bus.Bus
	store  *state.Store
	sink   *events.Sink
	logger *slog.Logger
}

func NewServer(b *bus.Bus, store *state.Store, sink *events.Sink, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{bus: b, store: store, sink: sink, logger: logger}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Single-user local app: any origin is acceptable for v1.
			// Tighten when authentication / multi-user lands.
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			s.logger.Warn("ws accept failed", "err", err)
			return
		}
		defer c.CloseNow()

		sessionID := uuid.NewString()
		s.logger.Info("ws connected", "session", sessionID, "remote", r.RemoteAddr)
		if err := s.serve(r.Context(), c, sessionID); err != nil {
			s.logger.Info("ws disconnected", "session", sessionID, "err", err)
		} else {
			s.logger.Info("ws disconnected", "session", sessionID)
		}
	})
}

// outgoing wraps a server-to-client payload destined for the writer goroutine.
type outgoing struct{ payload any }

func (s *Server) serve(parent context.Context, c *websocket.Conn, sessionID string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	sub := s.bus.Subscribe(nil, 256)
	defer sub.Close()

	out := make(chan outgoing, 64)

	conn := &conn{
		s:         s,
		c:         c,
		sessionID: sessionID,
		sub:       sub,
		out:       out,
	}

	if err := writeJSON(ctx, c, helloMsg{
		Type:              "hello",
		ServerVersion:     ServerVersion,
		SessionID:         sessionID,
		SupportedMessages: []string{"client_hello", "subscribe", "unsubscribe", "rpc", "ping"},
		MeteringMaxHz:     MeteringMaxHz,
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return conn.writer(gctx) })
	g.Go(func() error { return conn.reader(gctx) })
	err := g.Wait()
	cancel()
	_ = c.Close(websocket.StatusNormalClosure, "")
	return err
}

// conn holds the per-connection state.
type conn struct {
	s         *Server
	c         *websocket.Conn
	sessionID string
	sub       *bus.Subscription
	out       chan outgoing

	mu          sync.Mutex
	subscribed  map[string][]string // subscriptionID -> patterns
}

func (cn *conn) writer(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-cn.out:
			if !ok {
				return nil
			}
			if err := writeJSON(ctx, cn.c, m.payload); err != nil {
				return err
			}
		case msg, ok := <-cn.sub.Updates():
			if !ok {
				return nil
			}
			payload := cn.busToWire(msg)
			if payload == nil {
				continue
			}
			if err := writeJSON(ctx, cn.c, payload); err != nil {
				return err
			}
		}
	}
}

func (cn *conn) reader(ctx context.Context) error {
	for {
		_, data, err := cn.c.Read(ctx)
		if err != nil {
			return err
		}
		var m clientMsg
		if err := json.Unmarshal(data, &m); err != nil {
			cn.send(rpcResultMsg{
				Type: "rpc.result", ID: "", OK: false,
				Error: &rpcError{Code: "internal", Message: "malformed json: " + err.Error()},
			})
			continue
		}
		cn.dispatch(ctx, m)
	}
}

func (cn *conn) dispatch(ctx context.Context, m clientMsg) {
	switch m.Type {
	case "client_hello":
		// Nothing required server-side beyond logging for now.
		cn.s.logger.Debug("client_hello", "session", cn.sessionID, "client_version", m.ClientVersion)
	case "subscribe":
		cn.handleSubscribe(m)
	case "unsubscribe":
		cn.handleUnsubscribe(m)
	case "rpc":
		cn.handleRPC(ctx, m)
	case "ping":
		cn.send(pingMsg{Type: "pong", T: m.T})
	case "pong":
		// no-op
	default:
		cn.s.logger.Debug("unknown ws message", "type", m.Type)
	}
}

func (cn *conn) handleSubscribe(m clientMsg) {
	if m.SubscriptionID == "" || m.Topic == "" {
		cn.send(subscriptionErrorMsg{
			Type: "subscription.error", SubscriptionID: m.SubscriptionID,
			Error: "subscription_id and topic are required",
		})
		return
	}

	patterns, err := topicToPatterns(m.Topic, m.Params)
	if err != nil {
		cn.send(subscriptionErrorMsg{
			Type: "subscription.error", SubscriptionID: m.SubscriptionID,
			Error: err.Error(),
		})
		return
	}

	cn.mu.Lock()
	if cn.subscribed == nil {
		cn.subscribed = make(map[string][]string)
	}
	cn.subscribed[m.SubscriptionID] = patterns
	all := flattenPatterns(cn.subscribed)
	cn.mu.Unlock()

	cn.s.bus.SetPatterns(cn.sub, all)

	confirmed := subscriptionConfirmedMsg{
		Type: "subscription.confirmed", SubscriptionID: m.SubscriptionID,
	}
	confirmed.Snapshot = cn.snapshotFor(m.Topic, m.Params)
	cn.send(confirmed)
}

func (cn *conn) handleUnsubscribe(m clientMsg) {
	cn.mu.Lock()
	delete(cn.subscribed, m.SubscriptionID)
	all := flattenPatterns(cn.subscribed)
	cn.mu.Unlock()
	cn.s.bus.SetPatterns(cn.sub, all)
}

func (cn *conn) handleRPC(_ context.Context, m clientMsg) {
	// Phase 1: RPC isn't required by the end-of-phase test. Reply with a
	// structured "unsupported" error so the client can detect lack of support.
	cn.send(rpcResultMsg{
		Type: "rpc.result", ID: m.ID, OK: false,
		Error: &rpcError{Code: "unsupported", Message: "rpc method not implemented in Phase 1: " + m.Method},
	})
}

func (cn *conn) send(payload any) {
	select {
	case cn.out <- outgoing{payload: payload}:
	default:
		cn.s.logger.Warn("ws outbox full, dropping message", "session", cn.sessionID)
	}
}

// busToWire converts a bus.Message into a wire payload struct.
func (cn *conn) busToWire(m bus.Message) any {
	switch p := m.Payload.(type) {
	case events.StatePatchEvent:
		return channelStateMsg{
			Type:      "channel.state",
			ChannelID: p.ChannelID,
			TS:        p.Time.UnixMilli(),
			Patch:     p.Patch,
		}
	case events.ChannelAddedEvent:
		return channelAddedMsg{
			Type:    "channel.added",
			Channel: channelDTOFrom(p.Snapshot),
		}
	case events.ChannelRemovedEvent:
		return channelRemovedMsg{
			Type: "channel.removed", ChannelID: p.ChannelID,
		}
	case events.DeviceStatusEvent:
		return deviceStatusMsg{
			Type: "device.status", DeviceID: p.AdapterID, Status: p.Status,
		}
	case events.AlertFiredEvent:
		return alertFiredMsg{
			Type:      "alert.fired",
			ChannelID: p.ChannelID,
			Severity:  p.Severity,
			Category:  p.Category,
			Code:      p.Code,
			Message:   p.Message,
			Payload:   p.Detail,
			TS:        p.Time.UnixMilli(),
		}
	default:
		cn.s.logger.Warn("unknown bus payload type", "topic", m.Topic, "payload_type", fmt.Sprintf("%T", p))
		return nil
	}
}

// snapshotFor returns the snapshot payload included in subscription.confirmed.
func (cn *conn) snapshotFor(topic string, _ json.RawMessage) any {
	switch topic {
	case "channel.lifecycle":
		all := cn.s.store.All()
		out := make([]channelDTO, 0, len(all))
		for _, snap := range all {
			out = append(out, channelDTOFrom(snap))
		}
		return map[string]any{"channels": out}
	case "device.status":
		return map[string]any{"devices": cn.s.sink.Statuses()}
	default:
		// channel.state subscriptions get their initial values as the next
		// patch tick arrives — no inline snapshot needed.
		return nil
	}
}

// topicToPatterns translates a client-facing topic + params into bus pattern
// strings. Returns an error for malformed input.
func topicToPatterns(topic string, paramsRaw json.RawMessage) ([]string, error) {
	switch topic {
	case "channel.lifecycle", "device.status", "alerts":
		return []string{topic}, nil
	case "channel.state.*":
		var p subscribeParams
		if len(paramsRaw) > 0 {
			if err := json.Unmarshal(paramsRaw, &p); err != nil {
				return nil, fmt.Errorf("invalid params for channel.state.*: %w", err)
			}
		}
		if len(p.ChannelIDs) == 0 {
			return []string{"channel.state.*"}, nil
		}
		patterns := make([]string, 0, len(p.ChannelIDs))
		for _, id := range p.ChannelIDs {
			patterns = append(patterns, fmt.Sprintf("channel.state.%d", id))
		}
		return patterns, nil
	default:
		// e.g. "channel.state.5"
		return []string{topic}, nil
	}
}

func flattenPatterns(subs map[string][]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, pats := range subs {
		for _, p := range pats {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// JSON write helper with timeout.
func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := c.Write(wctx, websocket.MessageText, data); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("write timeout: %w", err)
		}
		return err
	}
	return nil
}
