// Package tui is the terminal chat interface.
package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
)

// Layout constants, in terminal rows.
const (
	headerHeight = 4 // three rows of the pixel head plus a blank
	inputHeight  = composerMinRows
	chromeHeight = headerHeight + inputHeight + 2
)

// uiPadX is the air between the terminal's edge and the interface: the header,
// the transcript, the composer, and the status bar all keep this many columns
// clear on both sides. The transcript's padding is baked into the viewport
// content itself, so mouse hit-testing against paneLines and the on-screen
// columns remain the same coordinate space.
const uiPadX = 2

// statusLifetime is how long a transient message stays on the status bar.
const statusLifetime = 4 * time.Second

// Connector opens the connection to a server. The interface takes one rather
// than a ready client so the opening animation can cover the connection, which
// on a cold start is the slowest part: racing routes and waiting on an mDNS
// scan takes long enough that a blank terminal looks like a hang.
type Connector func(context.Context) (*client.Client, error)

// Connected wraps an already-open client, for demo mode and tests.
func Connected(c *client.Client) Connector {
	return func(context.Context) (*client.Client, error) { return c, nil }
}

// Model is the Bubble Tea model for the chat interface.
type Model struct {
	connect     Connector
	connectErr  error
	client      *client.Client
	store       *session.Store
	profiles    *config.Profiles
	profileName string

	sess   *session.Session
	models []client.Model
	// rendered caches formatted messages so a repaint during streaming does
	// not re-run the markdown parser over the whole history.
	rendered []string

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	renderer *glamour.TermRenderer

	width, height int
	ready         bool

	mode     pickerKind
	pick     picker
	showHelp bool

	// attachments hold oversized pastes and dropped-in images out of the
	// composer text until send; each shows as a chip above the input.
	attachments []attachment
	// attachFocus is which chip the arrow keys are on; -1 while typing.
	attachFocus int
	// viewAttach is the attachment open in the full-content popup; -1 closed.
	viewAttach int
	// attachScroll is the popup's scroll offset, in lines.
	attachScroll int

	// paneLines mirrors the viewport's content so a mouse click can be mapped
	// back to the transcript line under it, and msgRanges records which of
	// those lines belong to which message.
	paneLines []string
	msgRanges []msgRange

	// viewMsg is the sent message open in the full-content popup; -1 closed.
	viewMsg int

	// editFrom is the message index an edit-in-progress will branch from;
	// -1 when the composer holds an ordinary new message.
	editFrom int

	// The /graph view's state (see graph.go): the flattened tree, cursor,
	// scroll, and the progress of the summarization chain.
	graphOpen  bool
	graphRows  []graphRow
	graphCur   int
	graphOff   int
	graphBusy  bool
	graphDone  int
	graphTotal int

	// blockSeq numbers code blocks as the transcript renders; copiedBlock is
	// the one whose control reads "copied!" until the flash expires, and
	// copiedSeq guards stale expiry timers the same way statusSeq does.
	blockSeq    int
	copiedBlock int
	copiedSeq   int

	// sel is the mouse-drag text selection, and lastFrame is the frame it was
	// drawn over — the copy on release reads from exactly what was on screen.
	sel       selection
	lastFrame string

	// splashScan is how many rows of the opening image have been painted.
	splashScan int
	splashDone bool

	// greeting is picked once per session so an empty screen does not reshuffle
	// its own wording on every repaint.
	greeting string
	// sliding tracks the composer's journey from the middle of an empty screen
	// down to its dock, which happens once, on the first message.
	sliding    bool
	slideStart time.Time
	// thinkingIdx rotates the status shown while waiting on the first token.
	thinkingIdx  int
	thinkingStep int

	streaming bool
	streamBuf string
	// streamNotes records what the gateway did mid-turn, e.g. a web search.
	streamNotes  []string
	streamErr    error
	streamCancel context.CancelFunc
	events       chan streamEvent
	// dirty marks that streamBuf changed since the last repaint.
	dirty       bool
	streamStart time.Time

	lastUsage   *client.Usage
	lastElapsed time.Duration

	status    string
	statusErr bool
	statusSeq int

	quitting bool
}

