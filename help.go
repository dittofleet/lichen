package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// A helpCmd is one top-level command: a line in the overview, and a page
// of its own (`lichen help <name>`, `lichen <name> --help`).
type helpCmd struct {
	name    string
	usage   string
	summary string
	about   string // the page's introduction
	subs    []helpSub
	outro   string // the page's closing section
}

// A helpSub is one subcommand form. Forms sharing a name share a page
// (`lichen <cmd> <name> --help`).
type helpSub struct {
	name    string
	usage   string
	summary string
	detail  string
}

type helpSection struct {
	title string
	cmds  []helpCmd
}

var help = []helpSection{
	{"Sync", []helpCmd{
		{
			name:    "files",
			usage:   "files [<command>]",
			summary: "Sync files and directories",
			about: "A source lives in ~/lichen/files and is written to one or more outputs. " +
				"Edits on either side sync. Sources can insert others with § lines (see the README).",
			subs: []helpSub{
				{"list", "files [list]", "List sources and their outputs", ""},
				{"preview", "files preview <source|path> [<device>]", "Show a file as a device writes it",
					"This device when none is named."},
				{"add", "files add <path> <source> [--include|--exclude <devices,groups>]", "Sync a file or directory",
					"The source is its name in ~/lichen/files, like shell/zshrc."},
				{"add", "files add <path> --to <source> [--include|--exclude <devices,groups>]", "Add an output to a source", ""},
				{"remove", "files remove <source|path>...", "Stop syncing", "Outputs stay as plain files."},
				{"include", "files include <source|path> <device|group>...", "Limit outputs to these devices",
					"A path targets one output, a source name all of its outputs."},
				{"exclude", "files exclude <source|path> [<device|group>...]", "Write outputs to all but these devices",
					"With no devices, reverts to every device."},
				{"recover", "files recover <source|path>...", "Restore deleted files on every device", ""},
			},
		},
		{
			name:    "skills",
			usage:   "skills <command>",
			summary: "Sync agent skills from public repos",
			about: "Installed to ~/.agents/skills and linked from ~/.claude/skills. Repos are " +
				"checked for updates hourly.",
			subs: []helpSub{
				{"add", "skills add <repo> [--skill <name>]... [--include|--exclude <devices,groups>]", "Sync skills from a public repo",
					"All skills in the repo, or the ones named with --skill."},
				{"remove", "skills remove <repo|skill>...", "Stop syncing and uninstall", ""},
				{"include", "skills include <repo> <device|group>...", "Limit a repo's skills to these devices", ""},
				{"exclude", "skills exclude <repo> [<device|group>...]", "Install on all but these devices",
					"With no devices, reverts to every device."},
				{"list", "skills list", "List synced skills", ""},
				{"update", "skills update", "Check repos for updates now", ""},
			},
		},
		{
			name:    "mcp",
			usage:   "mcp <command>",
			summary: "Sync MCP servers into Claude Code and Codex",
			about: "Installed at user scope through each harness's CLI. Missing harnesses are " +
				"skipped. Running sessions need a restart to see changes.",
			subs: []helpSub{
				{"add", "mcp add <name> [--include|--exclude <devices,groups>] <command> [<arg>...]", "Add a server that runs a command",
					"Flags go before the command."},
				{"add", "mcp add <name> [--include|--exclude <devices,groups>] <url>", "Add a server at a URL", ""},
				{"remove", "mcp remove <name>...", "Stop syncing and uninstall", ""},
				{"include", "mcp include <name> <device|group>...", "Limit a server to these devices", ""},
				{"exclude", "mcp exclude <name> [<device|group>...]", "Install on all but these devices",
					"With no devices, reverts to every device."},
				{"list", "mcp list", "List synced servers", ""},
			},
		},
		{
			name:    "resolve",
			usage:   "resolve [<path> [--keep here|synced] [--into <source>]...]",
			summary: "Settle held edits",
			about: "An edit is held when it conflicts with another device's, or adds lines where " +
				"an insert begins or ends. With no arguments, steps through each held edit. " +
				"--keep picks a version. --into names the source for new lines.",
		},
		{name: "sync", usage: "sync", summary: "Pull and sync now"},
	}},
	{"Devices", []helpCmd{
		{name: "devices", usage: "devices", summary: "List devices and their groups"},
		{
			name:    "groups",
			usage:   "groups [<command>]",
			summary: "Manage device groups",
			about: "A scope decides which devices get a file, skill repo or server: a list of " +
				"devices and groups, either included or excluded. Without one, everything goes " +
				"to every device. Set it with --include or --exclude when adding, or with the " +
				"module's include and exclude commands.\n\n" +
				"mac and linux are built-in groups. Custom groups are stored in " +
				"~/lichen/lichen.toml and shared by all devices.",
			subs: []helpSub{
				{"list", "groups [list]", "List groups and their devices", ""},
				{"add", "groups add <group> <device>...", "Add devices to a group",
					"Creates the group if needed. Devices don't need to have joined yet."},
				{"remove", "groups remove <group> [<device>...]", "Remove devices from a group",
					"With no devices, or once the last one is removed, deletes the group."},
			},
			outro: "Names are lowercase letters, digits and dashes. mac and linux are reserved.\n\n" +
				"Examples\n" +
				"  lichen groups add servers hetzner-1 hetzner-2\n" +
				"  lichen files add ~/.zshrc shell/zshrc --exclude servers\n" +
				"  lichen mcp include playwright mac lychee",
		},
	}},
	{"This device", []helpCmd{
		{
			name:    "setup",
			usage:   "setup [<git url>]",
			summary: "Set up this device",
			about:   "Clones the sync repo to ~/lichen, syncs, and starts the daemon.",
		},
		{name: "status", usage: "status", summary: "Show sync status"},
		{name: "logs", usage: "logs", summary: "Follow the daemon log"},
		{name: "start", usage: "start | stop | restart", summary: "Control the daemon"},
		{name: "update", usage: "update", summary: "Update lichen"},
		{
			name:    "uninstall",
			usage:   "uninstall [--yes]",
			summary: "Remove lichen from this device",
			about:   "~/lichen and synced files are kept.",
		},
		{name: "version", usage: "version", summary: "Print the version"},
		{name: "help", usage: "help [<command>]", summary: "Show help"},
	}},
	// `lichen daemon` and `lichen postinstall` are deliberately absent: the
	// service manager and the install script run them, not people.
}

