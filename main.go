// lichen keeps your dev machines in sync, one module per kind of thing:
// files (dotfiles, slash commands, anything under your home directory),
// agent skills installed from public repos, and MCP servers. One daemon
// per machine, edits propagate within seconds via crosstalk, skill repos
// are polled.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"lichen/internal/backup"
	"lichen/internal/config"
	"lichen/internal/daemon"
	"lichen/internal/events"
	"lichen/internal/files"
	"lichen/internal/mcp"
	"lichen/internal/module"
	"lichen/internal/proclock"
	"lichen/internal/selfupdate"
	"lichen/internal/skills"
	"lichen/internal/version"
)

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "status":
		err = cmdStatus()
	case "sync":
		err = cmdSyncCmd(args[1:])
	case "skills":
		err = cmdSkills(args[1:])
	case "mcp":
		err = cmdMCP(args[1:])
	case "logs":
		err = cmdLogs()
	case "daemon":
		err = daemon.Run()
	case "start", "stop", "restart":
		err = cmdDaemonCtl(args[0])
	case "update":
		err = cmdUpdate()
	case "uninstall":
		err = cmdUninstall(args[1:])
	case "version", "--version":
		fmt.Println("lichen " + version.Current)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "lichen: unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
	// The version gate refused the command: this build is older than the
	// sync repo requires. Update in place and rerun.
	var outdated *files.OutdatedError
	if errors.As(err, &outdated) {
		err = updateAndRerun(outdated)
	}
	if err != nil {
		log.Fatalf("lichen: %v", err)
	}
}

// updateAndRerun installs the release the sync repo requires, restarts
// the daemon on it, and execs the original command on the new binary.
// The env guard is keyed to the requirement that triggered the update:
// a rerun refused by the SAME requirement means the install didn't take
// (mis-stamped release) and must not loop, while a strictly newer
// requirement arriving mid-flight legitimately updates once more.
func updateAndRerun(outdated *files.OutdatedError) error {
	if os.Getenv("LICHEN_AUTOUPDATED") == outdated.Repo {
		return outdated
	}
	fmt.Printf("lichen %s is behind the sync repo (%s): updating...\n", outdated.Build, outdated.Repo)
	tag, err := selfupdate.Required(outdated.Repo)
	if err != nil {
		return err
	}
	fmt.Printf("updated %s -> %s\n", outdated.Build, tag)
	restartDaemonIfLoaded()
	fmt.Println("rerunning the command on the new build...")
	os.Setenv("LICHEN_AUTOUPDATED", outdated.Repo)
	return selfupdate.ExecSelf()
}

// restartDaemonIfLoaded bounces the launchd agent so it picks up a
// freshly installed binary. Best-effort: no daemon, no problem.
func restartDaemonIfLoaded() {
	if daemonLoaded() {
		if err := launchctlRun("kickstart", "-k", daemonTarget()); err == nil {
			fmt.Println("daemon restarted on the new build")
		}
	}
}

func usage() {
	fmt.Print(`lichen keeps the files on your machines in sync

  lichen sync <path...>            start syncing files across machines
  lichen sync                      pull and apply everything now
  lichen sync recover <path...>    bring back files deleted everywhere
  lichen sync list                 show every synced file

  lichen skills add <repo> [--skill <name>]...
                                   sync agent skills from a public repo
  lichen skills remove <repo|skill...>
                                   stop syncing skills (uninstalls them)
  lichen skills list               show every synced skill
  lichen skills update             check skill repos for updates now

  lichen mcp add <name> <command> [args...]
  lichen mcp add <name> <url>      install an MCP server in every harness
  lichen mcp remove <name...>      stop syncing MCP servers (uninstalls them)
  lichen mcp list                  show every synced MCP server

  lichen status                    daemon health
  lichen logs                      tail the daemon log
  lichen start | stop | restart    control the background daemon
  lichen update                    self-update to the latest release
  lichen uninstall [--yes]         remove lichen, keep your files
  lichen version                   print the installed version

aliases: ls = list
setup: install.sh   teardown: uninstall.sh (or lichen uninstall)
`)
	// `lichen daemon` is deliberately absent: it's launchd's entry point
	// (see install.sh's plist), not a command users run themselves.
}

func clilog() *log.Logger { return log.New(os.Stdout, "", 0) }

