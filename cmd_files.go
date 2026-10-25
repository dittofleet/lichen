package main

import (
	"errors"
	"fmt"
	"strings"

	"lichen/internal/config"
	"lichen/internal/core"
	"lichen/internal/files"
	"lichen/internal/module"
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
		return withEnv(func(env *core.Env) error {
			var removed []string
			err := module.Change(env, "stop syncing "+tilde(args[1:]...), func() (err error) {
				removed, err = files.Remove(env, args[1:])
				return err
			})
			if err == nil {
				fmt.Printf("stopped syncing %s (the files stay where they are)\n", strings.Join(removed, ", "))
			}
			return err
		})
	case "only":
		if len(args) < 2 {
			return errors.New("usage: lichen files only <source|path> [<group>...]")
		}
		target, groups := args[1], args[2:]
		return withEnv(func(env *core.Env) error {
			msg := fmt.Sprintf("%s goes to %s", tilde(target), core.Describe(groups))
			err := module.Change(env, msg, func() error {
				return files.Only(env, target, groups)
			})
			if err == nil {
				fmt.Println(msg)
			}
			return err
		})
	case "recover":
		if len(args) < 2 {
			return errors.New("usage: lichen files recover <source|path>...")
		}
		return withEnv(func(env *core.Env) error {
			var recovered []string
			err := module.Change(env, "recover "+tilde(args[1:]...), func() (err error) {
				recovered, err = files.Recover(env, args[1:])
				return err
			})
			if err == nil {
				fmt.Printf("recovered %s\n", strings.Join(recovered, ", "))
			}
			return err
		})
	case "resolve":
		if len(args) != 4 || args[2] != "--keep" {
			return errors.New("usage: lichen files resolve <path> --keep here|synced")
		}
		return withEnv(func(env *core.Env) error {
			if err := files.Resolve(env, args[1], args[3]); err != nil {
				return err
			}
			if err := module.Pass(env, false); err != nil {
				return err
			}
			fmt.Printf("resolved %s\n", tilde(args[1]))
			return nil
		})
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
	return withEnv(func(env *core.Env) error {
		if to != "" {
			err := module.Change(env, fmt.Sprintf("write %s to %s", to, tilde(paths[0])), func() error {
				return files.AddOutput(env, to, paths[0], only)
			})
			if err == nil {
				fmt.Printf("%s now also goes to %s, on %s\n", to, tilde(paths[0]), core.Describe(only))
			}
			return err
		}
		var added []string
		err := module.Change(env, "add "+tilde(paths...), func() (err error) {
			added, err = files.Add(env, paths, name, only)
			return err
		})
		if err == nil {
			for i, p := range paths {
				fmt.Printf("syncing %s on %s, from %s\n", tilde(p), core.Describe(only), config.ContractHome(files.SourcePath(added[i])))
			}
		}
		return err
	})
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
			if len(o.Only) > 0 {
				line += "  " + dim("("+strings.Join(o.Only, ", ")+")")
			}
			if !env.Device.In(o.Only) {
				line = dim(fmt.Sprintf("%-*s  %s  (%s, not this device)", width, name, o.Path, strings.Join(o.Only, ", ")))
			}
			fmt.Println(line)
		}
	}
	return nil
}
