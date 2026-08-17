package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The tree flattens with chains at one indent and forks pushed sideways.
func TestGraphFlattensForksSideways(t *testing.T) {
	m := branchedModel(t)
	m.openGraph()

	if !m.graphOpen {
		t.Fatal("openGraph did not open the graph")
	}
	// u1, a1, then two variants of u2 each with a reply: six rows.
	if len(m.graphRows) != 6 {
		t.Fatalf("graph rows = %d, want 6", len(m.graphRows))
	}

	// The unbranched chain sits at the left edge.
	if m.graphRows[0].prefix != "" || m.graphRows[1].prefix != "" {
		t.Errorf("chain rows are indented: %q, %q", m.graphRows[0].prefix, m.graphRows[1].prefix)
	}
	// The two variants carry connectors and the fork star.
	var forks []graphRow
	for _, row := range m.graphRows {
		if row.fork {
			forks = append(forks, row)
		}
	}
	if len(forks) != 2 {
		t.Fatalf("fork rows = %d, want the 2 variants", len(forks))
	}
	if !strings.Contains(forks[0].prefix, "├─") || !strings.Contains(forks[1].prefix, "└─") {
		t.Errorf("variant connectors wrong: %q, %q", forks[0].prefix, forks[1].prefix)
	}

	// The live branch is marked; the abandoned one is not.
	onPath := 0
	for _, row := range m.graphRows {
		if row.onPath {
			onPath++
		}
	}
	if onPath != 4 {
		t.Errorf("onPath rows = %d, want the 4 live messages", onPath)
	}
}

// The cursor opens on the newest live message, and enter on an off-path row
// swaps that branch in and lands the transcript on it.
func TestGraphJumpActivatesTheChosenBranch(t *testing.T) {
	m := branchedModel(t)
	m.openGraph()

	if got := m.graphRows[m.graphCur].id; got != m.sess.Head {
		t.Errorf("cursor opened on %q, want the head", got)
	}

	// Find the abandoned branch's reply.
	target := -1
	for i, row := range m.graphRows {
		if !row.onPath && strings.Contains(row.label, "original answer") {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("abandoned branch missing from the graph")
	}

	m.graphCur = target
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if m.graphOpen {
		t.Error("jump left the graph open")
	}
	if got := m.sess.Messages[3].Content; got != "original answer" {
		t.Errorf("jump did not activate the branch: message 3 = %q", got)
	}
}

// Node summaries label the graph once they exist; excerpts stand in before.
func TestGraphPrefersSummaries(t *testing.T) {
	m := branchedModel(t)
	m.sess.EnsureTree()
	for _, node := range m.sess.Tree {
		if node.Msg.Content == "first question" {
			node.Sum = "asks the opening question"
		}
	}
	m.openGraph()

	var labels []string
	for _, row := range m.graphRows {
		labels = append(labels, row.label)
	}
	joined := strings.Join(labels, "\n")
	if !strings.Contains(joined, "asks the opening question") {
		t.Errorf("summary not used as a label:\n%s", joined)
	}
	if !strings.Contains(joined, "edited prompt") {
		t.Errorf("excerpt stand-in missing:\n%s", joined)
	}

	view := stripANSI(m.graphView())
	if !strings.Contains(view, "asks the opening question") {
		t.Errorf("graph view missing the summary:\n%s", view)
	}
	if !strings.Contains(view, "conversation graph") {
		t.Errorf("graph view missing its header:\n%s", view)
	}
}

// /graph on an empty chat degrades to a status line, not a blank screen.
func TestGraphNeedsAConversation(t *testing.T) {
	m := readyModel(t)
	m.runSlash("/graph")
	if m.graphOpen {
		t.Error("graph opened over an empty conversation")
	}
	if m.status == "" {
		t.Error("no status explaining why nothing happened")
	}
}

func TestExcerptCollapsesWhitespace(t *testing.T) {
	got := excerpt("  a\n\n  question\tover   lines  ")
	if got != "a question over lines" {
		t.Errorf("excerpt = %q", got)
	}
	long := excerpt(strings.Repeat("word ", 40))
	if !strings.HasSuffix(long, "…") {
		t.Errorf("long excerpt not truncated: %q", long)
	}
}

func TestFirstSentenceLineTidiesModelOutput(t *testing.T) {
	got := firstSentenceLine("\n\n\"Asks about baking bread.\"\nExtra commentary.")
	if got != "Asks about baking bread." {
		t.Errorf("firstSentenceLine = %q", got)
	}
}