// withLock runs a mutating command holding the cross-process lock, so a
// CLI command and a daemon pass never race on the chezmoi git repo. It
// prints a hint if the daemon is mid-pass.
func withLock(fn func() error) error {
	release, err := proclock.Acquire(context.Background(), func() {
		fmt.Fprintln(os.Stderr, "waiting for the lichen daemon to finish its current sync...")
	})
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// Colors are for interactive status output only: never in daemon logs,
// never when piped, never when NO_COLOR is set.
var useColor = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func bold(s string) string { return paint("1", s) }
func dim(s string) string  { return paint("2", s) }

// cmdSyncCmd dispatches the files module's subcommands, mirroring the
// skills module's shape. Anything else is treated as paths to start
// syncing (a file literally named "recover" or "list" needs a ./ prefix
// or absolute path).
func cmdSyncCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "recover":
		return withLock(func() error { return cmdRecover(args[1:]) })
	case "list", "ls":
		return cmdList()
	}
	return withLock(func() error { return cmdSync(args) })
}

// cmdSync with paths starts managing them. With none it runs a full pass
// over every module: pull, capture local edits, apply, refresh skills.
func cmdSync(paths []string) error {
	lg := clilog()
	cfg, err := files.LoadConfig(lg)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		if err := module.ReconcileAll(cfg, lg); err != nil {
			return err
		}
		lg.Printf("ok")
		return nil
	}
	if err := files.Sync(cfg, lg, paths); err != nil {
		return err
	}
	fmt.Printf("syncing: %s\n", strings.Join(paths, ", "))
	return nil
}

func cmdRecover(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("usage: lichen sync recover <path...>")
	}
	lg := clilog()
	cfg, err := files.LoadConfig(lg)
	if err != nil {
		return err
	}
	recovered, err := files.Recover(cfg, lg, paths)
	if err != nil {
		return err
	}
	if len(recovered) == 0 {
		fmt.Println("nothing to recover")
		return nil
	}
	fmt.Printf("recovered: %s\n", strings.Join(recovered, ", "))
	return nil
}

func cmdList() error {
	if !files.Active() {
		return fmt.Errorf("no sync repo initialized (run install.sh with LICHEN_REPO=<git url>)")
	}
	managed, err := files.Managed()
	if err != nil {
		return err
	}
	for _, p := range managed {
		fmt.Println(config.ContractHome(p))
	}
	return nil
}

func cmdSkills(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "add":
		return withLock(func() error { return cmdSkillsAdd(args[1:]) })
	case "remove", "rm":
		return withLock(func() error { return cmdSkillsRemove(args[1:]) })
	case "update":
		return withLock(func() error { return cmdSkillsUpdate() })
	case "list", "ls":
		return cmdSkillsList()
	}
	return fmt.Errorf("usage: lichen skills <add|remove|list|update>")
}

// configChange is the shared tail of the mutating skills and mcp
// subcommands: pull the freshest shared state, apply an edit to the
// module's config file on top of it, make the installed things match
// (refresh), and publish the change to the other machines.
func configChange(path func() (string, error), refresh func(*log.Logger) error, edit func() (subject string, err error)) error {
	p, err := path()
	if err != nil {
		return err
	}
	lg := clilog()
	cfg, err := files.LoadConfig(lg)
	if err != nil {
		return err
	}
	// Pull before editing: module configs are shared state, and an edit on a
	// stale copy would push the staleness (commitPush resolves conflicts
	// in favor of the local commit, silently reverting another machine's
	// change). Offline just means the edit bases on the freshest state
	// this machine can get. An outdated build stops HERE, before it
	// writes anything, and updateAndRerun replays the command.
	if err := files.Reconcile(cfg, lg); err != nil {
		var outdated *files.OutdatedError
		if errors.As(err, &outdated) {
			return err
		}
		lg.Printf("files: %v (continuing with local state)", err)
	}
	subject, err := edit()
	if err != nil {
		return err
	}
	if err := refresh(lg); err != nil {
		return err
	}
	if err := files.CaptureConfig(cfg, subject, p, lg); err != nil {
		lg.Printf("config not pushed (%v), other machines catch up on a later pass", err)
	}
	return nil
}

