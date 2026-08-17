package session

import (
	"encoding/json"
	"testing"

	"github.com/justin06lee/alpaca/internal/client"
)

func user(text string) client.Message {
	return client.Message{Role: client.RoleUser, Content: text}
}

func reply(text string) client.Message {
	return client.Message{Role: client.RoleAssistant, Content: text}
}

func contents(msgs []client.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func wantMessages(t *testing.T, s *Session, want ...string) {
	t.Helper()
	got := contents(s.Messages)
	if len(got) != len(want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("messages = %v, want %v", got, want)
		}
	}
}

// Editing a prompt keeps the old continuation as a sibling branch, and the
// variants can be walked in both directions.
func TestRebaseBranchesInsteadOfOverwriting(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Append(reply("a1"))
	s.Append(user("u2"))
	s.Append(reply("a2"))

	if !s.Rebase(2) {
		t.Fatal("Rebase(2) refused")
	}
	wantMessages(t, s, "u1", "a1")

	s.Append(user("u2-edited"))
	s.Append(reply("a2-edited"))
	wantMessages(t, s, "u1", "a1", "u2-edited", "a2-edited")

	if k, n := s.Variants(2); k != 2 || n != 2 {
		t.Fatalf("Variants(2) = %d/%d, want 2/2", k, n)
	}
	// The unbranched prefix stays plain.
	if k, n := s.Variants(0); k != 1 || n != 1 {
		t.Fatalf("Variants(0) = %d/%d, want 1/1", k, n)
	}

	if !s.SwitchVariant(2, -1) {
		t.Fatal("SwitchVariant(2, -1) refused")
	}
	wantMessages(t, s, "u1", "a1", "u2", "a2")
	if k, n := s.Variants(2); k != 1 || n != 2 {
		t.Fatalf("after switch, Variants(2) = %d/%d, want 1/2", k, n)
	}

	// Wrapping: +1 from the first variant lands back on the edit.
	if !s.SwitchVariant(2, 1) {
		t.Fatal("SwitchVariant(2, 1) refused")
	}
	wantMessages(t, s, "u1", "a1", "u2-edited", "a2-edited")
}

// Editing the very first prompt forks at the root.
func TestRebaseAtRootForksTheFirstMessage(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("first"))
	s.Append(reply("hello"))

	s.Rebase(0)
	wantMessages(t, s)

	s.Append(user("first-edited"))
	s.Append(reply("hi again"))

	if k, n := s.Variants(0); k != 2 || n != 2 {
		t.Fatalf("Variants(0) = %d/%d, want 2/2", k, n)
	}
	s.SwitchVariant(0, 1)
	wantMessages(t, s, "first", "hello")
}

// Switching back to a branch resumes the sub-branch that was active there,
// not an arbitrary leaf.
func TestSwitchRemembersNestedSelections(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Append(reply("a1"))
	s.Append(user("u2"))
	s.Append(reply("a2"))

	// Fork at u2, then fork again deeper inside the new branch.
	s.Rebase(2)
	s.Append(user("u2b"))
	s.Append(reply("a2b"))
	s.Append(user("u3"))
	s.Append(reply("a3"))
	s.Rebase(4)
	s.Append(user("u3-edited"))
	s.Append(reply("a3-edited"))

	// Leave for the original branch and come back: the nested edit is still
	// the active continuation.
	s.SwitchVariant(2, 1)
	wantMessages(t, s, "u1", "a1", "u2", "a2")
	s.SwitchVariant(2, 1)
	wantMessages(t, s, "u1", "a1", "u2b", "a2b", "u3-edited", "a3-edited")
}

// A session recorded before branching existed gets a tree backfilled from its
// flat history the first time a tree operation touches it.
func TestPreTreeSessionsBackfill(t *testing.T) {
	s := New("m", "srv")
	s.Messages = []client.Message{user("old1"), reply("old2"), user("old3")}

	if !s.Rebase(2) {
		t.Fatal("Rebase on a pre-tree session refused")
	}
	wantMessages(t, s, "old1", "old2")
	s.Append(user("old3-edited"))
	if k, n := s.Variants(2); k != 2 || n != 2 {
		t.Fatalf("Variants(2) = %d/%d, want 2/2", k, n)
	}
	s.SwitchVariant(2, 1)
	wantMessages(t, s, "old1", "old2", "old3")
}

// Retry replaces the reply rather than branching it: the dropped reply leaves
// the tree, so regeneration never accumulates phantom variants.
func TestDropAfterLastUserPrunesTheTree(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Append(reply("a1"))

	if _, ok := s.DropAfterLastUser(); !ok {
		t.Fatal("DropAfterLastUser found nothing")
	}
	s.Append(reply("a1-regenerated"))

	if k, n := s.Variants(1); k != 1 || n != 1 {
		t.Fatalf("regenerated reply has variants %d/%d, want 1/1", k, n)
	}
	wantMessages(t, s, "u1", "a1-regenerated")
}

// Branches survive the trip through the session file.
func TestTreeSurvivesJSONRoundtrip(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Append(reply("a1"))
	s.Rebase(0)
	s.Append(user("u1-edited"))
	s.Append(reply("a1-edited"))

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if k, n := loaded.Variants(0); k != 2 || n != 2 {
		t.Fatalf("loaded Variants(0) = %d/%d, want 2/2", k, n)
	}
	loaded.SwitchVariant(0, 1)
	wantMessages(t, &loaded, "u1", "a1")
}

// Clear drops the branches with the messages.
func TestClearEmptiesTheTree(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Rebase(0)
	s.Append(user("u1b"))

	s.Clear()
	if len(s.Messages) != 0 || len(s.Tree) != 0 || len(s.Roots) != 0 || s.Head != "" {
		t.Fatalf("Clear left state behind: %+v", s)
	}
}

// ActivateNode brings an off-path node's branch back to life and reports where
// the node landed in the flat history.
func TestActivateNodeSwitchesThePath(t *testing.T) {
	s := New("m", "srv")
	s.Append(user("u1"))
	s.Append(reply("a1"))
	s.Rebase(0)
	s.Append(user("u1-edited"))

	// Find the original reply, now off-path.
	var target string
	s.Walk(func(n *Node, depth int, onPath bool) {
		if n.Msg.Content == "a1" {
			target = n.ID
		}
	})
	if target == "" {
		t.Fatal("original reply vanished from the tree")
	}

	idx := s.ActivateNode(target)
	if idx != 1 {
		t.Fatalf("ActivateNode index = %d, want 1", idx)
	}
	wantMessages(t, s, "u1", "a1")
}
