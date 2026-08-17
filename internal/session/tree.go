package session

import (
	"fmt"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
)

// The conversation tree.
//
// Messages stays the flat active path — everything that renders, persists, or
// goes on the wire keeps reading it exactly as before. The tree remembers what
// the flat list forgets: when a prompt is edited and resent, the old
// continuation survives as a sibling branch instead of being overwritten, and
// the arrow keys can walk between the variants.
//
// A node's content never changes after creation — editing creates a sibling,
// never a rewrite — which is what lets summaries (and anything else derived
// from content) be cached on the node forever.

// Node is one message in the conversation tree.
type Node struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	// Msg is the message exactly as it appears in Messages when this node is
	// on the active path.
	Msg client.Message `json:"msg"`
	// Children are ordered by creation, so variant numbering is stable.
	Children []string `json:"children,omitempty"`
	// Sel is the child last active under this node, so switching back to a
	// branch resumes where it was left rather than at an arbitrary leaf.
	Sel string `json:"sel,omitempty"`
	// Sum is the graphing model's one-line summary. Cached without an
	// invalidation scheme on purpose: node content is immutable.
	Sum string `json:"sum,omitempty"`
}

// newNodeID mints a tree node ID, falling back to a timestamp when the random
// source fails — uniqueness within one session is all that is needed.
func (s *Session) newNodeID() string {
	if id, err := config.NewID(); err == nil {
		return id
	}
	return fmt.Sprintf("n%d-%d", time.Now().UnixNano(), len(s.Tree))
}

// ensureTree backfills the tree for a session recorded before branching
// existed: the flat history becomes a single chain ending at Head.
func (s *Session) ensureTree() {
	if s.Tree == nil {
		s.Tree = map[string]*Node{}
	}
	if len(s.Tree) > 0 || len(s.Messages) == 0 {
		return
	}
	parent := ""
	for _, msg := range s.Messages {
		id := s.newNodeID()
		s.Tree[id] = &Node{ID: id, Parent: parent, Msg: msg}
		if parent == "" {
			s.Roots = append(s.Roots, id)
			s.RootSel = id
		} else {
			p := s.Tree[parent]
			p.Children = append(p.Children, id)
			p.Sel = id
		}
		parent = id
	}
	s.Head = parent
}

