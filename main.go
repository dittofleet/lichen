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
		printHelp(nil)
		os.Exit(2)
	}
	// `lichen <command> --help` and `lichen <command> <sub> --help`.
	for i := 1; i < len(args) && i <= 2; i++ {
		if isHelp(args[i]) {
			// After an argument that is no subcommand, the command's page.
			err := printHelp(args[:i])
			if err != nil && i == 2 {
				err = printHelp(args[:1])
			}
			if err != nil {
				log.Fatalf("lichen: %v", err)
			}
			return
		}
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
	case "resolve":
		err = cmdResolve(args[1:])
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
		err = printHelp(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "lichen: unknown command %q\n\n", args[0])
		printHelp(nil)
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

// cmdScope runs `<module> include|exclude <thing> [<device|group>...]`,
// scoping the thing through set. Excluding nothing is every device
// again.
func cmdScope(module string, args []string, set func(env *core.Env, thing string, scope core.Scope) error) error {
	if len(args) < 2 || (args[0] == "include" && len(args) < 3) {
		return usage(module, args[0])
	}
	thing, names := args[1], args[2:]
	for _, n := range names {
		if err := core.ValidGroup(n); err != nil {
			return err
		}
	}
	scope := core.Scope{Exclude: names}
	if args[0] == "include" {
		scope = core.Scope{Include: names}
	}
	msg := fmt.Sprintf("%s: %s goes to %s", module, targets(thing), core.Describe(scope))
	if err := change(msg, func(env *core.Env) error { return set(env, thing, scope) }); err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// scopeFlag adds to scope from --include or --exclude and a comma list
// of devices and groups. A thing has one or the other.
func scopeFlag(scope *core.Scope, flag, list string) error {
	if (flag == "--include" && scope.Exclude != nil) || (flag == "--exclude" && scope.Include != nil) {
		return errors.New("use --include or --exclude, not both")
	}
	var names []string
	for _, n := range strings.Split(list, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if err := core.ValidGroup(n); err != nil {
			return err
		}
		names = append(names, n)
	}
	if flag == "--include" {
		scope.Include = append(scope.Include, names...)
	} else {
		scope.Exclude = append(scope.Exclude, names...)
	}
	return nil
}

// notHere shows a listed thing that is for other devices.
func notHere(line string, scope core.Scope) string {
	return dim(fmt.Sprintf("%s  (%s, not this device)", line, core.Describe(scope)))
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

// tilde shows a path the way lichen stores it, ~/-relative: the shell
// has usually expanded the ~ already, and a home directory's absolute
// path does not belong in the shared history.
func tilde(p string) string {
	if abs, err := config.Abs(p); err == nil {
		return config.ContractHome(abs)
	}
	return p
}

// targets shows arguments that are a source name or a path, as files
// remove, include, exclude and recover take them: a name stays as it is.
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
