package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Traces of lyna-tmux in files it does not own: they are read to tell the
// user what is left, never edited.
const (
	// UninstallClaudePlugin is the Claude Code command that removes the companion plugin.
	UninstallClaudePlugin = "/plugin uninstall " + uninstallClaudePluginID
	// uninstallClaudePluginID is the plugin's key in the Claude Code plugin registry.
	uninstallClaudePluginID = "lyna-tmux@lyna-tmux"
	// uninstallClaudeRegistry is the registry file, below the Claude Code home.
	uninstallClaudeRegistry = "plugins/installed_plugins.json"
	// uninstallTPMPlugin is the tmux plugin manager name of plugin mode.
	uninstallTPMPlugin = "bayoudhdev/lyna-claude-tmux"
	// uninstallReadLimit bounds every user file uninstall looks into.
	uninstallReadLimit = 1 << 20
	// uninstallProbeTimeout bounds the connection that tells a running server
	// from a socket file left behind.
	uninstallProbeTimeout = 500 * time.Millisecond
	// uninstallStopTimeout bounds how long Apply waits for a server that
	// acknowledged kill-server to close its socket, and uninstallStopPoll is
	// how often it looks: the server has nothing to hand over, so a wait
	// beyond a few event loop turns means it is not going away.
	uninstallStopTimeout = 5 * time.Second
	uninstallStopPoll    = 10 * time.Millisecond
)

// ErrUninstallInside reports an uninstall started from a pane of the server it
// would stop.
var ErrUninstallInside = errors.New("uninstall runs from a pane of the lyna-tmux server it stops")

// UninstallManual is a step uninstall leaves to the user: what to do and why
// it is theirs, then how, as a shell command, a Claude Code command or the
// line to delete.
type UninstallManual struct {
	Step string
	How  string
}

// UninstallPlan is what uninstall stops and removes, and what it leaves to
// the user.
type UninstallPlan struct {
	// SocketName is the tmux -L name of the server that is stopped.
	SocketName string
	// ServerSocket is the socket of the server when one answers on it, so
	// stopping it is part of the plan; empty otherwise.
	ServerSocket string
	// Dirs are the existing lyna-tmux directories removed, in order.
	Dirs []string
	// Themes are the Claude Code theme files lyna-tmux generated and nobody changed since.
	Themes []string
	// Completions are the shell completion scripts `lmux completion`
	// wrote where setup told the user to put them.
	Completions []string
	// KeptConfig is the configuration directory left in place without purge,
	// empty when it is removed or absent.
	KeptConfig string
	// Binary is the lmux executable when uninstall removes it, last of all;
	// empty when it is gone or left to the user.
	Binary string
	// Alias is the link under the name the command had in 1.0.0, removed
	// with the binary it points at; empty when there is none.
	Alias string
	// Manual is what remains for the user once Apply is done.
	Manual []UninstallManual
}

// Empty reports a plan that stops nothing and removes nothing, so that
// confirming it would change nothing.
func (p UninstallPlan) Empty() bool {
	return p.ServerSocket == "" && len(p.Dirs) == 0 && len(p.Themes) == 0 && len(p.Completions) == 0 &&
		p.Binary == "" && p.Alias == ""
}

// UninstallPlanFor lists what uninstall stops and removes. purge adds the
// configuration directory. Nothing is changed.
func UninstallPlanFor(h Host, purge bool) (UninstallPlan, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return UninstallPlan{}, err
	}
	p := UninstallPlan{SocketName: tmux.DefaultSocketName}
	if name := h.Getenv(session.EnvSocketName); name != "" {
		if err := session.Validate(name); err != nil {
			return UninstallPlan{}, fmt.Errorf("%s: %w", session.EnvSocketName, err)
		}
		p.SocketName = name
	}
	if socket := SocketPath(h.Getenv, p.SocketName); uninstallServerAnswers(socket) {
		p.ServerSocket = socket
	}
	dirs := []string{paths.State, paths.Cache, paths.Data}
	if purge {
		dirs = append(dirs, paths.Config)
	}
	for _, dir := range dirs {
		if err := uninstallCheckDir(h, dir); err != nil {
			return UninstallPlan{}, err
		}
		if _, err := os.Lstat(dir); err == nil {
			p.Dirs = append(p.Dirs, dir)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return UninstallPlan{}, err
		}
	}
	if _, err := os.Lstat(paths.Config); err == nil && !purge {
		p.KeptConfig = paths.Config
	}
	claudeHome := xdg.ClaudeHome(h.Getenv, h.Home)
	p.Themes, err = uninstallThemes(claudeHome)
	if err != nil {
		return UninstallPlan{}, err
	}
	// The binary goes first in the manual list: without it the rest of the
	// list is what a fresh machine would still show.
	var manual *UninstallManual
	bin, alias := uninstallNames(h.Exe)
	p.Binary, manual, err = uninstallBinary(bin, os.Getuid())
	if err != nil {
		return UninstallPlan{}, err
	}
	if manual != nil {
		p.Manual = append(p.Manual, *manual)
	}
	// The link goes only where the binary it points at goes.
	if p.Binary != "" {
		p.Alias = alias
	}
	var foreign []UninstallManual
	p.Completions, foreign, err = uninstallCompletions(h.Getenv, paths.Home)
	if err != nil {
		return UninstallPlan{}, err
	}
	p.Manual = append(p.Manual, foreign...)
	if uninstallClaudePluginInstalled(claudeHome) {
		p.Manual = append(p.Manual, UninstallManual{Step: "in Claude Code, remove the companion plugin", How: UninstallClaudePlugin})
	}
	lines, err := uninstallTmuxConfLines(h.Getenv, paths.Home)
	if err != nil {
		return UninstallPlan{}, err
	}
	p.Manual = append(p.Manual, lines...)
	return p, nil
}

