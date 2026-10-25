// Package layout parses HarmonyOS uitest view hierarchy JSON.
package layout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Node is one view node. Attributes are kept as strings because the device
// emits everything as strings.
type Node struct {
	Attributes map[string]string `json:"attributes"`
	Children   []*Node           `json:"children"`
}

var boundsRe = regexp.MustCompile(`\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]`)

func Parse(raw []byte) (*Node, error) {
	var n Node
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Bounds returns the node rectangle [left, top, right, bottom].
func (n *Node) Bounds() (int, int, int, int, bool) {
	if n == nil {
		return 0, 0, 0, 0, false
	}
	v := n.Attributes["origBounds"]
	if v == "" {
		v = n.Attributes["bounds"]
	}
	m := boundsRe.FindStringSubmatch(v)
	if len(m) != 5 {
		return 0, 0, 0, 0, false
	}
	var nums [4]int
	for i := range nums {
		nums[i], _ = strconv.Atoi(m[i+1])
	}
	return nums[0], nums[1], nums[2], nums[3], true
}

// ScreenSize returns the display bounds. The root node is authoritative; if it
// has no bounds, the largest right/bottom edge in the tree is used.
func (n *Node) ScreenSize() (int, int) {
	if l, t, r, b, ok := n.Bounds(); ok && r > l && b > t {
		return r, b
	}
	w, h := 0, 0
	var walk func(*Node)
	walk = func(x *Node) {
		if x == nil {
			return
		}
		if _, _, r, b, ok := x.Bounds(); ok {
			if r > w {
				w = r
			}
			if b > h {
				h = b
			}
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return w, h
}

// Find returns nodes whose type/text/description contains the query.
func (n *Node) Find(query string, limit int) []*Node {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var out []*Node
	var walk func(*Node)
	walk = func(x *Node) {
		if x == nil || len(out) >= limit {
			return
		}
		hay := strings.ToLower(x.Attributes["type"] + " " + x.Attributes["text"] + " " + x.Attributes["description"] + " " + x.Attributes["id"])
		if strings.Contains(hay, query) {
			out = append(out, x)
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return out
}

func (n *Node) String() string {
	if n == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %s", n.Attributes["type"], n.Attributes["bounds"])
}
