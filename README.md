# faehigkeiten

*Fähigkeiten* is German for "skills". `faehigkeiten` (short: `fgk`) is a terminal UI and CLI that installs
**agent skills** (folders containing a `SKILL.md`) from skill repositories into a project or globally,
for many coding agents at once, and keeps them up to date.

- **Browse and search** the biggest public skill repositories (Anthropic, OpenAI, Vercel, obra/superpowers, Hugging Face, …), or search all of GitHub via [skills.sh](https://skills.sh).
- **Add any repository**: `owner/repo`, any git URL (GitHub, GitLab, self-hosted, SSH) or a local directory.
- **Install per project or globally** for Claude Code, Codex, GitHub Copilot, Cursor, Gemini CLI, OpenCode, Windsurf, Goose, Kiro, Roo Code, Amp and the cross-agent `.agents/skills` convention.
- **See licenses**: the license of every skill, from its `SKILL.md`, its own license file or its repository's.
- **Track updates** per skill, by **version** (from `SKILL.md` frontmatter), by **content hash** (for skills without a version), or not at all.

## Install

**macOS / Linux (Homebrew)**

```sh
brew install Joessst-Dev/tap/faehigkeiten
```

**Windows (Scoop)**

```powershell
scoop bucket add joessst https://github.com/Joessst-Dev/scoop-bucket
scoop install faehigkeiten
```

**Linux packages**: `.deb`, `.rpm` and `.apk` files are attached to every [release](https://github.com/Joessst-Dev/faehigkeiten/releases).

**Install script (macOS / Linux)**

```sh
curl -fsSL https://raw.githubusercontent.com/Joessst-Dev/faehigkeiten/main/install.sh | sh
```

**From source**: `go install github.com/Joessst-Dev/faehigkeiten/cmd/faehigkeiten@latest`

## Usage

Run `faehigkeiten` (or `fgk`) without arguments to start the TUI:

- **Browse repositories**: pick a repository, select skills with <kbd>space</kbd>, press <kbd>enter</kbd>;
  press <kbd>v</kbd> on a skill to install an older version and <kbd>l</kbd> to read its license
- **Search skills**: type to search the known repositories; <kbd>tab</kbd> switches to skills.sh; <kbd>ctrl+l</kbd> shows the license of a result;
  <kbd>ctrl+f</kbd> marks a result as a favourite and <kbd>ctrl+o</kbd> shows only favourites
- **Add repository**: any source; it is saved and appears in the repository list
- **Installed skills**: see and remove what is installed in the current target (press <kbd>c</kbd> to change how a skill is tracked, <kbd>i</kbd> to create the lockfile if there is none yet), including skills that were copied by hand or installed with another tool (marked *not managed*; press <kbd>l</kbd> to read a skill's license, <kbd>m</kbd> to manage one, or select several with <kbd>space</kbd>/<kbd>a</kbd> and press <kbd>m</kbd> to manage or <kbd>d</kbd> to delete them together)
- **Check for updates**: select and apply available updates; press <kbd>u</kbd> on a pinned skill to unpin and update it
- **Favourite skills**: the skills you marked as favourites, from every repository; <kbd>enter</kbd> opens a skill's
  repository to install it, <kbd>d</kbd> removes it from the favourites. In the skill lists of a repository and of installed
  skills, <kbd>f</kbd> marks a skill as a favourite (★) and <kbd>F</kbd> shows only favourites
- **Change target**: switch between a project directory and the global scope

The install wizard asks for the target (project or global), the agents, and the tracking mode.

### CLI

Everything is scriptable too:

```sh
fgk init                                                   # create an empty lockfile in the project
fgk install anthropics/skills --list                     # show skills in a repository
fgk install anthropics/skills pdf docx                    # install into the current project
fgk install obra/superpowers --all -g -a claude-code,codex # install globally for two agents
fgk install ./my-skills my-skill --track hash             # hash-track an unversioned skill
fgk install anthropics/skills pdf --versions              # versions in the history of a skill
fgk install anthropics/skills pdf@1.2.0                   # install (and pin) an older version
fgk list                                                   # installed skills
fgk license pdf                                            # license of an installed skill
fgk license anthropics/skills pdf                          # …or of a skill in a repository
fgk check --exit-code                                      # exit 10 if updates are available, 1 on errors (CI)
fgk update                                                 # apply all available updates
fgk update --force                                         # …including skills you edited locally
fgk update pdf --unpin                                     # unpin a skill and update it
fgk remove pdf
fgk track hash pdf                                         # switch a skill to hash tracking
fgk track version --all                                    # …or every installed skill that has a version
fgk adopt golang-cli                                       # manage a skill installed by hand or another tool
fgk adopt my-skill --from my-org/skills --track hash       # …from an explicit source
fgk adopt --all                                            # manage every unmanaged skill in the target
fgk search golang                                          # search known repositories
fgk search --remote terraform                              # search skills.sh
fgk fav add anthropics/skills pdf docx                     # mark skills as favourites
fgk fav ls                                                 # list favourites
fgk fav rm anthropics/skills docx
fgk search --favourites pdf                                # only favourites (also: list, install --list)
fgk repo add my-org/skills --ref main
fgk agents                                                 # supported agents and their directories
```

Project-scope commands use the git root of the working directory by default; pass `-p <dir>` for another project or `-g` for the global scope.

## Update tracking

Each installed skill gets an entry in a lockfile:

| Scope   | Lockfile |
|---------|----------|
| project | `<project>/.faehigkeiten.lock.yaml` (commit it with your project) |
| global  | `<user config dir>/faehigkeiten/global.lock.yaml` |

```yaml
version: 1
skills:
  - name: pdf
    source: https://github.com/anthropics/skills
    path: skills/pdf
    ref: main
    commit: 683bc88e56f3e09ba94f7055977f3d3aa499f202
    agents: [claude-code]
    tracking: hash
    hash: sha256:6a01b6dc757b8856d7eba5e9985d8550783f946d7afdf86402ba324b93239fc0
    license: Apache-2.0
    license_file: LICENSE
    installed_at: 2026-10-08T18:52:16Z
```

| Mode      | When                     | Update check |
|-----------|--------------------------|--------------|
| `version` | skill declares `version` (or `metadata.version`) in its frontmatter | semantic version of upstream is newer |
| `hash`    | opt-in, mainly for skills without a version | SHA-256 over all files (paths + contents) of the upstream skill differs |
| `none`    | untracked                | never checked |

`fgk init` creates an empty lockfile so it can be committed before any skill is installed. Once a
lockfile exists, it is kept even when its last skill is removed.

### Changing the tracking mode

`fgk track <version|hash|none> <skill...>` (or `--all`, or <kbd>c</kbd> on the TUI's installed skills
screen) changes how installed skills are tracked **without reinstalling them**. The recorded hash of
the installed content is kept, so local edits are still detected, and switching to `hash` keeps the
recorded version, so `version` tracking can be restored later; otherwise the version is read from the
installed `SKILL.md`. If the installed copy has no version, the source is fetched: when upstream
declares one, the skill is tracked from `0.0.0` (as when adopting such a copy), so the upstream version
is offered as an update. Switching an untracked skill to `version` or `hash` takes its current installed
content as the baseline. Pinned skills always use version tracking; unpin them first. With named
skills, nothing changes unless every one of them can switch; with `--all`, skills that cannot switch
(for example to `version` when neither the installed copy nor upstream has one) are skipped and
reported, the others are saved, and the command exits with status 1. The TUI shows modes a skill cannot
switch to greyed out, with the reason.

The lockfile also stores a hash of the installed content of every tracked skill. If you edit an
installed skill, `check` and the TUI flag it as *locally modified*: `fgk update` skips it with a
warning unless you pass `--force`, and the TUI shows a warning and does not preselect it.

### Installing a specific version

The versions of a skill are the values of `version` in the git history of its `SKILL.md`. Each
version maps to the newest commit that still declared it, so fixes made without a version bump are
included. `fgk install <source> <skill> --versions` lists them; `<skill>@<version>` installs one.

Installs at an explicit version are **pinned** (`pinned: true` in the lockfile, with the commit of
that version), even when it is the newest one. `fgk check` still shows when a newer version exists,
but does not count it as an update, and `fgk update` skips pinned skills. Installing a pinned skill
again without a version (from the same ref) keeps it at its pinned version.

Unpin with `fgk update <skill> --unpin`, by installing `<skill>@latest`, by picking *latest
(unpinned)* in the TUI's version list, or with <kbd>u</kbd> on the TUI's update screen.
Local changes are protected as usual: `--unpin` skips modified skills unless you add `--force`, and
the TUI asks before overwriting them.

Listing versions needs the full history of the repository, so faehigkeiten keeps a separate full
clone in its cache for it (and falls back to it when offline); normal installs keep using shallow
clones. Only the skill's own directory is extracted for each pinned version.

### Managing existing skills

Skills that were copied by hand or installed with another tool show up as *not managed*. Adopting
one records it in the lockfile **without changing its files**:

- faehigkeiten looks for a skill with the same name in the known repositories and prefers a source
  whose content is identical to your copy; you can also name the repository yourself.
- The lockfile stores your local version (or `0.0.0` if it has none) or the hash of your local copy,
  so if your copy differs from upstream, the next update check offers the upstream version.
- Skills whose source is unknown can be adopted as *local only*: they are managed (listed and removable
  for all agents) but never checked for updates.
- Several skills can be adopted at once (`fgk adopt --all`, or multi-select in the TUI). A source is
  then only chosen automatically when its content is identical to your copy, so an unrelated skill
  with the same name never overwrites yours on the next update. The TUI lets you pick sources for the
  rest one by one or keep them local only; the CLI skips them and prints the command to adopt each.
- Without `--track`, versioned skills track their version and the others a content hash.

## Licenses

The license of a skill is taken from, in order:

1. the `license` field of its `SKILL.md`, when it names a license (an SPDX identifier such as `MIT`
   or an expression such as `MIT OR Apache-2.0`),
2. a `LICENSE`, `LICENSE.txt`, `LICENSE.md` or `COPYING` file in the skill directory,
3. the same files at the root of its repository.

Well-known license texts (MIT, Apache-2.0, BSD, ISC, MPL, GPL, LGPL, AGPL, Unlicense, CC0, CC-BY) are
recognised and shown by their SPDX identifier; other license files are shown as *custom*. When the
`license` field is a sentence such as `Complete terms in LICENSE.txt` or a file name such as
`LICENSE-MIT`, the file it refers to decides. A license file of another well-known license than the
one the field names is ignored.
Lists show the license in a `LICENSE` column (CLI) or next to the description (TUI); `fgk license`
and <kbd>l</kbd> in the TUI print the full text. The license is recorded in the lockfile at install
time (`no_license: true` for skills without one). Searching for a license name such as `mit` or
`apache` also finds the skills under that license.

## Configuration

`<user config dir>/faehigkeiten/config.yaml` (e.g. `~/.config/faehigkeiten/config.yaml` on Linux,
`~/Library/Application Support/faehigkeiten/config.yaml` on macOS). Edit it by hand or under
**Settings** in the TUI:

```yaml
repos:                       # your own repositories (added via TUI or `fgk repo add`)
  - name: my-org/skills
    url: https://github.com/my-org/skills
    ref: main
default_agents: [claude-code, codex]   # preselected agents (default: detected agents)
agents:                      # override or add agent directories
  my-agent:
    name: My Agent
    project_dir: .my-agent/skills
    global_dir: .my-agent/skills       # relative to your home directory, or absolute
favourites:                  # skills marked with f in the TUI or `fgk fav add`
  - name: pdf
    url: https://github.com/anthropics/skills
    path: skills/pdf
```

Repositories are cloned shallowly into the user cache directory. `FAEHIGKEITEN_CONFIG_DIR` and
`FAEHIGKEITEN_CACHE_DIR` override these locations.

## Development

```sh
go run ./cmd/faehigkeiten               # start the TUI
go run github.com/onsi/ginkgo/v2/ginkgo -r   # run the Ginkgo test suites
goreleaser release --snapshot --clean   # build all release artifacts locally
```

Releases are built by GoReleaser when a `v*` tag is pushed. The workflow needs a `TAP_GITHUB_TOKEN`
secret with write access to `Joessst-Dev/homebrew-tap` and `Joessst-Dev/scoop-bucket`.

## License

[MIT](LICENSE) © 2026 Joessst-Dev
