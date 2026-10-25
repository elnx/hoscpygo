package layout

import "testing"

const sample = `{
  "attributes": {"type": "Root", "bounds": "[0,0][1260,2720]"},
  "children": [
    {"attributes": {"type": "Button", "text": "OK", "bounds": "[10,20][110,120]"}, "children": []},
    {"attributes": {"type": "Stack", "origBounds": "[0,0][630,1360]", "bounds": "[0,0][100,100]"},
     "children": [{"attributes": {"type": "Text", "text": "hello", "bounds": "[5,6][7,8]"}}]}
  ]
}`

func TestParseBounds(t *testing.T) {
	n, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	w, h := n.ScreenSize()
	if w != 1260 || h != 2720 {
		t.Fatalf("size=%dx%d", w, h)
	}
	hits := n.Find("hello", 10)
	if len(hits) != 1 {
		t.Fatalf("hits=%d", len(hits))
	}
	l, tp, r, b, ok := hits[0].Bounds()
	if !ok || l != 5 || tp != 6 || r != 7 || b != 8 {
		t.Fatalf("bounds=%v %v %v %v %v", l, tp, r, b, ok)
	}
}

func TestOrigBoundsPrecedence(t *testing.T) {
	n, _ := Parse([]byte(sample))
	child := n.Children[1]
	l, tp, r, b, ok := child.Bounds()
	if !ok || l != 0 || tp != 0 || r != 630 || b != 1360 {
		t.Fatalf("orig bounds not used: %v %v %v %v", l, tp, r, b)
	}
}
