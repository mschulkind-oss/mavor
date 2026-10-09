package overlay

import "sync"

// Mock is an Overlay implementation designed for testing that records all interactions
// and allows injecting errors for testing error paths.
type Mock struct {
	mu         sync.Mutex
	calls      []Visual
	stacks     [][]Visual
	levels     []float64
	texts      []string
	notices    []string
	lastLevel  float64
	lastText   string
	lastNotice string

	ShowErr   error
	LevelErr  error
	TextErr   error
	NoticeErr error
	CloseErr  error
}

// Show is the single-pill form of ShowStack, so a caller that only ever shows
// one pill records exactly as it always did.
func (m *Mock) Show(v Visual) error { return m.ShowStack([]Visual{v}) }

func (m *Mock) ShowStack(pills []Visual) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ShowErr != nil {
		return m.ShowErr
	}
	m.stacks = append(m.stacks, append([]Visual(nil), pills...))
	if len(pills) > 0 {
		m.calls = append(m.calls, pills[0])
	}
	return nil
}

func (m *Mock) SetLevel(level float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.LevelErr != nil {
		return m.LevelErr
	}
	m.levels = append(m.levels, level)
	m.lastLevel = level
	return nil
}

func (m *Mock) SetText(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.TextErr != nil {
		return m.TextErr
	}
	m.texts = append(m.texts, text)
	m.lastText = text
	return nil
}

func (m *Mock) SetNotice(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.NoticeErr != nil {
		return m.NoticeErr
	}
	m.notices = append(m.notices, text)
	m.lastNotice = text
	return nil
}

func (m *Mock) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CloseErr
}

func (m *Mock) Calls() []Visual {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Visual, len(m.calls))
	copy(out, m.calls)
	return out
}

// Stacks returns a snapshot of every pill stack passed to ShowStack, in call
// order. A Show call appears as a one-element stack.
func (m *Mock) Stacks() [][]Visual {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]Visual, len(m.stacks))
	for i, s := range m.stacks {
		out[i] = append([]Visual(nil), s...)
	}
	return out
}

func (m *Mock) Levels() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float64, len(m.levels))
	copy(out, m.levels)
	return out
}

func (m *Mock) LastLevel() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastLevel
}

func (m *Mock) Texts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.texts))
	copy(out, m.texts)
	return out
}

func (m *Mock) LastText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastText
}

func (m *Mock) Notices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.notices))
	copy(out, m.notices)
	return out
}

func (m *Mock) LastNotice() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastNotice
}

func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.stacks = nil
	m.levels = nil
	m.texts = nil
	m.notices = nil
	m.lastLevel = 0
	m.lastText = ""
	m.lastNotice = ""
	m.ShowErr = nil
	m.LevelErr = nil
	m.TextErr = nil
	m.NoticeErr = nil
	m.CloseErr = nil
}
