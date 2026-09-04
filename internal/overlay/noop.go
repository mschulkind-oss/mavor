package overlay

import "sync"

// Noop is an Overlay that records its calls without touching Wayland. It's
// what unit tests of the daemon use, and it's also the safe fallback when
// the daemon is built without GTK. Show, SetLevel, and SetText are goroutine-safe
// so daemon tests (which call them from background listeners) don't trip the race detector.
type Noop struct {
	mu        sync.Mutex
	calls     []Visual
	levels    []float64
	texts     []string
	lastLevel float64
	lastText  string
}

func (n *Noop) Show(v Visual) error {
	n.mu.Lock()
	n.calls = append(n.calls, v)
	n.mu.Unlock()
	return nil
}

func (n *Noop) SetLevel(level float64) error {
	n.mu.Lock()
	n.levels = append(n.levels, level)
	n.lastLevel = level
	n.mu.Unlock()
	return nil
}

func (n *Noop) SetText(text string) error {
	n.mu.Lock()
	n.texts = append(n.texts, text)
	n.lastText = text
	n.mu.Unlock()
	return nil
}

func (n *Noop) Close() error { return nil }

// Calls returns a snapshot of the visual states Show has been called with.
func (n *Noop) Calls() []Visual {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Visual, len(n.calls))
	copy(out, n.calls)
	return out
}

// Levels returns a snapshot of all level updates passed to SetLevel.
func (n *Noop) Levels() []float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]float64, len(n.levels))
	copy(out, n.levels)
	return out
}

// LastLevel returns the most recent level value passed to SetLevel.
func (n *Noop) LastLevel() float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastLevel
}

// Texts returns a snapshot of all preview text updates passed to SetText.
func (n *Noop) Texts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.texts))
	copy(out, n.texts)
	return out
}

// LastText returns the most recent text value passed to SetText.
func (n *Noop) LastText() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastText
}
