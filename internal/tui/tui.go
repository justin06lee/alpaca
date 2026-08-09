// Package tui is the terminal chat interface.
package tui

import (
	"context"
	"fmt"
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
	headerHeight = 2 // title line plus a blank
	inputHeight  = 3 // textarea rows
	chromeHeight = headerHeight + inputHeight + 2
)

// statusLifetime is how long a transient message stays on the status bar.
const statusLifetime = 4 * time.Second

// Model is the Bubble Tea model for the chat interface.
type Model struct {
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

	streaming    bool
	streamBuf    string
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

// New builds the chat interface.
func New(c *client.Client, store *session.Store, profiles *config.Profiles, profileName string, sess *session.Session) *Model {
	input := textarea.New()
	input.Placeholder = "Send a message…  (enter to send, ctrl+j for a newline)"
	input.ShowLineNumbers = false
	input.CharLimit = 0 // long pasted prompts are legitimate
	input.SetHeight(inputHeight)
	input.Focus()
	// Enter is intercepted as "send", so the textarea must not also insert a
	// newline for it.
	input.KeyMap.InsertNewline.SetEnabled(false)

	spin := spinner.New()
	spin.Spinner = spinner.Dot

	return &Model{
		client:      c,
		store:       store,
		profiles:    profiles,
		profileName: profileName,
		sess:        sess,
		input:       input,
		spinner:     spin,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, loadModels(m.client))
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		return m, m.resize(msg.Width, msg.Height)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case modelsMsg:
		return m, m.handleModels(msg)

	case streamEvent:
		return m, m.handleStreamEvent(msg)

	case streamClosedMsg:
		return m, m.finishStream()

	case tickMsg:
		if !m.streaming {
			return m, nil
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

	// The markdown renderer is width-bound, so it and the whole render cache
	// have to be rebuilt whenever the terminal changes size.
	if renderer, err := newRenderer(m.contentWidth()); err == nil {
		m.renderer = renderer
	}
	m.rebuildCache()
	m.refreshViewport(true)

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
	m.viewport.SetContent(m.conversation())
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

func (m *Model) View() string {
	if !m.ready {
		return "starting…"
	}
	if m.quitting {
		return ""
	}

	if m.showHelp {
		return m.helpView()
	}
	if m.mode != pickerNone {
		return m.pick.view(m.width, m.height)
	}

	return strings.Join([]string{
		m.header(),
		"",
		m.viewport.View(),
		m.inputView(),
		m.statusBar(),
	}, "\n")
}

func (m *Model) inputView() string {
	if m.streaming {
		// Hide the composer while the model is talking: it cannot be sent
		// anyway, and showing it invites typing that goes nowhere.
		return styleMuted.Render(strings.Repeat("─", maxInt(1, m.width))) + "\n" +
			styleMuted.Render("  generating… press esc to stop")
	}
	return styleMuted.Render(strings.Repeat("─", maxInt(1, m.width))) + "\n" + m.input.View()
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
	_ = m.profiles.Save()
}

func (m *Model) helpView() string {
	rows := [][2]string{
		{"enter", "send the message"},
		{"ctrl+j", "insert a newline"},
		{"esc", "stop generating, or clear the composer"},
		{"ctrl+c", "quit"},
		{"", ""},
		{"ctrl+p", "switch model"},
		{"ctrl+n", "new chat"},
		{"ctrl+s", "browse saved chats"},
		{"ctrl+r", "regenerate the last reply"},
		{"ctrl+y", "copy the last reply"},
		{"", ""},
		{"pgup/pgdn", "scroll the transcript"},
		{"ctrl+u/ctrl+d", "scroll half a page"},
		{"?", "toggle this help"},
	}

	var b strings.Builder
	b.WriteString(stylePickerTitle.Render("alpaca — keys"))
	b.WriteString("\n\n")
	for _, row := range rows {
		if row[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + styleStatusKey.Render(fmt.Sprintf("%-14s", row[0])) + styleMuted.Render(row[1]))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(stylePickerTitle.Render("slash commands"))
	b.WriteString("\n\n")
	for _, row := range [][2]string{
		{"/model [name]", "switch model, or open the picker"},
		{"/new", "start a new chat"},
		{"/sessions", "browse saved chats"},
		{"/system <text>", "set the system prompt (empty to clear)"},
		{"/retry", "regenerate the last reply"},
		{"/copy", "copy the last reply to the clipboard"},
		{"/clear", "delete this chat's messages"},
		{"/stats", "show connection and token details"},
		{"/help", "this screen"},
		{"/quit", "exit"},
	} {
		b.WriteString("  " + styleStatusKey.Render(fmt.Sprintf("%-16s", row[0])) + styleMuted.Render(row[1]))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("press any key to return"))
	return b.String()
}