func cmdSkillsAdd(args []string) error {
	var repo string
	var only []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--skill":
			if i++; i >= len(args) {
				return fmt.Errorf("--skill needs a name")
			}
			only = append(only, args[i])
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown flag %q", args[i])
		case repo != "":
			return fmt.Errorf("one repo at a time (got %q and %q)", repo, args[i])
		default:
			repo = args[i]
		}
	}
	if repo == "" {
		return fmt.Errorf("usage: lichen skills add <owner/repo> [--skill <name>]...")
	}
	// force: adding is the moment upstream freshness is user-visible
	// (--skill names are validated against the repo's current state).
	var key string
	err := configChange(config.SkillsPath, skills.Update, func() (string, error) {
		var err error
		key, err = skills.AddSource(repo, only)
		return "skills: add " + key, err
	})
	if err != nil {
		return err
	}
	installed, err := skills.Installed()
	if err != nil {
		return err
	}
	var mine []string
	for _, s := range installed {
		if s.Repo == key {
			mine = append(mine, s.Name)
		}
	}
	if len(mine) == 0 {
		fmt.Printf("no skills installed from %s (see messages above)\n", key)
		return nil
	}
	fmt.Printf("syncing %d skill(s) from %s: %s\n", len(mine), key, strings.Join(mine, ", "))
	return nil
}

func cmdSkillsRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lichen skills remove <repo|skill...>")
	}
	// No forced poll: removal needs no upstream contact at all.
	err := configChange(config.SkillsPath, skills.Reconcile, func() (string, error) {
		return "skills: remove " + strings.Join(args, " "), skills.RemoveSources(args)
	})
	if err != nil {
		return err
	}
	fmt.Printf("stopped syncing: %s\n", strings.Join(args, ", "))
	return nil
}

func cmdSkillsUpdate() error {
	if err := skills.Update(clilog()); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdSkillsList() error {
	installed, err := skills.Installed()
	if err != nil {
		return err
	}
	configured := skills.ConfiguredKeys()
	if len(installed) == 0 && len(configured) == 0 {
		fmt.Println("no skills synced (lichen skills add <owner/repo>)")
		return nil
	}
	width := 0
	for _, s := range installed {
		width = max(width, len(s.Name))
	}
	fromRepo := map[string]bool{}
	for _, s := range installed {
		fromRepo[s.Repo] = true
		repo := s.Repo
		if len(s.OnlyFor) > 0 {
			repo += " (" + strings.Join(s.OnlyFor, ", ") + " only)"
		}
		fmt.Printf("%-*s  %s\n", width, s.Name, dim(repo))
	}
	// A source with nothing installed yet: a fresh machine before its
	// first pass, or a repo that failed to clone.
	for _, key := range configured {
		if !fromRepo[key] {
			fmt.Printf("%s\n", dim(key+" (nothing installed yet, see: lichen logs)"))
		}
	}
	return nil
}

func cmdMCP(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "add":
		return withLock(func() error { return cmdMCPAdd(args[1:]) })
	case "remove", "rm":
		return withLock(func() error { return cmdMCPRemove(args[1:]) })
	case "list", "ls":
		return cmdMCPList()
	}
	return fmt.Errorf("usage: lichen mcp <add|remove|list>")
}

func cmdMCPAdd(args []string) error {
	// A `--` before the command is what `claude mcp add` and `codex mcp
	// add` expect, so tolerate it out of habit.
	if len(args) > 1 && args[1] == "--" {
		args = slices.Delete(slices.Clone(args), 1, 2)
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: lichen mcp add <name> <command> [args...]  or  lichen mcp add <name> <url>")
	}
	name, rest := args[0], args[1:]
	s := mcp.Server{Command: rest[0], Args: rest[1:]}
	if mcp.IsURL(rest[0]) {
		if len(rest) > 1 {
			return fmt.Errorf("a url takes no arguments (got %q)", strings.Join(rest[1:], " "))
		}
		s = mcp.Server{URL: rest[0]}
	}
	err := configChange(config.MCPPath, mcp.Reconcile, func() (string, error) {
		return "mcp: add " + name, mcp.AddServer(name, s)
	})
	if err != nil {
		return err
	}
	entries, err := mcp.List()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		if len(e.Harnesses) == 0 {
			fmt.Printf("syncing %s, but it isn't installed on this machine (see messages above)\n", name)
			return nil
		}
		fmt.Printf("syncing %s, installed in %s (restart running sessions to pick it up)\n", name, strings.Join(e.Harnesses, ", "))
	}
	return nil
}

func cmdMCPRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lichen mcp remove <name...>")
	}
	err := configChange(config.MCPPath, mcp.Reconcile, func() (string, error) {
		return "mcp: remove " + strings.Join(args, " "), mcp.RemoveServers(args)
	})
	if err != nil {
		return err
	}
	fmt.Printf("stopped syncing: %s\n", strings.Join(args, ", "))
	return nil
}

