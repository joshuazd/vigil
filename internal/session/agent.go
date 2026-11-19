package session

// AgentState is what a coding agent in a session's pane last reported through
// the @vigil_agent pane option. The empty value means no agent reported.
type AgentState string

const (
	AgentNone       AgentState = ""
	AgentIdle       AgentState = "idle"
	AgentWorking    AgentState = "working"
	AgentDone       AgentState = "done"
	AgentNeedsInput AgentState = "needs_input"
)

func (a AgentState) urgency() int {
	switch a {
	case AgentNeedsInput:
		return 4
	case AgentDone:
		return 3
	case AgentWorking:
		return 2
	case AgentIdle:
		return 1
	default:
		return 0
	}
}

// MoreUrgent returns whichever state needs the user sooner. An unrecognised
// value ranks with AgentNone, so it can never displace a real report.
func MoreUrgent(a, b AgentState) AgentState {
	if b.urgency() > a.urgency() {
		return b
	}
	return a
}
