package main

import (
	"fmt"
	"slices"
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
	case "update":
		return cmdSkillsUpdate()
	case "list", "ls":
		return cmdSkillsList()
	}
	return fmt.Errorf("usage: lichen skills <add|remove|list|update>")
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
	var key string
	err := change("skills: add "+repo, func(*core.Env) (err error) {
		key, err = skills.AddSource(repo, only)
		return err
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
	if err := withLock(func() error { return skills.Update(clilog()) }); err != nil {
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
		return cmdMCPAdd(args[1:])
	case "remove", "rm":
		return cmdMCPRemove(args[1:])
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
	err := change("mcp: add "+name, func(*core.Env) error { return mcp.AddServer(name, s) })
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
	err := change("mcp: remove "+strings.Join(args, " "), func(*core.Env) error { return mcp.RemoveServers(args) })
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
