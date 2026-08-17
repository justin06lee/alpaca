package webui

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/session"
)

// testServer stands up a GUI backed by the canned demo client and a sandboxed
// session store — the same stack `alpaca gui --demo` runs.
func testServer(t *testing.T) (*Server, *httptest.Server, *session.Store) {
	t.Helper()
	t.Setenv("ALPACA_HOME", t.TempDir())

	c, stop, err := client.NewDemo()
	if err != nil {
		t.Fatalf("NewDemo: %v", err)
	}
	t.Cleanup(stop)

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	srv, err := New(Connected(c), store, "demo", "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, store
}

func do(t *testing.T, srv *Server, method, url string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// waitReady polls boot until the background dial lands.
func waitReady(t *testing.T, srv *Server, ts *httptest.Server) bootReply {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := do(t, srv, "GET", ts.URL+"/api/boot", "")
		var boot bootReply
		if err := json.NewDecoder(res.Body).Decode(&boot); err != nil {
			t.Fatalf("decode boot: %v", err)
		}
		res.Body.Close()
		if boot.Status == "ready" {
			return boot
		}
		if time.Now().After(deadline) {
			t.Fatalf("never became ready: %+v", boot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The API refuses calls without the page's bearer token — a stray local
// process or hostile web page cannot drive the GUI.
func TestAPIRequiresTheToken(t *testing.T) {
	_, ts, _ := testServer(t)

	res, err := http.Get(ts.URL + "/api/boot")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("tokenless request got %d, want 401", res.StatusCode)
	}
}

// A DNS-rebound hostname fails the loopback host check even with the token.
func TestAPIPinsTheHost(t *testing.T) {
	srv, ts, _ := testServer(t)

	req, _ := http.NewRequest("GET", ts.URL+"/api/boot", nil)
	req.Header.Set("Authorization", "Bearer "+srv.Token())
	req.Host = "evil.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("rebound host got %d, want 403", res.StatusCode)
	}
}

// The page arrives with the token injected, so the browser can talk back.
func TestIndexInjectsTheToken(t *testing.T) {
	srv, ts, _ := testServer(t)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	if _, err := copyBody(&b, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), srv.Token()) {
		t.Error("served page is missing the token")
	}
	if strings.Contains(b.String(), "__ALPACA_TOKEN__") {
		t.Error("token placeholder survived")
	}
}

func copyBody(b *strings.Builder, res *http.Response) (int64, error) {
	buf := make([]byte, 32*1024)
	var n int64
	for {
		read, err := res.Body.Read(buf)
		b.Write(buf[:read])
		n += int64(read)
		if err != nil {
			if err.Error() == "EOF" {
				return n, nil
			}
			return n, err
		}
	}
}

// One send streams deltas and leaves a saved two-message session behind —
// in the same store the TUI reads.
func TestChatStreamsAndPersists(t *testing.T) {
	srv, ts, store := testServer(t)
	boot := waitReady(t, srv, ts)
	if boot.Model == "" {
		t.Fatal("boot reported no model")
	}

	res := do(t, srv, "POST", ts.URL+"/api/chat",
		`{"model":"`+boot.Model+`","text":"hello there"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("chat got %d", res.StatusCode)
	}

	var sessionID, reply string
	var sawDone bool
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		var ev chatEvent
		if err := json.Unmarshal(scan.Bytes(), &ev); err != nil {
			t.Fatalf("bad stream line %q: %v", scan.Text(), err)
		}
		switch ev.Type {
		case "session":
			sessionID = ev.ID
		case "delta":
			reply += ev.Content
		case "done":
			sawDone = true
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if !sawDone {
		t.Fatal("stream ended without a done event")
	}
	if reply == "" {
		t.Fatal("no content streamed")
	}

	sess, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if len(sess.Messages) != 2 {
		t.Fatalf("saved %d messages, want the prompt and the reply", len(sess.Messages))
	}
	if sess.Messages[0].Content != "hello there" {
		t.Errorf("prompt saved as %q", sess.Messages[0].Content)
	}

	// The saved chat shows up on boot, and can be fetched and deleted.
	boot = waitReady(t, srv, ts)
	if len(boot.Chats) != 1 {
		t.Fatalf("boot lists %d chats, want 1", len(boot.Chats))
	}
	res = do(t, srv, "GET", ts.URL+"/api/sessions/"+sessionID, "")
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("fetch session got %d", res.StatusCode)
	}
	res = do(t, srv, "DELETE", ts.URL+"/api/sessions/"+sessionID, "")
	res.Body.Close()
	if _, err := store.Load(sessionID); err == nil {
		t.Error("session survived deletion")
	}
}

// A second send into the same session carries the history forward.
func TestChatContinuesASession(t *testing.T) {
	srv, ts, store := testServer(t)
	boot := waitReady(t, srv, ts)

	first := do(t, srv, "POST", ts.URL+"/api/chat",
		`{"model":"`+boot.Model+`","text":"first question"}`)
	var sessionID string
	scan := bufio.NewScanner(first.Body)
	for scan.Scan() {
		var ev chatEvent
		_ = json.Unmarshal(scan.Bytes(), &ev)
		if ev.Type == "session" {
			sessionID = ev.ID
		}
	}
	first.Body.Close()

	second := do(t, srv, "POST", ts.URL+"/api/chat",
		`{"session_id":"`+sessionID+`","model":"`+boot.Model+`","text":"second question"}`)
	scan = bufio.NewScanner(second.Body)
	for scan.Scan() {
	}
	second.Body.Close()

	sess, err := store.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages) != 4 {
		t.Fatalf("continued session has %d messages, want 4", len(sess.Messages))
	}
}

// The idle clock counts silence, and streams in flight pin it busy.
func TestIdleAccounting(t *testing.T) {
	srv, ts, _ := testServer(t)
	waitReady(t, srv, ts)

	idle, busy := srv.IdleSince()
	if busy {
		t.Error("no stream running, but reported busy")
	}
	if idle > time.Second {
		t.Errorf("just touched, but idle for %s", idle)
	}
}