// New builds the chat interface. The connector runs while the opening plays.
func New(connect Connector, store *session.Store, profiles *config.Profiles, profileName string, sess *session.Session) *Model {
	input := textarea.New()
	input.Placeholder = "Ask anything…"
	input.ShowLineNumbers = false
	// The composer already has a border; the textarea's own prompt bar would be
	// a second frame inside the first.
	input.Prompt = ""
	input.CharLimit = 0 // long pasted prompts are legitimate
	input.SetHeight(inputHeight)
	input.Focus()
	// Enter is intercepted as "send", so the textarea must not also insert a
	// newline for it.
	input.KeyMap.InsertNewline.SetEnabled(false)
	// The textarea's own ctrl+v reads the clipboard behind the interface's
	// back; disabling it keeps bracketed paste the single path in, which is
	// where oversized pastes get staged as attachments.
	input.KeyMap.Paste.SetEnabled(false)

	spin := spinner.New()
	spin.Spinner = spinner.Dot

	return &Model{
		connect:  connect,
		greeting: randomGreeting(),
		// A size is assumed until the terminal reports one, so the opening can
		// draw immediately instead of showing a "starting…" placeholder.
		width:       80,
		height:      24,
		store:       store,
		profiles:    profiles,
		profileName: profileName,
		sess:        sess,
		input:       input,
		spinner:     spin,
		attachFocus: -1,
		viewAttach:  -1,
		viewMsg:     -1,
		editFrom:    -1,
		copiedBlock: -1,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, splashTick(), connectCmd(m.connect))
}

// Err reports a failure that ended the session, so the command line can print
// it properly instead of the interface swallowing it.
func (m *Model) Err() error { return m.connectErr }

// splashTotal is how many ticks the opening runs for: long enough to draw every
// row, and never shorter than the minimum the image stays up for.
func (m *Model) splashTotal() int {
	return maxInt(layoutFor(m.width, m.height).steps(), splashMinTicks)
}

// splashTagline names what the interface is about to connect to.
func (m *Model) splashTagline() string {
	if m.client == nil {
		return "connecting…"
	}
	if m.client.Route().Source == client.SourceDemo {
		return "offline demo · canned replies, no network"
	}
	return m.profileName + " · " + m.client.Route().Endpoint
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		return m, m.resize(msg.Width, msg.Height)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case modelsMsg:
		return m, m.handleModels(msg)

	case searchDoneMsg:
		return m, m.applySearch(msg)

	case graphSumMsg:
		return m, m.handleGraphSum(msg)

	case streamEvent:
		return m, m.handleStreamEvent(msg)

	case streamClosedMsg:
		return m, m.finishStream()

	case connectedMsg:
		if msg.err != nil {
			// Nothing to chat with; let the command line report why.
			m.connectErr = msg.err
			m.quitting = true
			return m, tea.Quit
		}
		m.client = msg.client
		return m, loadModels(m.client)

	case slideTickMsg:
		if !m.sliding {
			return m, nil
		}
		if time.Since(m.slideStart) >= slideDuration {
			m.sliding = false
			return m, nil
		}
		return m, slideTick()

	case splashTickMsg:
		if m.splashDone {
			return m, nil
		}
		m.splashScan++
		// The opening also serves as the loading screen, so it holds until the
		// connection resolves rather than handing over to an interface that has
		// nothing behind it yet.
		if m.splashScan > m.splashTotal() && m.client != nil {
			m.splashDone = true
			// Deferred while the opening had the screen; do it now.
			m.rebuildCache()
			m.refreshViewport(true)
			return m, nil
		}
		return m, splashTick()

	case tickMsg:
		if !m.streaming {
			return m, nil
		}
		// Roughly every two seconds, so it reads as progress rather than noise.
		m.thinkingStep++
		if m.thinkingStep%26 == 0 {
			m.thinkingIdx++
		}
		if m.dirty {
			m.dirty = false
			m.refreshViewport(true)
		}
		return m, tick()

	case statusExpiredMsg:
		// Only clear if no newer status has replaced this one.
		if int(msg) == m.statusSeq {
			m.status = ""
			m.statusErr = false
		}
		return m, nil

	case copiedExpiredMsg:
		// Only revert if this timer belongs to the latest copy.
		if int(msg) == m.copiedSeq && m.copiedBlock >= 0 {
			m.copiedBlock = -1
			m.rebuildCache()
			m.refreshViewport(true)
		}
		return m, nil

	case spinner.TickMsg:
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// While no tokens have arrived the spinner is the only moving thing,
		// so the pane has to repaint with it.
		if m.streamBuf == "" {
			m.refreshViewport(true)
		}
		return m, cmd
	}

	return m, nil
}

