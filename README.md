<img src="assets/icon.svg" width="80" alt="lichen icon">

# lichen

A project for easily keeping dev environments in sync across your Macs,
Linux and Windows machines.

It syncs things, one module per kind of thing:

- **files**: dotfiles, slash commands, `CLAUDE.md`, app configs, anything
  under your home directory, written wherever each device needs it.
- **skills**: agent skills installed from public repos, kept up to date
  by polling.
- **mcp**: MCP servers, installed into Claude Code and Codex.

lichen is a pun on 'liken' and just how lichen behaves in general.

## Setup

Each device needs:

- git access that can clone the sync repo (SSH key or credentials, since
  the repo should be private)
- [crosstalk](https://github.com/dittofleet/crosstalk), installed and
  joined to your hub

lichen names each device the way crosstalk does, and nudges your other
devices through it after each push, so they pull within seconds. Pushes
made outside lichen arrive on an hourly pass.

Create an empty private repo once (the sync repo, e.g. `you/lichen-sync`),
then on every device:

```sh
curl -fsSL https://raw.githubusercontent.com/dittofleet/.github/main/install.sh | sh -s lichen
```

On Windows, in PowerShell:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/dittofleet/.github/main/install.ps1))) lichen
```

It asks for the sync repo's URL (or takes it from `LICHEN_REPO`), clones
it to `~/lichen`, and starts the daemon: a launchd agent on macOS, a
systemd user service on Linux. `lichen setup <git url>` does the same
later, if you skip the question.

On Windows (x64) the daemon is one of your startup apps, listed as
`lichenw`, the program that starts it at login with no window. Nothing
starts it again if it stops before your next login: `lichen start` does.

## The sync folder

`~/lichen` holds everything lichen syncs, as plain files:

```
~/lichen/
  lichen.toml   device groups and settings
  files.toml    where each file goes
  skills.toml
  mcp.toml
  files/        the files themselves
```

The configs have one entry per output, repo or server:

```toml
# files.toml
["~/.zshrc"]
source = "shell/zshrc"
include = ["mac"]

["~/.codex/AGENTS.md"]
source = "agents/CLAUDE.md"
include = ["mac", "servers"]
exclude = ["hetzner-2"]
```

Edit anything in it and lichen commits and pushes it. You never run git
there yourself, but its history is all there.

## Which devices get what

Nothing syncs to a device that isn't named. Every file output, skill
repo and server has a scope: the devices and groups it goes to. Adding
something names only the device it was added on, unless `--include`
names others:

```sh
lichen files add ~/.zshrc shell/zshrc   # this device
lichen files add ~/.vimrc vim/vimrc --include mac,lychee
lichen files include ~/.zshrc lychee servers   # more devices later
lichen files exclude ~/.zshrc lychee           # or fewer
```

An excluded device stays out even when a group it is in is included.

Skills and MCP servers work the same way, and [inserts](#inserts) use
`if` and `unless`. A device that falls out of scope keeps its files as
plain files and uninstalls its skills and servers.

`lichen devices` lists device names (crosstalk's) and their groups. `mac`,
`linux` and `windows` are built-in groups, and so is `all`, which takes
in every device that joins later too. Custom groups are stored in
`~/lichen/lichen.toml`:

```sh
lichen groups add servers hetzner-1 hetzner-2
lichen groups remove servers hetzner-2   # no devices: deletes the group
lichen groups                            # list groups
```

## Files

```sh
lichen files add ~/.zshrc shell/zshrc
```

The path becomes the source `shell/zshrc` in `~/lichen/files`, with an
output where it came from. Every device writes the outputs meant for it,
and editing either side syncs: the output (`~/.zshrc`) or the source
(`~/lichen/files/shell/zshrc`). A directory syncs as a whole, including
files added to it later.

A file is the same on every device byte for byte: line endings are never
converted, whatever git is set to do. On Windows a path can also be
written `~\.gitconfig`.

`lichen files` shows every source and where it goes, and `lichen files
preview <source|path> [<device>]` prints a file as a device writes it,
with its [inserts](#inserts) settled for that device.

### Several outputs

One source can have several outputs, each with its own
[scope](#which-devices-get-what):

```sh
lichen files add ~/.codex/AGENTS.md --to agents/CLAUDE.md --include mac
lichen files add ~/.config/agents.md --to agents/CLAUDE.md --include linux
```

Edit any of them and the others follow. `lichen files include` and
`exclude` take a path for one output, or a source name for all of them.

When a file's scope takes in a device that has its own copy, the synced
version replaces it. A file only you can read (`~/.netrc`,
say) is written that way everywhere.

Windows marks no file as executable or as only yours to read. A script
first added there becomes executable everywhere once you `chmod +x` it on
a Mac or Linux device, and a file only you may read is best added from
one.

Windows does not allow some file names (`a:b`, `what?`, `aux`), and to
Windows and macOS two names that differ only by case are one name. A
device that can't hold a file another one added takes in nothing more
until the file is renamed where it came from, and its log says which
file. Nothing is deleted meanwhile, and what was changed on that device
is sent once it syncs again.

### Inserts

A source can take in the lines of others, for one file put together
differently on each device. Write the pieces as files of their own in
`~/lichen/files`, and insert them with lines starting with `§`:

```
export EDITOR=vim
§ shell/aliases
§ shell/mac if mac
§ shell/work if lychee, servers
§ shell/gui unless servers
```

`~/.zshrc` gets `shell/aliases` everywhere, `shell/mac` on Macs,
`shell/work` on lychee and the servers, and `shell/gui` everywhere but
the servers. A piece can insert others, and needs no output of its own.
A line meant to start with `§` starts with `§§` in the source.

Where an insert is skipped, a blank line beside it goes too if it would
otherwise be doubled, or start or end the file.

Outputs show none of this, and editing them still syncs: lichen knows
which source each line came from, and an edited line goes back there.
New lines go to the source of the lines around them. Where an insert
begins or ends, they could belong to either, so lichen asks: it leaves
the file as it is on that device (a notification and `lichen status` say
so) until `lichen resolve` says which.

### Deleting and stopping

Deleting a synced file deletes it on every device, unless another device
edits it at the same time: then the edit wins, and the file comes back
everywhere. A file deleted by mistake comes back from the sync folder's
history, on all devices at once:

```sh
lichen files recover ~/.zshrc
```

`lichen files remove ~/.zshrc` stops syncing instead: every device keeps
its copy as a plain file. So does moving an output elsewhere, or taking a
device out of its scope.

### Edits on two devices at once

They merge, as long as they touch different lines. Edits to the same
lines are a conflict: lichen leaves the file as it is on that device and
says so (a notification, `lichen status`, the log) until you pick one.
`lichen resolve` walks through whatever waits, or settles one file:

```sh
lichen resolve ~/.zshrc --keep here      # or --keep synced
lichen resolve ~/.zshrc --into shell/mac   # where its new lines go
```

### Backups

Off by default. When a synced version replaces a device's own copy, or a
file deleted elsewhere is deleted here, the old one is gone. A directory
in the way is the exception: it may hold files that were never synced, so
it is always moved to `~/lichen-backups/`. To keep replaced and deleted
files there as well, set `backups` in `~/lichen/lichen.toml` (it syncs, so
this turns backups on for every device):

```toml
backups = true
```

## Skills

The skills module installs agent skills (`SKILL.md` directories, the
format the [Vercel skills CLI](https://github.com/vercel-labs/skills)
uses) from public repos:

```sh
lichen skills add vercel-labs/agent-skills --skill frontend-design
lichen skills add owner/single-skill-repo
```

The canonical copy lands in `~/.agents/skills/<name>` with a relative
symlink from `~/.claude/skills/<name>` (a junction on Windows), the same
layout `npx skills` creates, so Claude Code picks the skill up globally and both tools
coexist: lichen never touches a skill it didn't install. Other harnesses
get links too by listing their skill directories in the skills config:

```toml
harnesses = ["~/.claude/skills", "~/.codex/skills"]
```

A skill can limit itself to some harnesses with a comma-separated
`lichen-harnesses` under `metadata` in its `SKILL.md` frontmatter. A
harness is named after the directory holding its skills dir, so
`~/.claude/skills` is `claude` and `~/.codex/skills` is `codex`:

```yaml
---
name: my-skill
description: ...
metadata:
  lichen-harnesses: claude, codex
