package agent

// RoleClaude is the pane role (the @lt_role pane option) of a Claude pane
// that lyna-tmux launched. It matches the tmux adapter's constant; the domain
// keeps its own copy because it imports no adapters.
const RoleClaude = "claude"

// MaxAncestry bounds the parent walk from an agent process to a pane process.
// Real process trees between a tmux pane and Claude are a handful of levels
// deep (shell, optional wrapper, Claude); the bound keeps a corrupted or
// hostile ancestry table from turning the join into an unbounded loop.
const MaxAncestry = 64

// Pane is the pane information the join needs, independent of the tmux
// adapter that lists it.
type Pane struct {
	ID          string
	Session     string
	WindowIndex int
	WindowName  string
	TTY         string
	// PID is the pane's root process (#{pane_pid}), usually the shell.
	PID int
	// Managed is true when the pane's session was created by lyna-tmux.
	Managed bool
	// Role is the pane's @lt_role option ("" when unset).
	Role string
}

// Parents returns the parent process id of pid, and false when pid is not a
// known process.
type Parents func(pid int) (ppid int, ok bool)

// ParentMap adapts a pid to parent pid table to Parents.
func ParentMap(m map[int]int) Parents {
	return func(pid int) (int, bool) {
		ppid, ok := m[pid]
		return ppid, ok
	}
}

// Join builds the agent view from `claude agents --json` records, the panes of
// the lyna-tmux server and the process ancestry.
//
// A record belongs to the pane whose root process is the record's process or
// one of its ancestors. Background records are never attributed to a pane:
// their process is owned by the background runner even when that runner was
// started from a pane, and jumping there would show another session. The
// result is sorted with Sort.
func Join(records []Record, panes []Pane, parents Parents) []Agent {
	byPID := make(map[int]Pane, len(panes))
	for _, p := range panes {
		if p.PID <= 0 {
			continue
		}
		if _, dup := byPID[p.PID]; !dup {
			byPID[p.PID] = p
		}
	}
	agents := make([]Agent, 0, len(records))
	for _, r := range records {
		a := Agent{Record: r, Status: r.EffectiveStatus(), Source: SourceExternal}
		if r.Background() {
			a.Source = SourceBackground
		} else if pane, found := paneOf(r.PID, byPID, parents); found {
			a.Location = &Location{
				Session:     pane.Session,
				WindowIndex: pane.WindowIndex,
				WindowName:  pane.WindowName,
				PaneID:      pane.ID,
				TTY:         pane.TTY,
			}
			a.Source = SourcePane
			if pane.Managed && pane.Role == RoleClaude {
				a.Source = SourceManaged
			}
		}
		agents = append(agents, a)
	}
	Sort(agents)
	return agents
}

// paneOf walks from an agent process towards the root and returns the
// first pane whose root process is on that path. The walk stops at unknown
// processes, self-parented processes (pid 0 and launchd/init report
// themselves or 0), revisited pids and after MaxAncestry steps.
func paneOf(pid int, byPID map[int]Pane, parents Parents) (Pane, bool) {
	if pid <= 0 {
		return Pane{}, false
	}
	seen := make(map[int]struct{}, 8)
	for range MaxAncestry {
		if p, ok := byPID[pid]; ok {
			return p, true
		}
		if parents == nil {
			return Pane{}, false
		}
		seen[pid] = struct{}{}
		ppid, ok := parents(pid)
		if !ok || ppid <= 0 {
			return Pane{}, false
		}
		if _, loop := seen[ppid]; loop {
			return Pane{}, false
		}
		pid = ppid
	}
	return Pane{}, false
}
