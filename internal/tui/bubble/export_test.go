package bubble

// Busy reports whether the state has a run or a message on its way, for
// the tests' driver: the screen cannot tell, since "λ Starting" between a
// run's end and the session's Idle carries no "esc to interrupt".
func (m Model) Busy() bool { return m.st.Busy }

// Draft is the composer's text.
func (m Model) Draft() string { return m.composer.Value() }

// Attached lists the labels of the draft's images.
func (m Model) Attached() []string {
	labels := make([]string, 0, len(m.st.Attached))
	for _, img := range m.st.Attached {
		labels = append(labels, img.Label)
	}

	return labels
}

// Prompts is how many prompts ↑ can recall.
func (m Model) Prompts() int { return m.st.History.Len() }

// ComposerRows is how tall the composer grows on a terminal h rows high.
func ComposerRows(h int) int { return composerRows(h) }

// EdgeScrolling reports whether a drag's edge scroll waits for its tick.
func (m Model) EdgeScrolling() bool { return m.edgeTicking }

// Workspace is the open session's workspace.
func (m Model) Workspace() string { return m.st.Settings.Workspace }

// ToastText is the toast's text, "" without one.
func (m Model) ToastText() string {
	if m.st.Toast == nil {
		return ""
	}

	return m.st.Toast.Text
}
