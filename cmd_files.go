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
			return errors.New("usage: lichen files remove <source|path>...")
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
	case "only":
		if len(args) < 2 {
			return errors.New("usage: lichen files only <source|path> [<group>...]")
		}
		target, groups := args[1], args[2:]
		msg := fmt.Sprintf("%s goes to %s", targets(target), core.Describe(groups))
		if err := change(msg, func(env *core.Env) error { return files.Only(env, target, groups) }); err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "recover":
		if len(args) < 2 {
			return errors.New("usage: lichen files recover <source|path>...")
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
	case "resolve":
		if len(args) != 4 || args[2] != "--keep" {
			return errors.New("usage: lichen files resolve <path> --keep here|synced")
		}
		p, keep := args[1], args[3]
		if err := change(fmt.Sprintf("resolve %s, keeping %s", tilde(p), keep), func(env *core.Env) error {
			return files.Resolve(env, p, keep)
		}); err != nil {
			return err
		}
		fmt.Printf("resolved %s\n", tilde(p))
		return nil
	case "list", "ls":
		return cmdFilesList()
	}
	return errors.New("usage: lichen files [add|remove|only|recover|resolve]")
}

func cmdFilesAdd(args []string) error {
	var paths, only []string
	var name, to string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--name", "--to", "--only":
			if i++; i >= len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			switch a {
			case "--name":
				name = args[i]
			case "--to":
				to = args[i]
			default:
				var err error
				if only, err = groupList(args[i]); err != nil {
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
		return errors.New("usage: lichen files add <path>... [--name <source>] [--only <groups>]")
	case to != "" && (len(paths) > 1 || name != ""):
		return errors.New("--to takes one path, and no --name")
	}
	if to != "" {
		if err := change(fmt.Sprintf("write %s to %s", to, tilde(paths[0])), func(env *core.Env) error {
			return files.AddOutput(env, to, paths[0], only)
		}); err != nil {
			return err
		}
		fmt.Printf("%s now also goes to %s, on %s\n", to, tilde(paths[0]), core.Describe(only))
		return nil
	}
	var added []string
	if err := change("add "+tilde(paths...), func(env *core.Env) (err error) {
		if added, err = files.Add(env, paths, name, only); err != nil {
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
		fmt.Printf("syncing %s on %s, from %s\n", tilde(p), core.Describe(only), added[i])
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
			case !env.Device.In(o.Only):
				line = dim(fmt.Sprintf("%s  (%s, not this device)", line, core.Describe(o.Only)))
			case len(o.Only) > 0:
				line += "  " + dim("("+core.Describe(o.Only)+")")
			}
			fmt.Println(line)
		}
	}
	return nil
}
