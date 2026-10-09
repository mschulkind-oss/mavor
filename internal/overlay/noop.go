package overlay

import "sync"

// Noop is an Overlay that records its calls without touching Wayland. It's
// what unit tests of the daemon use, and it's also the safe fallback when the
// real one cannot be built — no compositor, or one without layer-shell —
// since dictation works fine without a visual indicator and refusing to start
// over a missing HUD would be the worse trade. Show, SetLevel, SetText and
// SetNotice are goroutine-safe so daemon tests (which call them from background
// listeners) don't trip the race detector.
type Noop struct {
	mu         sync.Mutex
	calls      []Visual
	stacks     [][]Visual
	levels     []float64
	texts      []string
	notices    []string
	lastLevel  float64
	lastText   string
	lastNotice string
}

// Show is the single-pill form of ShowStack, so a caller that only ever shows
// one pill records exactly as it always did.
func (n *Noop) Show(v Visual) error { return n.ShowStack([]Visual{v}) }

func (n *Noop) ShowStack(pills []Visual) error {
	n.mu.Lock()
	n.stacks = append(n.stacks, append([]Visual(nil), pills...))
	if len(pills) > 0 {
		n.calls = append(n.calls, pills[0])
	}
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

func (n *Noop) SetNotice(text string) error {
	n.mu.Lock()
	n.notices = append(n.notices, text)
	n.lastNotice = text
	n.mu.Unlock()
	return nil
}

func (n *Noop) Close() error { return nil }

// Calls returns a snapshot of the primary visual each Show/ShowStack call
// carried.
func (n *Noop) Calls() []Visual {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Visual, len(n.calls))
	copy(out, n.calls)
	return out
}

// Stacks returns a snapshot of every pill stack passed to ShowStack, in call
// order. A Show call appears as a one-element stack.
func (n *Noop) Stacks() [][]Visual {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([][]Visual, len(n.stacks))
	for i, s := range n.stacks {
		out[i] = append([]Visual(nil), s...)
	}
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

// Notices returns a snapshot of all notice updates passed to SetNotice.
func (n *Noop) Notices() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.notices))
	copy(out, n.notices)
	return out
}

// LastNotice returns the most recent notice value passed to SetNotice.
func (n *Noop) LastNotice() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastNotice
}