// uninstallServerAnswers reports whether a tmux server accepts connections on
// socket. A socket file alone proves nothing: tmux leaves one behind when the
// server dies, and its clients unlink it on the next refused connection.
func uninstallServerAnswers(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, uninstallProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// uninstallAwaitStopped returns once nothing answers on socket any more, as
// reported by answers. tmux acknowledges kill-server from its event loop and
// exits on a later turn, so the socket can still accept a connection after
// the command returned; a run that ended there would report the server
// stopped while a run right after it, or the user, still finds it. The wait
// is bounded by ctx and uninstallStopTimeout, and a server still answering
// at the end is an error rather than a stop that never happened.
func uninstallAwaitStopped(ctx context.Context, socket string, answers func(string) bool) error {
	ctx, cancel := context.WithTimeout(ctx, uninstallStopTimeout)
	defer cancel()
	tick := time.NewTicker(uninstallStopPoll)
	defer tick.Stop()
	for answers(socket) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("the server on %s is still answering after kill-server: %w", socket, ctx.Err())
		case <-tick.C:
		}
	}
	return nil
}

// uninstallCompletionPaths are the completion scripts `lmux setup` tells
// the user to write, one per shell, plus the places bash and fish load from
// when the XDG variables move them: a user who followed the guidance to the
// letter and one who adapted it to their layout both get found.
func uninstallCompletionPaths(getenv func(string) string, home string) []string {
	var out []string
	add := func(path string) {
		if !slices.Contains(out, path) {
			out = append(out, path)
		}
	}
	xdgDir := func(name, fallback string) string {
		if v := getenv(name); v != "" && filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
		return fallback
	}
	// Both names: the scripts of an installation from 1.0.0 are named after
	// the command as it was then, and nothing renamed them.
	for _, name := range []string{xdg.Command, xdg.CommandWas} {
		add(filepath.Join(home, ".local", "share", "bash-completion", "completions", name))
		add(filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "bash-completion", "completions", name))
		add(filepath.Join(home, ".zfunc", "_"+name))
		add(filepath.Join(home, ".config", "fish", "completions", name+".fish"))
		add(filepath.Join(xdgDir("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "fish", "completions", name+".fish"))
	}
	return out
}

// uninstallCompletions sorts the completion paths into the scripts
// `lmux completion` wrote, which are removed, and files of another
// origin at those paths, which are named to the user: a dotfile manager's
// link or an edited script is not lyna-tmux's to delete.
func uninstallCompletions(getenv func(string) string, home string) (remove []string, manual []UninstallManual, err error) {
	for _, path := range uninstallCompletionPaths(getenv, home) {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if info.Mode().IsRegular() && uninstallGeneratedCompletion(path) {
			remove = append(remove, path)
			continue
		}
		manual = append(manual, UninstallManual{
			Step: "remove " + path + " if it is lyna-tmux's: it is not the script `lmux completion` writes",
			How:  "rm " + path,
		})
	}
	return remove, manual, nil
}

// uninstallCompletionHeaders are the first lines of the completion scripts
// the CLI generates, as words, one per shell and per name the command has
// been installed under, so a script written for 1.0.0 is recognized as ours
// too. The bash and fish headers go on with an editor mode marker, which is
// not compared.
var uninstallCompletionHeaders = func() [][]string {
	var headers [][]string
	for _, name := range []string{xdg.Command, xdg.CommandWas} {
		headers = append(headers,
			[]string{"#", "bash", "completion", "V2", "for", name},
			[]string{"#compdef", name},
			[]string{"#", "fish", "completion", "for", name})
	}
	return headers
}()

// uninstallGeneratedCompletion reports whether path is a regular file that
// starts like a completion script the CLI generates for its own name.
func uninstallGeneratedCompletion(path string) bool {
	data, err := fsx.ReadFileNoFollow(path, uninstallReadLimit)
	if err != nil {
		return false
	}
	first, _, _ := strings.Cut(string(data), "\n")
	words := strings.Fields(first)
	for _, header := range uninstallCompletionHeaders {
		if len(words) >= len(header) && slices.Equal(words[:len(header)], header) {
			return true
		}
	}
	return false
}

// uninstallClaudePluginInstalled reports whether the Claude Code plugin
// registry lists the companion plugin. No registry means no plugin was ever
// installed. A registry that cannot be read or decoded, or whose shape is
// not the known one, counts as listing it: a command that finds nothing to
// uninstall costs the user less than a plugin left behind.
func uninstallClaudePluginInstalled(claudeHome string) bool {
	data, err := fsx.ReadFileLimited(filepath.Join(claudeHome, filepath.FromSlash(uninstallClaudeRegistry)), uninstallReadLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	var registry struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &registry); err != nil || registry.Plugins == nil {
		return true
	}
	_, ok := registry.Plugins[uninstallClaudePluginID]
	return ok
}

// uninstallConfMarkers are the strings that make a line of the user's tmux
// configuration ours: the plugin manager entry, the directory name, which is
// in every path of the layout, and both names of the command.
var uninstallConfMarkers = []string{uninstallTPMPlugin, xdg.AppName, xdg.Command, xdg.CommandWas}

// uninstallTmuxConfLines finds the lines of the user's tmux configuration
// that mention us: the plugin manager entry and the source-file line of
// plugin mode, which would break their tmux start once the state directory is
// gone, plus anything naming the command or the directory, a binding that runs
// it for instance. Both names of the command are looked for, so a line written
// against 1.0.0 is found as well. The files tmux reads are looked at, links
// followed because dotfile managers link them, and nothing is written.
func uninstallTmuxConfLines(getenv func(string) string, home string) ([]UninstallManual, error) {
	configHome := filepath.Join(home, ".config")
	if v := getenv("XDG_CONFIG_HOME"); v != "" && filepath.IsAbs(v) {
		configHome = filepath.Clean(v)
	}
	var out []UninstallManual
	for _, file := range []string{filepath.Join(home, ".tmux.conf"), filepath.Join(configHome, "tmux", "tmux.conf")} {
		data, err := fsx.ReadFileLimited(file, uninstallReadLimit)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !slices.ContainsFunc(uninstallConfMarkers, func(m string) bool { return strings.Contains(line, m) }) {
				continue
			}
			out = append(out, UninstallManual{Step: fmt.Sprintf("remove line %d of %s", i+1, file), How: strings.TrimSpace(line)})
		}
	}
	return out, nil
}

// uninstallOwned lists, for each directory of the layout, the entries
// lyna-tmux creates there. The names come from the path helpers, so a renamed
// file cannot silently drop out of the list.
func uninstallOwned(paths xdg.Paths) map[string][]string {
	owned := map[string][]string{}
	add := func(dir string, names ...string) {
		clean := filepath.Clean(dir)
		owned[clean] = append(owned[clean], names...)
	}
	log := filepath.Base(paths.LogFile())
	add(paths.State, filepath.Base(paths.TmuxConf()), filepath.Base(paths.SettingsDir()), log, log+".1", pluginTmuxStateDir)
	add(paths.Cache, filepath.Base(paths.AgentsCache()))
	add(paths.Data, filepath.Base(paths.ReviewDir()))
	add(paths.Config, filepath.Base(paths.ConfigFile()), filepath.Base(paths.LocalTmuxConf()))
	return owned
}

// uninstallCheckDir refuses to remove a directory lyna-tmux cannot show it
// owns. Being one of the four resolved directories is not enough on its own:
// LYNA_TMUX_HOME names all four at once, so with it set to a home directory
// they are plain names such as "config" and "state" that the user may have
// been using for years. A directory of the XDG layout carries the application
// name and nothing else is in it; anywhere else every entry must be one
// lyna-tmux writes.
func uninstallCheckDir(h Host, dir string) error {
	clean := filepath.Clean(dir)
	refuse := func(why string) error { return fmt.Errorf("refusing to remove %s: %s", dir, why) }
	if !filepath.IsAbs(clean) || clean == filepath.Dir(clean) || clean == filepath.Clean(h.Home) {
		return refuse("it is not a lyna-tmux directory")
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return err
	}
	owned, known := uninstallOwned(paths)[clean]
	if !known {
		return refuse("it is not a lyna-tmux directory")
	}
	if filepath.Base(clean) == xdg.AppName {
		return nil
	}
	entries, err := os.ReadDir(clean)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !slices.Contains(owned, e.Name()) {
			return refuse(filepath.Join(dir, e.Name()) + " is not something lyna-tmux created")
		}
	}
	return nil
}

