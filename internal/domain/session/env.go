package session

// Environment variables exported into every managed Claude pane and read back
// by the hidden hook, statusline and watch commands.
const (
	// EnvManaged is "1" inside panes launched by lyna-tmux. The companion Claude
	// plugin checks it to avoid running its hooks twice.
	EnvManaged = "LYNA_TMUX_MANAGED"
	// EnvSocket is the absolute path of the lyna-tmux server socket.
	EnvSocket = "LYNA_TMUX_SOCKET"
	// EnvSession is the tmux session name of the workspace.
	EnvSession = "LYNA_TMUX_SESSION"
	// EnvSandbox is the sandbox profile the pane was launched with.
	EnvSandbox = "LYNA_TMUX_SANDBOX"
	// EnvClient carries the tmux client environment, the value of TMUX, of the
	// pane a teammate opened in. The launcher exports it and starts lyna-tmux
	// with TMUX cleared: a terminal library reads TMUX while the process is
	// starting and asks that server what colors it supports, which would be a
	// command sent to a server lyna-tmux did not create, before any code here
	// can refuse it. The agent that follows is executed with TMUX put back.
	EnvClient = "LYNA_TMUX_CLIENT"
	// EnvBell is claude.bell: "0" keeps hooks from ringing the terminal bell.
	EnvBell = "LYNA_TMUX_BELL"
	// EnvTheme names the palette (ui.theme) the status line draws with.
	EnvTheme = "LYNA_TMUX_THEME"
	// EnvIcons is the icon setting (ui.icons): auto, unicode, nerd or ascii.
	EnvIcons = "LYNA_TMUX_ICONS"
	// EnvColor is the color depth setting (ui.color): auto, truecolor, 256 or
	// 16. Any value but a depth detects it from COLORTERM and TERM.
	EnvColor = "LYNA_TMUX_COLOR"
	// EnvHome overrides every XDG root (config, state, cache) with one directory.
	EnvHome = "LYNA_TMUX_HOME"
	// EnvSocketName overrides the tmux -L socket name (tests and development).
	EnvSocketName = "LYNA_TMUX_SOCKET_NAME"
	// EnvClaudeTmuxTruecolor lifts Claude Code's 256-color clamp inside tmux.
	EnvClaudeTmuxTruecolor = "CLAUDE_CODE_TMUX_TRUECOLOR"
	// EnvClaudeNoFlicker enables Claude Code's fullscreen renderer.
	EnvClaudeNoFlicker = "CLAUDE_CODE_NO_FLICKER"
	// EnvClaudeEnvScrub strips credentials from subprocess environments.
	EnvClaudeEnvScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"
	// EnvClaudeTeams enables experimental agent teams.
	EnvClaudeTeams = "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"
	// EnvClaudeTeammateCommand replaces the executable Claude Code runs for a
	// teammate. It holds one path, not a command line: Claude Code quotes what
	// it reads as a single word and appends the teammate's own arguments.
	EnvClaudeTeammateCommand = "CLAUDE_CODE_TEAMMATE_COMMAND"
	// EnvClaudeConfigDir relocates Claude Code's user directory.
	EnvClaudeConfigDir = "CLAUDE_CONFIG_DIR"
)