// resize recomputes the layout and re-renders at the new width.
func (m *Model) resize(width, height int) tea.Cmd {
	m.width, m.height = width, height
	// The old screen's cell coordinates mean nothing on the new one.
	m.clearSelection()

	paneHeight := height - chromeHeight
	if paneHeight < 3 {
		paneHeight = 3
	}

	if !m.ready {
		m.viewport = viewport.New(width, paneHeight)
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = paneHeight
	}
	m.input.SetWidth(width)

	// The markdown renderer is width-bound, so it is discarded here and rebuilt
	// on demand. Building it eagerly would put it on the startup path, and
	// glamour's auto style asks the terminal for its background colour, which
	// means waiting on a reply before the opening can paint.
	m.renderer = nil
	if m.splashDone {
		m.rebuildCache()
		m.refreshViewport(true)
	}

	return nil
}

// refreshViewport rewrites the chat pane, optionally sticking to the bottom.
func (m *Model) refreshViewport(follow bool) {
	if !m.ready {
		return
	}
	// Only auto-scroll if the user was already at the bottom; yanking the view
	// down while they are reading scrollback is infuriating.
	atBottom := m.viewport.AtBottom()
	content := padLines(m.conversation(), uiPadX)
	m.paneLines = strings.Split(content, "\n")
	m.viewport.SetContent(content)
	if follow && atBottom {
		m.viewport.GotoBottom()
	}
}

// setStatus shows a transient message on the status bar.
func (m *Model) setStatus(text string, isErr bool) tea.Cmd {
	m.status = text
	m.statusErr = isErr
	m.statusSeq++
	seq := m.statusSeq
	return tea.Tick(statusLifetime, func(time.Time) tea.Msg { return statusExpiredMsg(seq) })
}

func (m *Model) handleModels(msg modelsMsg) tea.Cmd {
	if msg.err != nil {
		return m.setStatus("could not list models: "+msg.err.Error(), true)
	}
	m.models = msg.models

	// A brand new session has no model yet; adopt the server's first one so the
	// user can start typing immediately instead of hunting through a picker.
	if m.sess.Model == "" && len(m.models) > 0 {
		m.sess.Model = m.models[0].ID
		m.rebuildCache()
		m.refreshViewport(true)
	}
	return nil
}

// View renders the frame, remembers it for the selection machinery, and
// paints any active highlight on top.
func (m *Model) View() string {
	frame := m.viewFrame()
	m.lastFrame = frame
	return m.applySelection(frame)
}

func (m *Model) viewFrame() string {
	if m.quitting {
		return ""
	}

	if !m.splashDone {
		return renderSplash(m.width, m.height, m.splashScan, m.splashTagline())
	}

	// The pickers and the help float over the conversation rather than
	// replacing it — the chat is the context those decisions are made in.
	// The graph is the exception: a tree wants the whole screen.
	base := m.baseView()
	if m.graphOpen {
		base = m.graphView()
	}
	switch {
	case m.showHelp:
		return m.helpOverlay(base)
	case m.mode == pickerModel, m.mode == pickerGraphModel:
		return m.modelPopover(base)
	case m.mode == pickerSession:
		return m.sessionSidebar(base)
	case m.viewAttach >= 0:
		return m.attachmentPopover(base)
	case m.viewMsg >= 0:
		return m.messagePopover(base)
	}
	return base
}

// baseView is the conversation itself: an untouched one gets the composer in
// the middle of the screen; everything else is the docked layout, including
// the frames where the composer is still on its way down.
func (m *Model) baseView() string {
	if m.sess.Empty() && !m.streaming && !m.sliding {
		return m.welcomeView()
	}
	return m.chatView()
}

// persist saves the session, surfacing failures rather than losing history
// silently.
func (m *Model) persist() tea.Cmd {
	if err := m.store.Save(m.sess); err != nil {
		return m.setStatus("could not save session: "+err.Error(), true)
	}
	return nil
}

// rememberModel records the model choice on the profile so the next launch
// starts where the user left off.
func (m *Model) rememberModel() {
	prof, ok := m.profiles.Entries[m.profileName]
	if !ok || prof.Model == m.sess.Model {
		return
	}
	prof.Model = m.sess.Model
	_, _ = config.UpdateProfiles(func(p *config.Profiles) error {
		if entry, ok := p.Entries[m.profileName]; ok {
			entry.Model = m.sess.Model
		}
		return nil
	})
}
