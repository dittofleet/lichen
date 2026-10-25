// lichen keeps your devices in sync, one module per kind of thing: files
// (sources in the sync folder, written wherever each device needs them),
// agent skills installed from public repos, and MCP servers. One daemon
// runs per device, and edits propagate within seconds via crosstalk.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	kitupdate "github.com/dittofleet/go-cli-kit/selfupdate"
	"github.com/dittofleet/go-cli-kit/updatecheck"

	"lichen/internal/app"
	"lichen/internal/config"
	"lichen/internal/core"
	"lichen/internal/daemon"
	"lichen/internal/module"
	"lichen/internal/proclock"
	"lichen/internal/selfupdate"
	"lichen/internal/service"
	"lichen/internal/version"
)

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	lichen := app.New()
	lichen.AfterUpdate = func() error {
		restartDaemon()
		return nil
	}

	var err error
	switch args[0] {
	case "setup":
		err = cmdSetup(args[1:])
	case "postinstall":
		err = cmdPostinstall()
	case "status":
		err = cmdStatus()
	case "sync":
		err = withEnv(func(env *core.Env) error { return module.Pass(env, true) })
	case "files":
		err = cmdFiles(args[1:])
	case "skills":
		err = cmdSkills(args[1:])
	case "mcp":
		err = cmdMCP(args[1:])
	case "groups":
		err = cmdGroups(args[1:])
	case "devices":
		err = cmdDevices()
	case "logs":
		err = cmdLogs()
	case "daemon":
		err = daemon.Run()
	case "start", "stop", "restart":
		err = cmdDaemonCtl(args[0])
	case "update":
		_, err = kitupdate.Run(lichen)
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
	// sync folder requires. Update in place and rerun.
	var outdated *core.OutdatedError
	if errors.As(err, &outdated) {
		err = updateAndRerun(outdated)
	}
	if err != nil {
		log.Fatalf("lichen: %v", err)
	}
	updatecheck.MaybeCheck(lichen, args[0])
}

func usage() {
	fmt.Print(`lichen keeps your devices in sync

  lichen setup [<git url>]         set this device up with your sync repo

  lichen files                     show every synced file and where it goes
  lichen files add <path>... [--name <source>] [--only <groups>]
                                   start syncing files or directories
  lichen files add <path> --to <source> [--only <groups>]
                                   write a source to another path as well
  lichen files remove <source|path>...
                                   stop syncing (files stay where they are)
  lichen files only <source|path> [<group>...]
                                   choose the devices an output is for
  lichen files recover <source|path>...
                                   bring back deleted files, everywhere
  lichen files resolve <path> --keep here|synced
                                   settle a file edited on two devices at once

  lichen skills add <repo> [--skill <name>]... [--only <groups>]
                                   sync agent skills from a public repo
  lichen skills remove <repo|skill...>
                                   stop syncing skills (uninstalls them)
  lichen skills only <repo> [<group>...]
                                   choose the devices a repo's skills are for
  lichen skills list               show every synced skill
  lichen skills update             check skill repos for updates now

  lichen mcp add <name> [--only <groups>] <command> [args...]
  lichen mcp add <name> [--only <groups>] <url>
                                   install an MCP server in every harness
  lichen mcp remove <name...>      stop syncing MCP servers (uninstalls them)
  lichen mcp only <name> [<group>...]
                                   choose the devices a server is for
  lichen mcp list                  show every synced MCP server

  lichen devices                   show your devices and their groups
  lichen groups add <group> <device>...
  lichen groups remove <group> [<device>...]

  lichen sync                      pull and sync everything now
  lichen status                    what lichen is doing on this device
  lichen logs                      follow the daemon log
  lichen start | stop | restart    control the background daemon
  lichen update                    update to the latest release
  lichen uninstall [--yes]         remove lichen, keep your files
  lichen version                   print the installed version

Groups are device names, mac, linux, or your own (lichen groups). Give
several as a comma list: --only mac,servers.
`)
	// `lichen daemon` and `lichen postinstall` are deliberately absent: the
	// service manager and the install script run them, not people.
}

func clilog() *log.Logger { return log.New(os.Stdout, "", 0) }

// withLock runs a command holding the cross-process lock, so a CLI
// command and a daemon pass never race on the sync folder.
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

// withEnv runs a command holding the lock, with this device's view of
// the sync folder.
func withEnv(fn func(env *core.Env) error) error {
	return withLock(func() error {
		env, err := core.Load(clilog())
		if err != nil {
			return err
		}
		return fn(env)
	})
}

// change runs a CLI edit through module.Change: on the freshest shared
// state, committed under subject, and synced.
func change(subject string, edit func(env *core.Env) error) error {
	return withEnv(func(env *core.Env) error {
		return module.Change(env, subject, func() error { return edit(env) })
	})
}

// cmdOnly scopes something to devices and groups, through change.
func cmdOnly(what string, groups []string, scope func(env *core.Env) error) error {
	for _, g := range groups {
		if err := core.ValidGroup(g); err != nil {
			return err
		}
	}
	msg := fmt.Sprintf("%s goes to %s", what, core.Describe(groups))
	if err := change(msg, scope); err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// notHere shows a listed thing that is for other devices.
func notHere(line string, only []string) string {
	return dim(fmt.Sprintf("%s  (%s, not this device)", line, core.Describe(only)))
}

// updateAndRerun installs the release the sync folder requires, restarts
// the daemon on it, and execs the original command on the new binary.
// The env guard is keyed to the requirement that triggered the update: a
// rerun refused by the SAME requirement means the install didn't take
// (mis-stamped release) and must not loop.
func updateAndRerun(outdated *core.OutdatedError) error {
	if os.Getenv("LICHEN_AUTOUPDATED") == outdated.Repo {
		return outdated
	}
	fmt.Printf("lichen %s is behind the sync folder (%s): updating...\n", outdated.Build, outdated.Repo)
	tag, err := selfupdate.Required(outdated.Repo)
	if err != nil {
		return err
	}
	fmt.Printf("updated %s -> %s\n", outdated.Build, tag)
	restartDaemon()
	fmt.Println("rerunning the command on the new build...")
	os.Setenv("LICHEN_AUTOUPDATED", outdated.Repo)
	return selfupdate.ExecSelf()
}

// restartDaemon puts the daemon on a freshly installed binary.
// Best-effort: no daemon, no problem.
func restartDaemon() {
	if service.Installed() {
		if err := service.Restart(); err == nil {
			fmt.Println("daemon restarted on the new build")
		}
	}
}

// Colors are for interactive output only: never in daemon logs, never
// when piped, never when NO_COLOR is set.
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

// tilde shows paths the way lichen stores them, ~/-relative: the shell
// has usually expanded the ~ already, and a home directory's absolute
// path does not belong in the shared history.
func tilde(paths ...string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		if abs, err := config.Abs(p); err == nil {
			out[i] = config.ContractHome(abs)
		}
	}
	return strings.Join(out, " ")
}

// targets shows arguments that are a source name or a path, as files
// remove, only and recover take them: a name stays as it is.
func targets(args ...string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") || strings.HasPrefix(a, ".") {
			out[i] = tilde(a)
		}
	}
	return strings.Join(out, " ")
}

// groupList parses a comma list of groups, as --only takes them.
func groupList(s string) ([]string, error) {
	var groups []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		if err := core.ValidGroup(g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}
