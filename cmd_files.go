package main

import (
	"fmt"
	"os"
	"strings"

	"lichen/internal/config"
	"lichen/internal/core"
	"lichen/internal/events"
	"lichen/internal/files"
)

func cmdFiles(args []string) error {
	if len(args) == 0 {
		return cmdFilesList()
	}
	switch args[0] {
	case "add":
		return cmdFilesAdd(args[1:])
	case "remove", "rm":
		if len(args) < 2 {
			return usage("files", "remove")
		}
		var removed []string
		if err := change("stop syncing "+targets(args[1:]...), func(env *core.Env) (err error) {
			removed, err = files.Remove(env, args[1:])
			return err
		}); err != nil {
			return err
		}
		fmt.Printf("stopped syncing %s (the files stay where they are)\n", strings.Join(removed, ", "))
		return nil
	case "include", "exclude":
		return cmdScope("files", args, files.SetScope)
	case "recover":
		if len(args) < 2 {
			return usage("files", "recover")
		}
		var recovered []string
		if err := change("recover "+targets(args[1:]...), func(env *core.Env) (err error) {
			recovered, err = files.Recover(env, args[1:])
			return err
		}); err != nil {
			return err
		}
		fmt.Printf("recovered %s\n", strings.Join(recovered, ", "))
		return nil
	case "list", "ls":
		return cmdFilesList()
	case "preview":
		return cmdFilesPreview(args[1:])
	}
	return fmt.Errorf("unknown command %q (see: lichen help files)", args[0])
}

func cmdFilesAdd(args []string) error {
	var pos []string
	var to string
	var scope core.Scope
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--to", "--include", "--exclude":
			if i++; i >= len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			switch a {
			case "--to":
				to = args[i]
			default:
				if err := scopeFlag(&scope, a, args[i]); err != nil {
					return err
				}
			}
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q", a)
			}
			pos = append(pos, a)
		}
	}
	if to != "" {
		if len(pos) != 1 {
			return usage("files", "add")
		}
		if err := change(fmt.Sprintf("write %s to %s", to, tilde(pos[0])), func(env *core.Env) error {
			return files.AddOutput(env, to, pos[0], scope)
		}); err != nil {
			return err
		}
		fmt.Printf("%s now also goes to %s, on %s\n", to, tilde(pos[0]), core.Describe(scope))
		return nil
	}
	if len(pos) != 2 {
		return usage("files", "add")
	}
	p, name := pos[0], pos[1]
	var at string
	if err := change("add "+tilde(p), func(env *core.Env) error {
		at = config.ContractHome(env.Path(files.SourcesDir, name))
		return files.Add(env, p, name, scope)
	}); err != nil {
		return err
	}
	fmt.Printf("syncing %s on %s, from %s\n", tilde(p), core.Describe(scope), at)
	return nil
}

func cmdFilesPreview(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usage("files", "preview")
	}
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	d := env.Device
	if len(args) == 2 && args[1] != d.Name {
		oses, err := events.OSes()
		if err != nil {
			return fmt.Errorf("previewing another device takes its OS, from crosstalk: %w", err)
		}
		if oses[args[1]] == "" {
			return fmt.Errorf("%s has not told crosstalk its OS: is lichen running there? (see: lichen devices)", args[1])
		}
		d = env.Settings.Device(args[1], oses[args[1]])
	}
	content, off, err := files.Preview(env, args[0], d)
	if err != nil {
		return err
	}
	os.Stdout.Write(content)
	if off {
		fmt.Fprintln(os.Stderr, dim(fmt.Sprintf("(%s doesn't write this file)", d.Name)))
	}
	return nil
}

func cmdFilesList() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	entries, err := files.List(env)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no files synced (lichen files add <path> <source>)")
		return nil
	}
	width := 0
	for _, e := range entries {
		width = max(width, len(e.Name)+1)
	}
	for _, e := range entries {
		name := e.Name
		if e.Dir {
			name += "/"
		}
		for i, o := range e.Outputs {
			if i > 0 {
				name = ""
			}
			line := fmt.Sprintf("%-*s  %s", width, name, o.Path)
			switch {
			case !env.Device.In(o.Scope):
				line = notHere(line, o.Scope)
			case !o.Scope.Everywhere():
				line += "  " + dim("("+core.Describe(o.Scope)+")")
			}
			fmt.Println(line)
		}
	}
	return nil
}
