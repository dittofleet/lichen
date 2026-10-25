<img src="assets/icon.svg" width="80" alt="lichen icon">

# lichen

A project for easily keeping dev environments in sync across your Macs
and Linux machines.

It syncs things, one module per kind of thing:

- **files**: dotfiles, slash commands, `CLAUDE.md`, app configs, anything
  under your home directory, written wherever each device needs it.
- **skills**: agent skills installed from public repos, kept up to date
  by polling.
- **mcp**: MCP servers, installed into Claude Code and Codex.

lichen is a pun on 'liken' and just how lichen behaves in general.

## Setup

Prereqs on each device: git access that can clone the sync repo (SSH key
or credentials, since the repo should be private), and
[crosstalk](https://github.com/dittofleet/crosstalk) joined to your hub.
lichen nudges your other devices through crosstalk after each push, so
they pull within seconds, and names each device the way crosstalk does.
Without it, and for pushes made outside lichen, devices catch up on an
hourly pass.

Create an empty private repo once (the sync repo, e.g. `you/lichen-sync`),
then on every device:

```sh
curl -fsSL https://raw.githubusercontent.com/dittofleet/.github/main/install.sh | sh -s lichen
```

It asks for the sync repo's URL (or takes it from `LICHEN_REPO`), clones
it to `~/lichen`, and starts the daemon: a launchd agent on macOS, a
systemd user service on Linux. `lichen setup <git url>` does the same
later, if you skip the question.

## The sync folder

`~/lichen` holds everything lichen syncs, as plain files:

```
~/lichen/
  lichen.json   device groups and settings
  files.json    where each file goes
  skills.json
  mcp.json
  files/        the files themselves
```

Edit anything in it and lichen commits and pushes it. You never run git
there yourself, but its history is all there.

## Files

```sh
lichen files add ~/.zshrc ~/.claude/CLAUDE.md
```

Each path becomes a source in `~/lichen/files` (`zshrc`,
`claude/CLAUDE.md`: leading dots are dropped), with an output where it
came from. Every device writes the outputs meant for it, and editing
either side syncs: the output (`~/.zshrc`) or the source
(`~/lichen/files/zshrc`). A directory syncs as a whole, including files
added to it later.

`lichen files` shows every source and where it goes.

### Which devices, and where

An output can be limited to some devices: by name, by OS (`mac`,
`linux`), or by a group of your own.

```sh
lichen files add ~/.zshrc --only mac
lichen groups add servers hetzner-1 hetzner-2
lichen files only ~/.zshrc mac servers   # change it later
lichen devices                           # names and groups
```

One source can go to several places, and each place can have its own
devices:

```sh
lichen files add ~/.codex/AGENTS.md --to claude/CLAUDE.md
lichen files add ~/.config/agents.md --to claude/CLAUDE.md --only linux
```

Edit any of them and the others follow. When a device joins with its own
copy of a file that is already synced, the synced version replaces it.

### Deleting and stopping

Deleting a synced file deletes it on every device. A file deleted by
mistake comes back from the sync folder's history, on all devices at
once:

```sh
lichen files recover ~/.zshrc
```

`lichen files remove ~/.zshrc` stops syncing instead: every device keeps
its copy as a plain file. So does moving an output elsewhere, or taking a
device out of its scope.

### Edits on two devices at once

They merge, as long as they touch different lines. Edits to the same
lines are a conflict: lichen leaves the file as it is on that device and
says so in `lichen status` and the log, until you pick one:

```sh
lichen files resolve ~/.zshrc --keep here     # or --keep synced
```

### Backups

Off by default. When a synced version replaces a device's own copy, or a
file deleted elsewhere is deleted here, the old one is gone. A directory
in the way is the exception: it may hold files that were never synced, so
it is always moved to `~/lichen-backups/`. To keep replaced and deleted
files there as well, set `backups` in `~/lichen/lichen.json` (it syncs, so
this turns backups on for every device):

```json
{ "backups": true }
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
symlink from `~/.claude/skills/<name>`, the same layout `npx skills`
creates, so Claude Code picks the skill up globally and both tools
coexist: lichen never touches a skill it didn't install. Other harnesses
get links too by listing their skill directories in the skills config:

```json
{ "harnesses": ["~/.claude/skills", "~/.codex/skills"] }
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

The list of skill repos lives in `~/lichen/skills.json`, so adding a skill
on one device installs it everywhere, and removing one uninstalls it
everywhere. The
daemon polls each repo about hourly, and a repo moving only reinstalls
the skills whose content actually changed. `lichen skills update` checks
immediately.

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

The list of servers lives in `~/lichen/mcp.json`, so adding a server on
one device installs it everywhere, and removing one uninstalls it
everywhere.
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