// isHelp reports whether an argument asks for help.
func isHelp(arg string) bool { return arg == "-h" || arg == "--help" }

// printHelp prints the overview, a command's page, or a subcommand's.
func printHelp(topic []string) error {
	if len(topic) == 0 {
		overview()
		return nil
	}
	c, ok := findCmd(alias(map[string]string{"stop": "start", "restart": "start"}, topic[0]))
	if !ok {
		return fmt.Errorf("no help for %q (see: lichen help)", topic[0])
	}
	if len(topic) == 1 {
		commandPage(c)
		return nil
	}
	forms := c.forms(alias(map[string]string{"rm": "remove", "ls": "list"}, topic[1]))
	if len(forms) == 0 {
		return fmt.Errorf("no help for %q (see: lichen help %s)", strings.Join(topic, " "), c.name)
	}
	for i, s := range forms {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("Usage: lichen %s\n  %s\n", s.usage, s.summary)
		if s.detail != "" {
			fmt.Println(wrap(s.detail, 2))
		}
	}
	return nil
}

// forms lists the subcommand forms named sub, or all of them without one.
func (c helpCmd) forms(sub string) []helpSub {
	var forms []helpSub
	for _, s := range c.subs {
		if sub == "" || s.name == sub {
			forms = append(forms, s)
		}
	}
	return forms
}

// usage is the error for a command used wrong, showing its usage as the
// help has it: a subcommand's forms, or the command's.
func usage(name, sub string) error {
	c, _ := findCmd(name)
	lines := []string{"lichen " + c.usage}
	if len(c.subs) > 0 {
		lines = nil
		for _, s := range c.forms(sub) {
			lines = append(lines, "lichen "+s.usage)
		}
	}
	// Aligned under the first, after main's "lichen: " prefix.
	return errors.New("usage: " + strings.Join(lines, "\n"+strings.Repeat(" ", len("lichen: usage: "))))
}

func alias(names map[string]string, s string) string {
	if a, ok := names[s]; ok {
		return a
	}
	return s
}

func findCmd(name string) (helpCmd, bool) {
	for _, sec := range help {
		for _, c := range sec.cmds {
			if c.name == name {
				return c, true
			}
		}
	}
	return helpCmd{}, false
}

func overview() {
	fmt.Print("lichen keeps your devices in sync\n\nUsage: lichen <command> [args]\n" +
		"Run `lichen help <command>` for a command's details.\n")
	for _, sec := range help {
		fmt.Printf("\n%s\n", sec.title)
		for _, c := range sec.cmds {
			var detail string
			if len(c.subs) > 0 {
				var names []string
				for _, s := range c.subs {
					if len(names) == 0 || names[len(names)-1] != s.name {
						names = append(names, s.name)
					}
				}
				detail = strings.Join(names, ", ")
			}
			entry(c.usage, c.summary, detail, 26)
		}
	}
}

func commandPage(c helpCmd) {
	fmt.Printf("lichen %s\n\nUsage: lichen %s\n", c.name, c.usage)
	if len(c.subs) == 0 {
		fmt.Printf("\n%s\n", c.summary)
	}
	if c.about != "" {
		for _, p := range strings.Split(c.about, "\n\n") {
			fmt.Printf("\n%s\n", wrap(p, 0))
		}
	}
	if len(c.subs) > 0 {
		fmt.Println("\nCommands")
		for _, s := range c.subs {
			entry(s.usage, s.summary, s.detail, 40)
		}
	}
	if c.outro != "" {
		fmt.Printf("\n%s\n", c.outro)
	}
}

// entry prints a usage and its summary in a column at col, the summary
// on a line of its own when the usage reaches it, with any detail
// wrapped beneath the summary.
func entry(usage, summary, detail string, col int) {
	line := "  " + usage
	if len(line) > col-2 {
		fmt.Println(line)
		line = ""
	}
	fmt.Printf("%-*s%s\n", col, line, summary)
	if detail != "" {
		fmt.Println(wrap(detail, col))
	}
}

const helpWidth = 80

// wrap fills text to helpWidth, every line indented by indent.
func wrap(text string, indent int) string {
	pad := strings.Repeat(" ", indent)
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && indent+utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) > helpWidth {
			lines = append(lines, pad+line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, pad+line)
	}
	return strings.Join(lines, "\n")
}
