package session

import "testing"

func TestMoreUrgentRanksNeedsInputDoneWorkingIdle(t *testing.T) {
	order := []AgentState{AgentNone, AgentIdle, AgentWorking, AgentDone, AgentNeedsInput}
	for i, lower := range order {
		for _, higher := range order[i+1:] {
			if got := MoreUrgent(lower, higher); got != higher {
				t.Errorf("MoreUrgent(%q, %q) = %q, want %q", lower, higher, got, higher)
			}
			if got := MoreUrgent(higher, lower); got != higher {
				t.Errorf("MoreUrgent(%q, %q) = %q, want %q", higher, lower, got, higher)
			}
		}
	}
}

func TestMoreUrgentNeverPublishesAnUnknownValue(t *testing.T) {
	if got := MoreUrgent(AgentNone, "bogus"); got != AgentNone {
		t.Errorf("got %q, want none", got)
	}
	if got := MoreUrgent(AgentIdle, "bogus"); got != AgentIdle {
		t.Errorf("got %q, want idle", got)
	}
}

func openPR() *PRStatus { return &PRStatus{Number: 1, State: "OPEN"} }

func TestAgentStateDecidesState(t *testing.T) {
	cases := []struct {
		name string
		s    Session
		want SessionState
	}{
		{"needs input outranks a failing PR", Session{Agent: AgentNeedsInput, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "fail"}}, Attention},
		{"needs input outranks merged", Session{Agent: AgentNeedsInput, PR: &PRStatus{Number: 1, State: "MERGED"}}, Attention},
		{"an agent silences the bell", Session{Agent: AgentWorking, HasBell: true}, Idle},
		{"a done agent's bell is Waiting, not Attention", Session{Agent: AgentDone, HasBell: true}, Waiting},
		{"the bell still decides without an agent", Session{HasBell: true, PR: openPR()}, Attention},
		{"done with no PR is Waiting", Session{Agent: AgentDone}, Waiting},
		{"done with an open PR is Waiting", Session{Agent: AgentDone, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"}}, Waiting},
		{"merged outranks done", Session{Agent: AgentDone, PR: &PRStatus{Number: 1, State: "MERGED"}}, Done},
		{"blocked outranks done", Session{Agent: AgentDone, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "fail"}}, Blocked},
		{"unresolved outranks done", Session{Agent: AgentDone, PR: &PRStatus{Number: 1, State: "OPEN", UnresolvedComments: 2}}, Unresolved},
		{"working leaves PR state alone", Session{Agent: AgentWorking, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "pending"}}, Pending},
		{"idle leaves PR state alone", Session{Agent: AgentIdle, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"}}, Mergeable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.s.State(); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestWaitingSortsBetweenUnresolvedAndMergeable(t *testing.T) {
	all := AllStates()
	idx := map[SessionState]int{}
	for i, s := range all {
		idx[s] = i
	}
	if idx[Unresolved] >= idx[Waiting] || idx[Waiting] >= idx[Mergeable] {
		t.Errorf("AllStates order %v puts waiting out of place", all)
	}
	if Unresolved >= Waiting || Waiting >= Mergeable {
		t.Errorf("iota order puts waiting out of place")
	}
	if Waiting.String() != "waiting" {
		t.Errorf("got %q, want waiting", Waiting.String())
	}
}
