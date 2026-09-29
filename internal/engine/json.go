package engine

// FormatVersion versions every JSON document zakwas writes: plans, apply
// events and history. Adding fields keeps it; renaming or removing bumps it.
const FormatVersion = "1"

// JSONChange is a Change for machines: actions are words, not symbols.
type JSONChange struct {
	Module      string `json:"module,omitempty"`
	Action      string `json:"action"`
	Target      string `json:"target"`
	Detail      string `json:"detail,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Destructive bool   `json:"destructive"`
	Step        bool   `json:"step"`
	Diff        string `json:"diff,omitempty"`
}

type JSONModule struct {
	Name    string       `json:"name"`
	Status  string       `json:"status"`
	Error   string       `json:"error,omitempty"`
	Changes []JSONChange `json:"changes"`
}

type JSONSummary struct {
	Create      int `json:"create"`
	Update      int `json:"update"`
	Delete      int `json:"delete"`
	Run         int `json:"run"`
	Destructive int `json:"destructive"`
	Steps       int `json:"steps"`
}

// Module statuses.
const (
	StatusUpToDate = "up_to_date"
	StatusChanges  = "changes"
	StatusError    = "error"
)

var actionNames = map[Action]string{Create: "create", Update: "update", Remove: "delete", Run: "run"}

// JSON converts a change; diffs are included only when asked, since file
// contents can be large.
func (c Change) JSON(withDiff bool) JSONChange {
	j := JSONChange{
		Action: actionNames[c.Action], Target: c.Target, Detail: c.Detail,
		From: c.From, To: c.To, Destructive: c.Destructive, Step: c.Step(),
	}
	if withDiff {
		j.Diff = c.Diff
	}
	return j
}

// JSONModules converts every module plan, in order.
func (p Plan) JSONModules(withDiff bool) []JSONModule {
	out := make([]JSONModule, 0, len(p))
	for _, mp := range p {
		m := JSONModule{Name: mp.Module, Status: StatusUpToDate, Changes: []JSONChange{}}
		switch {
		case mp.Err != nil:
			m.Status, m.Error = StatusError, mp.Err.Error()
		case len(mp.Changes) > 0:
			m.Status = StatusChanges
		}
		for _, c := range mp.Changes {
			m.Changes = append(m.Changes, c.JSON(withDiff))
		}
		out = append(out, m)
	}
	return out
}

func (p Plan) JSONSummary() JSONSummary {
	n := p.Counts()
	return JSONSummary{
		Create: n.Create, Update: n.Update, Delete: n.Remove, Run: n.Run,
		Destructive: n.Destructive, Steps: p.Steps(),
	}
}
