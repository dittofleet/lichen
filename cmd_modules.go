package main

import (
	"fmt"
	"strings"

	"lichen/internal/core"
	"lichen/internal/mcp"
	"lichen/internal/skills"
)

func cmdSkills(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "add":
		return cmdSkillsAdd(args[1:])
	case "remove", "rm":
		return cmdSkillsRemove(args[1:])
	case "include", "exclude":
		return cmdScope("skills", "<repo>", args, func(_ *core.Env, repo string, scope core.Scope) error { return skills.SetScope(repo, scope) })
	case "update":
		return cmdSkillsUpdate()
	case "list", "ls":
		return cmdSkillsList()
	}
	if sub != "" {
		return fmt.Errorf("unknown command %q (see: lichen help skills)", sub)
	}
	return printHelp([]string{"skills"})
}

func cmdSkillsAdd(args []string) error {
	var repo string
	var names []string
	var scope core.Scope
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--skill":
			if i++; i >= len(args) {
				return fmt.Errorf("--skill needs a name")
			}
			names = append(names, args[i])
		case args[i] == "--include" || args[i] == "--exclude":
			if i++; i >= len(args) {
				return fmt.Errorf("%s needs devices or groups", args[i-1])
			}
			if err := scopeFlag(&scope, args[i-1], args[i]); err != nil {
				return err
			}
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown flag %q", args[i])
		case repo != "":
			return fmt.Errorf("one repo at a time (got %q and %q)", repo, args[i])
		default:
			repo = args[i]
		}
	}
	if repo == "" {
		return fmt.Errorf("usage: lichen skills add <owner/repo> [--skill <name>]... [--include|--exclude <devices,groups>]")
	}
	var src skills.Source
	var here bool
	err := change("skills: add "+repo, func(env *core.Env) (err error) {
		src, err = skills.AddSource(repo, names, scope, env.Log)
		here = env.Device.In(src.Scope)
		return err
	})
	if err != nil {
		return err
	}
	if !here {
		fmt.Printf("syncing %s on %s, so not here\n", src.Repo, core.Describe(src.Scope))
		return nil
	}
	installed, err := skills.Installed()
	if err != nil {
		return err
	}
	var mine []string
	for _, s := range installed {
		if s.Repo == src.Repo {
			mine = append(mine, s.Name)
		}
	}
	if len(mine) == 0 {
		fmt.Printf("no skills installed from %s (see messages above)\n", src.Repo)
		return nil
	}
	fmt.Printf("syncing %d skill(s) from %s: %s\n", len(mine), src.Repo, strings.Join(mine, ", "))
	return nil
}

func cmdSkillsRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lichen skills remove <repo|skill...>")
	}
	err := change("skills: remove "+strings.Join(args, " "), func(*core.Env) error {
		return skills.RemoveSources(args)
	})
	if err != nil {
		return err
	}
	fmt.Printf("stopped syncing: %s\n", strings.Join(args, ", "))
	return nil
}

func cmdSkillsUpdate() error {
	if err := withEnv(skills.Update); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdSkillsList() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	installed, err := skills.Installed()
	if err != nil {
		return err
	}
	configured := skills.Configured()
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
	for _, src := range configured {
		switch {
		case fromRepo[src.Repo]:
		case !env.Device.In(src.Scope):
			fmt.Println(notHere(src.Repo, src.Scope))
		default:
			// Nothing installed yet: a fresh machine before its first
			// pass, or a repo that failed to clone.
			fmt.Println(dim(src.Repo + " (nothing installed yet, see: lichen logs)"))
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
		return cmdMCPAdd(args[1:])
	case "remove", "rm":
		return cmdMCPRemove(args[1:])
	case "include", "exclude":
		return cmdScope("mcp", "<name>", args, func(_ *core.Env, name string, scope core.Scope) error { return mcp.SetScope(name, scope) })
	case "list", "ls":
		return cmdMCPList()
	}
	if sub != "" {
		return fmt.Errorf("unknown command %q (see: lichen help mcp)", sub)
	}
	return printHelp([]string{"mcp"})
}

func cmdMCPAdd(args []string) error {
	usage := fmt.Errorf("usage: lichen mcp add <name> [--include|--exclude <devices,groups>] <command> [args...]  or  lichen mcp add <name> [--include|--exclude <devices,groups>] <url>")
	if len(args) < 2 {
		return usage
	}
	// Flags go before the command: whatever follows it is its own.
	name, rest := args[0], args[1:]
	var scope core.Scope
	for len(rest) > 1 && (rest[0] == "--include" || rest[0] == "--exclude") {
		if err := scopeFlag(&scope, rest[0], rest[1]); err != nil {
			return err
		}
		rest = rest[2:]
	}
	// A `--` before the command is what `claude mcp add` and `codex mcp
	// add` expect, so tolerate it out of habit.
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return usage
	}
	s := mcp.Server{Command: rest[0], Args: rest[1:]}
	if mcp.IsURL(rest[0]) {
		if len(rest) > 1 {
			return fmt.Errorf("a url takes no arguments (got %q)", strings.Join(rest[1:], " "))
		}
		s = mcp.Server{URL: rest[0]}
	}
	s.Scope = scope
	var here bool
	err := change("mcp: add "+name, func(env *core.Env) error {
		here = env.Device.In(scope)
		return mcp.AddServer(name, s)
	})
	if err != nil {
		return err
	}
	if !here {
		fmt.Printf("syncing %s on %s, so not here\n", name, core.Describe(scope))
		return nil
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
	err := change("mcp: remove "+strings.Join(args, " "), func(*core.Env) error { return mcp.RemoveServers(args) })
	if err != nil {
		return err
	}
	fmt.Printf("stopped syncing: %s\n", strings.Join(args, ", "))
	return nil
}

func cmdMCPList() error {
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
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
		line := fmt.Sprintf("%-*s  %s", width, e.Name, e.Server)
		if !env.Device.In(e.Server.Scope) {
			fmt.Println(notHere(line, e.Server.Scope))
			continue
		}
		where := "not installed here, see: lichen logs"
		if len(e.Harnesses) > 0 {
			where = strings.Join(e.Harnesses, ", ")
		}
		if !e.Server.Scope.Everywhere() {
			where = core.Describe(e.Server.Scope) + ": " + where
		}
		fmt.Println(line + "  " + dim("("+where+")"))
	}
	return nil
}
