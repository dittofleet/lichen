package main

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	clikit "github.com/dittofleet/go-cli-kit"
	"github.com/dittofleet/go-cli-kit/postinstall"
	"github.com/dittofleet/go-cli-kit/uninstall"
	"golang.org/x/term"

	"lichen/internal/app"
	"lichen/internal/backup"
	"lichen/internal/config"
	"lichen/internal/core"
	"lichen/internal/events"
	"lichen/internal/files"
	"lichen/internal/mcp"
	"lichen/internal/module"
	"lichen/internal/service"
	"lichen/internal/skills"
)

func cmdSetup(args []string) error {
	if len(args) > 1 {
		return usage("setup", "")
	}
	url := ""
	if len(args) == 1 {
		url = args[0]
	}
	return setup(url)
}

// setup creates the sync folder, brings everything synced to this
// device, and starts the daemon.
func setup(url string) error {
	if _, err := core.DeviceName(); err != nil {
		return err
	}
	dir, err := core.Setup(url)
	if err != nil {
		return err
	}
	fmt.Printf("sync folder: %s\n", config.ContractHome(dir))
	if err := withEnv(func(env *core.Env) error { return module.Pass(env, true) }); err != nil {
		return err
	}
	binary, err := clikit.Executable()
	if err != nil {
		return err
	}
	if err := service.Install(binary); err != nil {
		return err
	}
	fmt.Println("daemon started")
	if url == "" {
		fmt.Println("This sync folder isn't synced anywhere yet: give it a remote with `git -C ~/lichen remote add origin <url>`.")
	}
	fmt.Println("Start syncing with `lichen files add <path> <source>`.")
	return nil
}

// cmdPostinstall is the shared install script's first-time setup. A
// device already set up just gets its daemon onto the new binary.
// LICHEN_REPO skips the question.
func cmdPostinstall() error {
	return postinstall.Run(app.New(), func() error {
		if _, err := core.Load(clilog()); err == nil {
			binary, err := clikit.Executable()
			if err != nil {
				return err
			}
			return service.Install(binary)
		}
		url := os.Getenv("LICHEN_REPO")
		if url == "" && term.IsTerminal(int(os.Stdin.Fd())) {
			url = prompt(bufio.NewReader(os.Stdin), "Sync repo git URL (an empty private repo for your first device, empty to set up later): ")
		}
		if url == "" {
			fmt.Println("Run `lichen setup <git url>` to set this device up.")
			return nil
		}
		return setup(url)
	})
}

func cmdStatus() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	running, state := service.Running()
	if running {
		state = paint("32", state)
	} else {
		state = paint("33", state)
	}
	fmt.Printf("%s   %s\n", bold("daemon:"), state)
	channel, ok := events.Status()
	if ok {
		channel = paint("32", channel)
	} else {
		channel = paint("33", channel+": other devices' changes arrive with the hourly pass")
	}
	fmt.Printf("%s   %s\n", bold("events:"), channel)
	fmt.Printf("%s   %s %s\n", bold("device:"), env.Device.Name, dim("("+strings.Join(env.Device.Groups, ", ")+")"))
	origin := env.Origin()
	if origin == "" {
		origin = paint("33", "no remote: not synced anywhere")
	}
	fmt.Printf("%s     %s %s %s\n", bold("sync:"), config.ContractHome(env.Dir), dim("→"), origin)
	if err := env.Gate(); err != nil {
		fmt.Printf("          %s\n", paint("31", err.Error()))
	}
	if p, err := config.LogPath(); err == nil {
		fmt.Printf("%s      %s\n", bold("log:"), dim(config.ContractHome(p)))
	}

	if entries, err := files.List(env); err == nil {
		here := 0
		for _, e := range entries {
			for _, o := range e.Outputs {
				if env.Device.In(o.Scope) {
					here++
				}
			}
		}
		fmt.Printf("\n%s %d sources, %d outputs on this device\n", bold("files:"), len(entries), here)
	}
	if installed, err := skills.Installed(); err == nil && len(installed) > 0 {
		fmt.Printf("%s %d installed\n", bold("skills:"), len(installed))
	}
	if entries, err := mcp.List(); err == nil {
		var names []string
		for _, e := range entries {
			if len(e.Harnesses) > 0 {
				names = append(names, e.Name)
			}
		}
		if len(names) > 0 {
			fmt.Printf("%s %s\n", bold("mcp:"), strings.Join(names, ", "))
		}
	}
	if broken := files.Broken(env); len(broken) > 0 {
		fmt.Printf("\n%s not synced until their sources compile:\n", paint("31", "broken:"))
		for _, b := range broken {
			fmt.Printf("  %s  %s\n", b.Path, dim(b.Why))
		}
	}
	if holds := files.Holds(); len(holds) > 0 {
		fmt.Printf("\n%s edits here that lichen can't place on its own (lichen resolve):\n", paint("31", "waiting:"))
		for _, h := range holds {
			fmt.Printf("  %s  %s\n", h.Path, dim(h.Why))
		}
	}
	if backup.NonEmpty() {
		if root, err := backup.Root(); err == nil {
			fmt.Printf("\n%s\n", dim("backups of replaced files are in "+config.ContractHome(root)))
		}
	}
	return nil
}