// path lists the node IDs from the first message down to Head — the tree's
// spelling of Messages, index for index.
func (s *Session) path() []string {
	var rev []string
	for id := s.Head; id != ""; {
		node, ok := s.Tree[id]
		if !ok {
			break
		}
		rev = append(rev, id)
		id = node.Parent
	}
	// Reverse in place: the walk above went leaf-to-root.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// siblings is the variant set a node belongs to: its parent's children, or the
// root set for a first message.
func (s *Session) siblings(node *Node) []string {
	if node.Parent == "" {
		return s.Roots
	}
	if p, ok := s.Tree[node.Parent]; ok {
		return p.Children
	}
	return nil
}

// descend follows the remembered selections from a node down to a leaf,
// falling back to the newest child where no selection was recorded.
func (s *Session) descend(id string) string {
	for {
		node, ok := s.Tree[id]
		if !ok || len(node.Children) == 0 {
			return id
		}
		next := node.Sel
		if _, ok := s.Tree[next]; !ok || !contains(node.Children, next) {
			next = node.Children[len(node.Children)-1]
		}
		id = next
	}
}

// syncMessages rewrites the flat history from the active path.
func (s *Session) syncMessages() {
	ids := s.path()
	s.Messages = s.Messages[:0]
	for _, id := range ids {
		s.Messages = append(s.Messages, s.Tree[id].Msg)
	}
	s.Updated = time.Now()
}

// appendNode grows the tree under the current head and moves the head there.
func (s *Session) appendNode(msg client.Message) {
	s.ensureTree()
	id := s.newNodeID()
	node := &Node{ID: id, Parent: s.Head, Msg: msg}
	s.Tree[id] = node
	if s.Head == "" {
		s.Roots = append(s.Roots, id)
		s.RootSel = id
	} else {
		p := s.Tree[s.Head]
		p.Children = append(p.Children, id)
		p.Sel = id
	}
	s.Head = id
}

// Rebase prepares an edit of message i: the head moves above it and the flat
// history is cut back, while the old continuation stays in the tree as a
// branch. The next Append becomes message i's sibling.
func (s *Session) Rebase(i int) bool {
	s.ensureTree()
	ids := s.path()
	if i < 0 || i >= len(ids) {
		return false
	}
	s.Head = s.Tree[ids[i]].Parent
	s.syncMessages()
	return true
}

// Variants reports where message i sits among its siblings: variant k of n,
// 1-based. n is 1 for a message that was never branched.
func (s *Session) Variants(i int) (k, n int) {
	if len(s.Tree) == 0 {
		return 1, 1
	}
	ids := s.path()
	if i < 0 || i >= len(ids) {
		return 1, 1
	}
	node := s.Tree[ids[i]]
	sibs := s.siblings(node)
	for idx, id := range sibs {
		if id == node.ID {
			return idx + 1, len(sibs)
		}
	}
	return 1, 1
}

// SwitchVariant replaces the conversation from message i onward with the
// sibling delta steps away, wrapping at the ends. It reports whether anything
// changed.
func (s *Session) SwitchVariant(i, delta int) bool {
	s.ensureTree()
	ids := s.path()
	if i < 0 || i >= len(ids) {
		return false
	}
	node := s.Tree[ids[i]]
	sibs := s.siblings(node)
	if len(sibs) < 2 {
		return false
	}
	at := 0
	for idx, id := range sibs {
		if id == node.ID {
			at = idx
		}
	}
	next := sibs[((at+delta)%len(sibs)+len(sibs))%len(sibs)]
	if next == node.ID {
		return false
	}
	s.activate(next)
	return true
}

// ActivateNode makes the path through a node the live conversation and
// reports the node's index in Messages, or -1 if the node is unknown.
func (s *Session) ActivateNode(id string) int {
	s.ensureTree()
	if _, ok := s.Tree[id]; !ok {
		return -1
	}
	s.activate(id)
	for i, pid := range s.path() {
		if pid == id {
			return i
		}
	}
	return -1
}

// activate records the selection at every fork above the node, then follows
// the remembered selections below it to a leaf.
func (s *Session) activate(id string) {
	for cur := id; cur != ""; {
		node := s.Tree[cur]
		if node == nil {
			break
		}
		if node.Parent == "" {
			s.RootSel = cur
		} else if p, ok := s.Tree[node.Parent]; ok {
			p.Sel = cur
		}
		cur = node.Parent
	}
	s.Head = s.descend(id)
	s.syncMessages()
}

// removeSubtree deletes a node and everything under it — used when a reply is
// regenerated in place, which replaces rather than branches.
func (s *Session) removeSubtree(id string) {
	node, ok := s.Tree[id]
	if !ok {
		return
	}
	for _, child := range append([]string(nil), node.Children...) {
		s.removeSubtree(child)
	}
	if node.Parent == "" {
		s.Roots = remove(s.Roots, id)
		if s.RootSel == id {
			s.RootSel = ""
			if len(s.Roots) > 0 {
				s.RootSel = s.Roots[len(s.Roots)-1]
			}
		}
	} else if p, ok := s.Tree[node.Parent]; ok {
		p.Children = remove(p.Children, id)
		if p.Sel == id {
			p.Sel = ""
			if len(p.Children) > 0 {
				p.Sel = p.Children[len(p.Children)-1]
			}
		}
	}
	delete(s.Tree, id)
}

// Walk visits the whole tree depth-first in creation order, telling the
// visitor each node's depth and whether it lies on the active path.
func (s *Session) Walk(visit func(node *Node, depth int, onPath bool)) {
	s.ensureTree()
	onPath := map[string]bool{}
	for _, id := range s.path() {
		onPath[id] = true
	}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		node, ok := s.Tree[id]
		if !ok {
			return
		}
		visit(node, depth, onPath[id])
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	for _, root := range s.Roots {
		walk(root, 0)
	}
}

// NodeIndex is the message index a node occupies on the active path, or -1
// when it lives on an inactive branch.
func (s *Session) NodeIndex(id string) int {
	for i, pid := range s.path() {
		if pid == id {
			return i
		}
	}
	return -1
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func remove(list []string, drop string) []string {
	out := list[:0]
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
