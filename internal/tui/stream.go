package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
)

// repaintInterval decouples the repaint rate from the token rate. A fast model
// emits tokens far quicker than a terminal can usefully redraw, and repainting
// per token would burn CPU re-running the markdown parser for frames nobody
// sees.
const repaintInterval = 80 * time.Millisecond

// Messages.
type (
	tickMsg          time.Time
	splashTickMsg    time.Time
	statusExpiredMsg int
	streamClosedMsg  struct{}

	modelsMsg struct {
		models []client.Model
		err    error
	}

	connectedMsg struct {
		client *client.Client
		err    error
	}

	searchDoneMsg struct {
		query   string
		results []client.SearchResult
		err     error
	}

	// streamEvent is one item from the in-flight reply.
	streamEvent struct {
		content string
		usage   *client.Usage
		finish  string
		err     error
		// note describes work the gateway did mid-turn, such as a web search.
		note string
	}
)

// splashInterval is one scanline. Slow enough to read as a sweep, quick enough
// that the whole reveal is over before it becomes something to sit through.
const splashInterval = 35 * time.Millisecond

// splashHold is how many ticks the finished image stays up.
const splashHold = 12

// splashGrace is how many frames must pass before a keypress can dismiss the
// opening. It exists to outlast the replies a terminal sends to a TUI's startup
// queries, which would otherwise skip it before it is visible.
const splashGrace = 8

func splashTick() tea.Cmd {
	return tea.Tick(splashInterval, func(t time.Time) tea.Msg { return splashTickMsg(t) })
}

func tick() tea.Cmd {
	return tea.Tick(repaintInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// connectCmd opens the connection off the update loop, so the opening keeps
// animating while routes are raced.
func connectCmd(connect Connector) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		c, err := connect(ctx)
		return connectedMsg{client: c, err: err}
	}
}

func loadModels(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		models, err := c.Models(ctx)
		return modelsMsg{models: models, err: err}
	}
}

// startStream kicks off a completion for the current conversation.
func (m *Model) startStream() tea.Cmd {
	if m.streaming {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	m.streaming = true
	m.streamBuf = ""
	m.streamErr = nil
	m.streamNotes = nil
	m.dirty = false
	m.streamStart = time.Now()
	m.lastUsage = nil

	// Buffered so a burst of tokens does not block the reader goroutine
	// between Bubble Tea update cycles.
	events := make(chan streamEvent, 128)
	m.events = events

	req := client.ChatRequest{Model: m.sess.Model, Messages: m.sess.Wire()}
	c := m.client

	go func() {
		defer close(events)

		err := c.Chat(ctx, req, func(ch client.Chunk) error {
			if ch.Content == "" && ch.Usage == nil && ch.FinishReason == "" && ch.Event == "" {
				return nil
			}
			select {
			case events <- streamEvent{
				content: ch.Content, usage: ch.Usage, finish: ch.FinishReason,
				note: describeEvent(ch),
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
		if err != nil {
			select {
			case events <- streamEvent{err: err}:
			case <-ctx.Done():
			default:
			}
		}
	}()

	m.refreshViewport(true)
	return tea.Batch(waitForEvent(events), m.spinner.Tick, tick())
}

// waitForEvent reads one event. Bubble Tea commands run once, so each event
// schedules the next read.
func waitForEvent(ch chan streamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return ev
	}
}

// describeEvent renders a gateway event for the transcript.
func describeEvent(ch client.Chunk) string {
	switch ch.Event {
	case "":
		return ""
	case "search":
		return "searched the web — " + ch.Detail
	case "search_failed":
		return "web search failed — " + ch.Detail
	default:
		return ch.Event + " " + ch.Detail
	}
}

func (m *Model) handleStreamEvent(ev streamEvent) tea.Cmd {
	if ev.err != nil {
		m.streamErr = ev.err
	}
	if ev.note != "" {
		m.streamNotes = append(m.streamNotes, ev.note)
		m.dirty = true
	}
	if ev.content != "" {
		m.streamBuf += ev.content
		m.dirty = true
	}
	if ev.usage != nil {
		m.lastUsage = ev.usage
	}
	return waitForEvent(m.events)
}

// finishStream commits the reply once the event channel closes.
func (m *Model) finishStream() tea.Cmd {
	if !m.streaming {
		return nil
	}
	m.streaming = false
	m.lastElapsed = time.Since(m.streamStart)

	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}

	text := strings.TrimSpace(m.streamBuf)
	m.streamBuf = ""

	// Partial output is kept even when the stream failed or was interrupted:
	// half an answer is usually still worth having, and silently discarding
	// what the user watched appear is worse than showing it with a note.
	if text != "" {
		m.sess.Append(client.Message{Role: client.RoleAssistant, Content: text})
		m.cacheLast()
	}

	var cmd tea.Cmd
	switch {
	case m.streamErr == nil:
		// Success.
	case errors.Is(m.streamErr, context.Canceled):
		cmd = m.setStatus("stopped", false)
	default:
		cmd = m.setStatus(m.streamErr.Error(), true)
	}
	m.streamErr = nil

	m.refreshViewport(true)
	m.input.Focus()

	if saveCmd := m.persist(); saveCmd != nil && cmd == nil {
		cmd = saveCmd
	}
	return tea.Batch(cmd, textarea.Blink)
}

// stopStream cancels an in-flight reply. The channel close that follows drives
// finishStream, so cleanup happens in exactly one place.
func (m *Model) stopStream() {
	if m.streamCancel != nil {
		m.streamCancel()
	}
}