func cmdDevices() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	devices, err := events.Devices()
	if err != nil {
		devices = []events.Device{{Name: env.Device.Name, Online: true, Self: true}}
		fmt.Println(dim("(crosstalk is not running: only this device and the ones in your groups are known)"))
	}
	// Devices named in groups count too, known to crosstalk or not.
	known := len(devices)
	for _, g := range slices.Sorted(maps.Keys(env.Settings.Groups)) {
		for _, name := range env.Settings.Groups[g] {
			if !slices.ContainsFunc(devices, func(d events.Device) bool { return d.Name == name }) {
				devices = append(devices, events.Device{Name: name})
			}
		}
	}
	width := 0
	for _, d := range devices {
		width = max(width, len(d.Name))
	}
	for i, d := range devices {
		note := ""
		switch {
		case d.Self:
			note = " (this device)"
		case !d.Online && i < known:
			note = " (away)"
		}
		fmt.Printf("%-*s  %s%s\n", width, d.Name, strings.Join(env.Settings.GroupsOf(d.Name), ", "), dim(note))
	}
	return nil
}

func cmdGroups(args []string) error {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		return cmdGroupsList()
	}
	if (args[0] != "add" && args[0] != "remove" && args[0] != "rm") || len(args) < 2 || (args[0] == "add" && len(args) < 3) {
		return usage("groups", "")
	}
	op, group, members := args[0], args[1], args[2:]
	subject := strings.TrimSpace(fmt.Sprintf("groups: %s %s %s", op, group, strings.Join(members, " ")))
	var now []string
	if err := change(subject, func(env *core.Env) error {
		if err := env.EditGroup(op == "add", group, members); err != nil {
			return err
		}
		now = env.Settings.Groups[group]
		return nil
	}); err != nil {
		return err
	}
	if len(now) == 0 {
		fmt.Printf("%s: deleted\n", group)
	} else {
		fmt.Printf("%s: %s\n", group, strings.Join(now, ", "))
	}
	return nil
}

func cmdGroupsList() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	groups := env.Settings.Groups
	if len(groups) == 0 {
		fmt.Println("no groups (lichen groups add <group> <device>...)")
	}
	width := 0
	for g := range groups {
		width = max(width, len(g))
	}
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		fmt.Printf("%-*s  %s\n", width, g, strings.Join(groups[g], ", "))
	}
	fmt.Println(dim("built in: mac, linux"))
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

func cmdDaemonCtl(action string) error {
	if !service.Installed() {
		return errors.New("the daemon isn't set up on this device (run: lichen setup <git url>)")
	}
	var err error
	switch action {
	case "start":
		err = service.Start()
	case "stop":
		err = service.Stop()
	case "restart":
		err = service.Restart()
	}
	if err != nil {
		return err
	}
	fmt.Printf("daemon %s\n", map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[action])
	return nil
}

// cmdUninstall removes lichen from this device: the daemon, its local
// state and the binary. The sync folder and every synced file stay, the
// files as plain files.
func cmdUninstall(args []string) error {
	yes := len(args) == 1 && (args[0] == "--yes" || args[0] == "-y")
	if len(args) > 0 && !yes {
		return usage("uninstall", "")
	}
	data, err := config.DataDir()
	if err != nil {
		return err
	}
	return uninstall.Run(app.New(), yes, uninstall.Plan{
		Items: []uninstall.Item{
			{Label: "Daemon", Note: "stopped, and no longer started on its own", Remove: func(string) error { return service.Remove() }},
			{Label: "State", Path: data, Remove: removeState},
		},
		Notice: "Kept: the sync folder (~/lichen) and every synced file, as plain files.",
	})
}

// removeState removes lichen's local state, except harness-limited
// skills, which live in there and stay installed like the ones in
// ~/.agents/skills.
func removeState(dir string) error {
	keep, _ := skills.PrivateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if p := filepath.Join(dir, e.Name()); p != keep {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	os.Remove(dir) // fails, as it should, when skills were kept
	return nil
}
