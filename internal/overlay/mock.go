package overlay

import "sync"

// Mock is an Overlay implementation designed for testing that records all interactions
// and allows injecting errors for testing error paths.
type Mock struct {
	mu        sync.Mutex
	calls     []Visual
	levels    []float64
	texts     []string
	lastLevel float64
	lastText  string

	ShowErr  error
	LevelErr error
	TextErr  error
	CloseErr error
}

func (m *Mock) Show(v Visual) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ShowErr != nil {
		return m.ShowErr
	}
	m.calls = append(m.calls, v)
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

func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.levels = nil
	m.texts = nil
	m.lastLevel = 0
	m.lastText = ""
	m.ShowErr = nil
	m.LevelErr = nil
	m.TextErr = nil
	m.CloseErr = nil
}
