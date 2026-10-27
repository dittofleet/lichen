package main

import (
	"errors"
	"fmt"
	"strings"

	"lichen/internal/config"
	"lichen/internal/core"
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
	}
	return fmt.Errorf("unknown command %q (see: lichen help files)", args[0])
}

func cmdFilesAdd(args []string) error {
	var paths []string
	var name, to string
	var scope core.Scope
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--name", "--to", "--include", "--exclude":
			if i++; i >= len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			switch a {
			case "--name":
				name = args[i]
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
			paths = append(paths, a)
		}
	}
	switch {
	case len(paths) == 0:
		return usage("files", "add")
	case to != "" && (len(paths) > 1 || name != ""):
		return errors.New("--to takes one path, and no --name")
	}
	if to != "" {
		if err := change(fmt.Sprintf("write %s to %s", to, tilde(paths[0])), func(env *core.Env) error {
			return files.AddOutput(env, to, paths[0], scope)
		}); err != nil {
			return err
		}
		fmt.Printf("%s now also goes to %s, on %s\n", to, tilde(paths[0]), core.Describe(scope))
		return nil
	}
	var added []string
	if err := change("add "+tilde(paths...), func(env *core.Env) (err error) {
		if added, err = files.Add(env, paths, name, scope); err != nil {
			return err
		}
		for i := range added {
			added[i] = config.ContractHome(env.Path(files.SourcesDir, added[i]))
		}
		return nil
	}); err != nil {
		return err
	}
	for i, p := range paths {
		fmt.Printf("syncing %s on %s, from %s\n", tilde(p), core.Describe(scope), added[i])
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
		fmt.Println("no files synced (lichen files add <path>)")
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