func cmdMCPList() error {
	entries, err := mcp.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no MCP servers synced (lichen mcp add <name> <command or url>)")
		return nil
	}
	width := 0
	for _, e := range entries {
		width = max(width, len(e.Name))
	}
	for _, e := range entries {
		where := "not installed here, see: lichen logs"
		if len(e.Harnesses) > 0 {
			where = strings.Join(e.Harnesses, ", ")
		}
		fmt.Printf("%-*s  %s  %s\n", width, e.Name, e.Server, dim("("+where+")"))
	}
	return nil
}

func cmdLogs() error {
	p, err := config.LogPath()
	if err != nil {
		return err
	}
	tail, err := exec.LookPath("tail")
	if err != nil {
		return err
	}
	return syscall.Exec(tail, []string{"tail", "-n", "50", "-F", p}, os.Environ())
}

func cmdStatus() error {
	if _, err := config.Load(); err != nil {
		return err
	}
	state := launchdState()
	if strings.HasPrefix(state, "running") {
		state = paint("32", state)
	} else {
		state = paint("33", state)
	}
	fmt.Printf("%s  %s\n", bold("daemon:"), state)
	channel, ok := events.Status()
	if ok {
		channel = paint("32", channel)
	} else {
		channel = paint("33", channel+": other machines' changes arrive with the hourly pass")
	}
	fmt.Printf("%s  %s\n", bold("events:"), channel)
	if p, err := config.LogPath(); err == nil {
		fmt.Printf("%s     %s\n", bold("log:"), dim(p))
	}
	for _, tool := range []string{"git", "chezmoi"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Printf("%s\n", paint("31", tool+" not found on PATH (brew install "+tool+")"))
		}
	}

	origin := files.Origin()
	if files.Active() {
		managed, _ := files.Managed()
		originNote := origin
		if originNote == "" {
			originNote = "(no origin: commits stay local)"
		}
		fmt.Printf("\n%s sync repo %s\n", bold(fmt.Sprintf("files (%d synced):", len(managed))), originNote)
	} else {
		fmt.Printf("\n%s %s\n", bold("files:"), paint("33", "not initialized (run install.sh with LICHEN_REPO=<git url>)"))
	}

	if installed, err := skills.Installed(); err == nil {
		if repos := skills.ConfiguredKeys(); len(repos) > 0 || len(installed) > 0 {
			fmt.Printf("\n%s %s\n", bold(fmt.Sprintf("skills (%d installed):", len(installed))), strings.Join(repos, ", "))
		}
	}

	if entries, err := mcp.List(); err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Printf("\n%s %s\n", bold(fmt.Sprintf("mcp (%d servers):", len(entries))), strings.Join(names, ", "))
	}

	if backup.NonEmpty() {
		if root, err := backup.Root(); err == nil {
			fmt.Printf("\n%s\n", dim("backups of overwritten files exist in "+root))
		}
	}
	return nil
}

const daemonLabel = "dev.lichen"

func guiDomain() string    { return fmt.Sprintf("gui/%d", os.Getuid()) }
func daemonTarget() string { return guiDomain() + "/" + daemonLabel }
func daemonLoaded() bool   { return exec.Command("launchctl", "print", daemonTarget()).Run() == nil }

func plistPath() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemonLabel+".plist"), nil
}

