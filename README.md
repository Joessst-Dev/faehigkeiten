# faehigkeiten

*Fähigkeiten* is German for "skills". `faehigkeiten` (short: `fgk`) is a terminal UI and CLI that installs
**agent skills** (folders containing a `SKILL.md`) from skill repositories into a project or globally,
for many coding agents at once, and keeps them up to date.

- **Browse and search** the biggest public skill repositories (Anthropic, OpenAI, Vercel, obra/superpowers, Hugging Face, …), or search all of GitHub via [skills.sh](https://skills.sh).
- **Add any repository**: `owner/repo`, any git URL (GitHub, GitLab, self-hosted, SSH) or a local directory.
- **Install per project or globally** for Claude Code, Codex, GitHub Copilot, Cursor, Gemini CLI, OpenCode, Windsurf, Goose, Kiro, Roo Code, Amp and the cross-agent `.agents/skills` convention.
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

- **Browse repositories**: pick a repository, select skills with <kbd>space</kbd>, press <kbd>enter</kbd>
- **Search skills**: type to search the known repositories; <kbd>tab</kbd> switches to skills.sh
- **Add repository**: any source; it is saved and appears in the repository list
- **Installed skills**: see and remove what is installed in the current target, including skills that were copied by hand or installed with another tool (marked *not managed*; press <kbd>m</kbd> to manage them)
- **Check for updates**: select and apply available updates
- **Change target**: switch between a project directory and the global scope

The install wizard asks for the target (project or global), the agents, and the tracking mode.

### CLI

Everything is scriptable too:

```sh
fgk install anthropics/skills --list                     # show skills in a repository
fgk install anthropics/skills pdf docx                    # install into the current project
fgk install obra/superpowers --all -g -a claude-code,codex # install globally for two agents
fgk install ./my-skills my-skill --track hash             # hash-track an unversioned skill
fgk list                                                   # installed skills
fgk check --exit-code                                      # exit 10 if updates are available, 1 on errors (CI)
fgk update                                                 # apply all available updates
fgk update --force                                         # …including skills you edited locally
fgk remove pdf
fgk adopt golang-cli                                       # manage a skill installed by hand or another tool
fgk adopt my-skill --from my-org/skills --track hash       # …from an explicit source
fgk search golang                                          # search known repositories
fgk search --remote terraform                              # search skills.sh
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
    installed_at: 2026-10-08T18:52:16Z
```

| Mode      | When                     | Update check |
|-----------|--------------------------|--------------|
| `version` | skill declares `version` (or `metadata.version`) in its frontmatter | semantic version of upstream is newer |
| `hash`    | opt-in, mainly for skills without a version | SHA-256 over all files (paths + contents) of the upstream skill differs |
| `none`    | untracked                | never checked |

The lockfile also stores a hash of the installed content of every tracked skill. If you edit an
installed skill, `check` and the TUI flag it as *locally modified*: `fgk update` skips it with a
warning unless you pass `--force`, and the TUI shows a warning and does not preselect it.

### Managing existing skills

Skills that were copied by hand or installed with another tool show up as *not managed*. Adopting
one records it in the lockfile **without changing its files**:

- faehigkeiten looks for a skill with the same name in the known repositories and prefers a source
  whose content is identical to your copy; you can also name the repository yourself.
- The lockfile stores your local version (or `0.0.0` if it has none) or the hash of your local copy,
  so if your copy differs from upstream, the next update check offers the upstream version.
- Skills whose source is unknown can be adopted as *local only*: they are managed (listed and removable
  for all agents) but never checked for updates.

## Configuration

`<user config dir>/faehigkeiten/config.yaml` (e.g. `~/.config/faehigkeiten/config.yaml` on Linux,
`~/Library/Application Support/faehigkeiten/config.yaml` on macOS):

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
