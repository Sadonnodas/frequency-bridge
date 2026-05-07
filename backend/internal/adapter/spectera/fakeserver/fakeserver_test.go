package fakeserver_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter/spectera/fakeserver"
)

func newServer(t *testing.T) (*fakeserver.Server, *httptest.Server, *http.Client) {
	t.Helper()
	fake := fakeserver.New("api", "secret")
	ts := httptest.NewTLSServer(fake.Handler())
	t.Cleanup(ts.Close)
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	return fake, ts, httpClient
}

func TestAuthRequired(t *testing.T) {
	_, ts, c := newServer(t)
	resp, err := c.Get(ts.URL + "/api/audio/inputs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate")
	}
}

func TestListInputsWithCorrectCreds(t *testing.T) {
	fake, ts, c := newServer(t)
	fake.AddInput(2, "Vox 2", 567550)
	fake.AddInput(1, "Vox 1", 562275)

	req, _ := http.NewRequest("GET", ts.URL+"/api/audio/inputs", nil)
	req.SetBasicAuth("api", "secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got []fakeserver.Input
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("not sorted by ID: %+v", got)
	}
}

func TestSubscribeOpenEventThenSubscribeAndReceiveUpdates(t *testing.T) {
	fake, ts, c := newServer(t)
	fake.AddInput(1, "Vox 1", 562275)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/ssc/state/subscriptions", nil)
	req.SetBasicAuth("api", "secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q", got)
	}
	loc := resp.Header.Get("Content-Location")
	if !strings.HasPrefix(loc, "/api/ssc/state/subscriptions/") {
		t.Errorf("Content-Location = %q", loc)
	}

	br := bufio.NewReader(resp.Body)

	// Read the open event and extract sessionUUID.
	openEv := readEvent(t, br)
	if openEv.kind != "open" {
		t.Fatalf("first event = %q, want open", openEv.kind)
	}
	var openPayload struct {
		Path        string `json:"path"`
		SessionUUID string `json:"sessionUUID"`
	}
	if err := json.Unmarshal([]byte(openEv.data), &openPayload); err != nil {
		t.Fatalf("open payload: %v", err)
	}
	if openPayload.SessionUUID == "" {
		t.Fatal("sessionUUID missing in open event")
	}
	if openPayload.Path != loc {
		t.Errorf("open path = %q, want %q", openPayload.Path, loc)
	}

	// Now register interest in /api/audio/inputs/1.
	body, _ := json.Marshal([]string{"/api/audio/inputs/1"})
	putReq, _ := http.NewRequest(http.MethodPut, ts.URL+loc, bytes.NewReader(body))
	putReq.SetBasicAuth("api", "secret")
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := c.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", putResp.StatusCode)
	}

	// Initial value of input 1 should arrive on the SSE stream.
	initialEv := readEvent(t, br)
	if initialEv.kind != "message" && initialEv.kind != "" {
		t.Fatalf("initial event = %q, want message", initialEv.kind)
	}
	var initial map[string]fakeserver.Input
	if err := json.Unmarshal([]byte(initialEv.data), &initial); err != nil {
		t.Fatalf("initial data: %v (raw=%q)", err, initialEv.data)
	}
	in1, ok := initial["/api/audio/inputs/1"]
	if !ok || in1.ID != 1 || in1.Frequency != 562275 {
		t.Fatalf("initial input mismatch: %+v", initial)
	}

	// Trigger an update; expect a fresh resource-keyed event.
	fake.UpdateInput(1, func(in *fakeserver.Input) { in.Frequency = 580125 })
	updateEv := readEvent(t, br)
	var updated map[string]fakeserver.Input
	if err := json.Unmarshal([]byte(updateEv.data), &updated); err != nil {
		t.Fatalf("update data: %v", err)
	}
	if updated["/api/audio/inputs/1"].Frequency != 580125 {
		t.Errorf("frequency = %d, want 580125", updated["/api/audio/inputs/1"].Frequency)
	}

	// Sanity: server reports paths we registered.
	paths := fake.SubscriptionPaths(openPayload.SessionUUID)
	if len(paths) != 1 || paths[0] != "/api/audio/inputs/1" {
		t.Errorf("SubscriptionPaths = %v, want [/api/audio/inputs/1]", paths)
	}
}

func TestSubscribeAddIncrementally(t *testing.T) {
	fake, ts, c := newServer(t)
	fake.AddInput(1, "A", 562275)
	fake.AddInput(2, "B", 567550)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/ssc/state/subscriptions", nil)
	req.SetBasicAuth("api", "secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Content-Location")
	br := bufio.NewReader(resp.Body)
	openEv := readEvent(t, br)
	var openPayload struct {
		SessionUUID string `json:"sessionUUID"`
	}
	_ = json.Unmarshal([]byte(openEv.data), &openPayload)

	// First subscribe to input 1 only.
	put := func(suffix, listJSON string) int {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+loc+suffix, strings.NewReader(listJSON))
		req.SetBasicAuth("api", "secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := put("", `["/api/audio/inputs/1"]`); got != 200 {
		t.Fatalf("set status = %d", got)
	}
	_ = readEvent(t, br) // initial input 1

	// Now incrementally add input 2.
	if got := put("/add", `["/api/audio/inputs/2"]`); got != 200 {
		t.Fatalf("add status = %d", got)
	}
	addEv := readEvent(t, br)
	var got map[string]fakeserver.Input
	_ = json.Unmarshal([]byte(addEv.data), &got)
	if got["/api/audio/inputs/2"].ID != 2 {
		t.Errorf("expected initial value of input 2 after add, got %+v", got)
	}

	paths := fake.SubscriptionPaths(openPayload.SessionUUID)
	if len(paths) != 2 {
		t.Errorf("paths count = %d, want 2 (%v)", len(paths), paths)
	}
}

// readEvent reads one SSE event (lines until a blank line).
type sseEvent struct {
	kind string
	data string
}

func readEvent(t *testing.T, br *bufio.Reader) sseEvent {
	t.Helper()
	var ev sseEvent
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF && line == "" {
			t.Fatalf("unexpected EOF")
		}
		if err != nil && err != io.EOF {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if ev.kind != "" || ev.data != "" {
				return ev
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if ev.data == "" {
				ev.data = strings.TrimPrefix(line, "data: ")
			} else {
				ev.data += "\n" + strings.TrimPrefix(line, "data: ")
			}
		}
	}
}