// uninstallThemes lists the theme files in the Claude Code themes directory
// that lyna-tmux generated and that are unchanged. A themes directory that is
// a link is not looked into.
func uninstallThemes(claudeHome string) ([]string, error) {
	dir := filepath.Join(claudeHome, claudetheme.DirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") && uninstallGeneratedTheme(path) {
			out = append(out, path)
		}
	}
	return out, nil
}

// uninstallGeneratedTheme reports whether path is a regular theme file that
// claudetheme recognizes as generated and unchanged.
func uninstallGeneratedTheme(path string) bool {
	data, err := fsx.ReadFileNoFollow(path, claudetheme.MaxFileBytes)
	return err == nil && claudetheme.Recognize(data)
}

// UninstallResult reports what Apply did.
type UninstallResult struct {
	// ServerStopped reports that a running lyna-tmux server was stopped.
	ServerStopped bool
	Removed       []string
}

// Apply stops the lyna-tmux server of the plan's socket name, then removes the
// review installation, the listed directories, the theme files that are still
// generated and unchanged, the completion scripts still generated, and the
// binary last of all. It refuses to run from a pane of that server, which
// would stop it midway. Other tmux servers are never contacted.
func (p UninstallPlan) Apply(ctx context.Context, h Host) (UninstallResult, error) {
	var res UninstallResult
	socket := SocketPath(h.Getenv, p.SocketName)
	if current, _, ok := strings.Cut(h.Getenv("TMUX"), ","); ok && filepath.Clean(current) == socket {
		return res, fmt.Errorf("%w; run lmux uninstall from a terminal outside lyna-tmux", ErrUninstallInside)
	}
	client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: p.SocketName}, Env: ServerEnviron(h.Environ)})
	_, err := client.Run(ctx, "kill-server")
	switch {
	case err == nil:
		if err := uninstallAwaitStopped(ctx, socket, uninstallServerAnswers); err != nil {
			return res, fmt.Errorf("stop the lyna-tmux server: %w", err)
		}
		res.ServerStopped = true
	case errors.Is(err, tmux.ErrNoServer), errors.Is(err, tmux.ErrNotInstalled):
	default:
		return res, fmt.Errorf("stop the lyna-tmux server: %w", err)
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return res, err
	}
	if err := review.Uninstall(paths); err != nil {
		return res, err
	}
	for _, dir := range p.Dirs {
		if err := uninstallCheckDir(h, dir); err != nil {
			return res, err
		}
		if err := os.RemoveAll(dir); err != nil {
			return res, fmt.Errorf("remove %s: %w", dir, err)
		}
		res.Removed = append(res.Removed, dir)
	}
	for _, theme := range p.Themes {
		// The file is checked again: it may have been edited since the plan.
		if !uninstallGeneratedTheme(theme) {
			continue
		}
		if err := os.Remove(theme); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("remove %s: %w", theme, err)
		}
		res.Removed = append(res.Removed, theme)
	}
	for _, script := range p.Completions {
		// Checked again: the script may have been replaced since the plan.
		if !uninstallGeneratedCompletion(script) {
			continue
		}
		if err := os.Remove(script); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("remove %s: %w", script, err)
		}
		res.Removed = append(res.Removed, script)
	}
	if p.Binary == "" {
		return res, nil
	}
	// The binary goes last so that a refusal or a failure here leaves
	// everything else removed rather than a half-removed state. It is
	// classified again, before anything is unlinked: the file may have been
	// replaced since the plan, and one that stopped being ours to remove keeps
	// the link under the old name, which is then still the way to it.
	remove, manual, err := uninstallBinary(p.Binary, os.Getuid())
	if err != nil {
		return res, err
	}
	if manual != nil {
		return res, fmt.Errorf("%s (%s)", manual.Step, manual.How)
	}
	// The link goes before the binary: removed after it, a failure in between
	// would leave it pointing at nothing. It too is checked again, and left
	// alone unless it still points at the binary beside it.
	if p.Alias != "" {
		if _, alias := uninstallNames(p.Alias); alias == p.Alias {
			if err := os.Remove(p.Alias); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, fmt.Errorf("remove %s: %w", p.Alias, err)
			}
			res.Removed = append(res.Removed, p.Alias)
		}
	}
	if remove == "" {
		return res, nil
	}
	if err := os.Remove(remove); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("remove %s: %w", remove, err)
	}
	res.Removed = append(res.Removed, remove)
	return res, nil
}