---
```

Some harnesses (Codex among them) read `~/.agents/skills` directly, so a
limited skill's canonical copy lives in `~/.local/share/lichen/skills`
instead, reachable only through the listed harnesses' links. List
`agents` to keep it in `~/.agents/skills`.

The list of skill repos lives in `~/lichen/skills.toml`, so a skill
installs on every device in its scope, and removing one uninstalls it
everywhere. The
daemon polls each repo about hourly, and a repo moving only reinstalls
the skills whose content actually changed. `lichen skills update` checks
immediately.

A repo's skills go where it is [scoped](#which-devices-get-what), and
adding a repo again adds the device it is added on:

```sh
lichen skills add owner/mac-skills --include mac
lichen skills exclude owner/mac-skills servers   # change it later
```

## MCP servers

The mcp module installs MCP servers into Claude Code and Codex, from a
command to launch or a URL to reach:

```sh
lichen mcp add playwright bunx @playwright/mcp@latest
lichen mcp add context7 https://mcp.context7.com/mcp
lichen mcp remove context7
lichen mcp list
```

Servers go in at user scope through each harness's own CLI (`claude mcp`
and `codex mcp`), and a harness that isn't installed on a machine is
skipped. lichen never touches a server it didn't add, so a name you
already use in a harness is left alone there. A lichen server deleted by
hand comes back on the next pass, so remove it with `lichen mcp remove`
instead. Running sessions pick up changes after a restart.

The list of servers lives in `~/lichen/mcp.toml`, so a server installs
on every device in its [scope](#which-devices-get-what), and removing
one uninstalls it everywhere. Adding a server again keeps the devices it
had:

```sh
lichen mcp add playwright --include mac bunx @playwright/mcp@latest
lichen mcp include playwright hetzner-1   # change it later
```

Servers that need API keys, headers or a login aren't supported.

## Updates

lichen updates itself: at most once a day, after a command, it installs a
newer release if one is out and restarts the daemon. It skips the check
when `CI` or `LICHEN_NO_UPDATE_CHECK` is set or stderr is not a terminal.
`lichen update` does the same right away.

The sync folder also records the newest lichen version that has synced
with it, and a device running an older build pauses syncing and updates
itself straight away, so updating one device brings the whole fleet along.

## Uninstalling

`lichen uninstall` stops the daemon and removes lichen from the device.
The sync folder and every synced file stay, the files as plain files.