func launchctlRun(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchdState() string {
	out, err := exec.Command("launchctl", "print", daemonTarget()).CombinedOutput()
	if err != nil {
		return "not registered (run install.sh)"
	}
	state := "registered"
	if m := regexp.MustCompile(`state = (\w+)`).FindSubmatch(out); m != nil {
		state = string(m[1])
	}
	if m := regexp.MustCompile(`pid = (\d+)`).FindSubmatch(out); m != nil {
		state += " (pid " + string(m[1]) + ")"
	}
	return state
}

// cmdDaemonCtl controls the launchd agent: stop unloads it, start loads it,
// restart kickstarts a loaded one (or loads it if it was stopped).
func cmdDaemonCtl(action string) error {
	plist, err := plistPath()
	if err != nil {
		return err
	}
	loaded := daemonLoaded()
	switch action {
	case "stop":
		if !loaded {
			fmt.Println("daemon already stopped")
			return nil
		}
		if err := launchctlRun("bootout", daemonTarget()); err != nil {
			return err
		}
		fmt.Println("daemon stopped")
	case "start":
		if _, err := os.Stat(plist); err != nil {
			return fmt.Errorf("no launchd plist at %s (run install.sh first)", plist)
		}
		if loaded {
			fmt.Println("daemon already running")
			return nil
		}
		if err := launchctlRun("bootstrap", guiDomain(), plist); err != nil {
			return err
		}
		fmt.Println("daemon started")
	case "restart":
		if _, err := os.Stat(plist); err != nil {
			return fmt.Errorf("no launchd plist at %s (run install.sh first)", plist)
		}
		if loaded {
			if err := launchctlRun("kickstart", "-k", daemonTarget()); err != nil {
				return err
			}
		} else if err := launchctlRun("bootstrap", guiDomain(), plist); err != nil {
			return err
		}
		fmt.Println("daemon restarted")
	}
	return nil
}

// cmdUpdate self-updates to the latest GitHub release: fetch the latest
// tag, download this platform's asset, and rename it over the running
// binary.
func cmdUpdate() error {
	if !version.IsRelease() {
		return fmt.Errorf("dev build (built from source): update by re-running dev.sh, or install the released binary via install.sh")
	}
	fmt.Println("checking the latest release...")
	tag, err := selfupdate.LatestTag()
	if err != nil {
		return err
	}
	switch {
	case tag == version.Current:
		fmt.Println("already at the latest release: " + version.Current)
		return nil
	case version.Compare(tag, version.Current) < 0:
		// A rollback: the latest published release is now OLDER than this
		// build, meaning a release was deleted. This command is the only
		// way off a pulled build, so install it, but loudly: the sync
		// repo's marker may still require the deleted version and needs
		// lowering by hand.
		fmt.Printf("latest release %s is older than this build (%s): a release was deleted, downgrading.\n", tag, version.Current)
		fmt.Printf("If syncing pauses afterwards, lower %s in the sync repo to %s or below.\n", version.Marker, tag)
	}
	fmt.Printf("downloading %s...\n", tag)
	if err := selfupdate.Install(tag); err != nil {
		return err
	}
	fmt.Printf("updated %s -> %s\n", version.Current, tag)
	restartDaemonIfLoaded()
	return nil
}

// cmdUninstall unwinds lichen from this machine so it can later be
// reinstalled against the same or a different sync repo: it stops the
// daemon and removes the binary, lichen's config and local state, and the
// chezmoi source and state. The files lichen applied to $HOME are KEPT, as
// plain files, as is the sync repo itself.
func cmdUninstall(args []string) error {
	yes := len(args) > 0 && (args[0] == "--yes" || args[0] == "-y")
	if !yes {
		fmt.Println("This removes lichen from this machine:")
		fmt.Println("  daemon + launchd agent, the lichen binary, config (~/.config/lichen),")
		fmt.Println("  local state (~/.local/share/lichen), and the chezmoi source and state.")
		fmt.Println("Kept: every synced file in your home directory, and the sync repo on GitHub.")
		fmt.Print("Proceed? [y/N] ")
		reply, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || !slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(reply))) {
			return fmt.Errorf("aborted (pass --yes to skip this prompt)")
		}
	}
	if daemonLoaded() {
		launchctlRun("bootout", daemonTarget())
	}
	var removed []string
	rm := func(path, label string) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		if os.RemoveAll(path) == nil {
			removed = append(removed, label)
		}
	}
	if plist, err := plistPath(); err == nil {
		rm(plist, "launchd agent")
	}
	if p, err := config.Path(); err == nil {
		rm(filepath.Dir(p), "config (~/.config/lichen)")
	}
	if d, err := config.DataDir(); err == nil {
		// Harness-limited skills live in there and stay installed, like
		// the ones in ~/.agents/skills: the rest is lichen's own state.
		keep, _ := skills.PrivateDir()
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if p := filepath.Join(d, e.Name()); p != keep {
				os.RemoveAll(p)
			}
		}
		os.Remove(d) // fails, as it should, when skills were kept
		if len(entries) > 0 {
			removed = append(removed, "local state (~/.local/share/lichen)")
		}
	}
	// The chezmoi source and state, so a later install can initialize a
	// different sync repo cleanly. The applied files in $HOME are not here,
	// they stay put.
	if p, err := files.SourcePath(); err == nil {
		rm(p, "chezmoi source repo")
	}
	if home, err := config.Home(); err == nil {
		rm(filepath.Join(home, ".config", "chezmoi"), "chezmoi state")
	}
	// Binary last: on macOS a running executable removing its own path is
	// fine, the inode lives until the process exits.
	if self, err := os.Executable(); err == nil {
		rm(self, "binary")
	}
	fmt.Println("lichen uninstalled.")
	if len(removed) > 0 {
		fmt.Println("Removed: " + strings.Join(removed, ", "))
	}
	fmt.Println("Kept: the files in your home directory (now plain files), and your sync repo on GitHub.")
	fmt.Println("Re-run install.sh anytime to sync again, pointing at the same or a different repo.")
	return nil
}
