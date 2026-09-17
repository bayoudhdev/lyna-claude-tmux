# Sandbox and isolation

Every Claude launch `lyna-tmux` starts carries two decisions:

- a **sandbox profile** (`standard`, `strict`, `off`): how tightly Bash commands are confined, which credentials are hidden and which hosts may be reached;
- an **isolation level** (`bash`, `process`, `container`): what runs inside the boundary.

Both come from the configuration (`sandbox.profile`, `sandbox.isolation`) and can be overridden per launch (`--sandbox`, `--isolation`). See [configuration.md](configuration.md) for the `[sandbox]` table and [keys.md](keys.md) for the bindings and menus that open the sandbox views.

The resolved profile and level are written into a per-launch settings file that `claude` receives with `--settings`. That delivery is deliberate: `network.strictAllowlist` is honored only from user, managed and `--settings` sources, so the fragments cannot travel in a repository file. The file is content-addressed by the SHA-256 of its own bytes, written atomically with mode 0600 into a directory with mode 0700, and identical launches share one file.

Inspect any of it without launching anything:

```sh
lmux sandbox profiles                      # what each profile and level protects
lmux sandbox show                          # the exact settings file, byte for byte
lmux sandbox show strict --dir ~/src/api
lmux sandbox show --isolation process | jq .sandbox
lmux sandbox status --json                 # the sandbox in effect, plus readiness
```

`sandbox status` reports the profile of the workspace you are in when run from one of its panes or popups, and the configured profile otherwise. It is also the "Sandbox status" entry (key `b`) of the status line session menu.

## What the sandbox protects, and what it does not

Four assets are in view.

**Credentials.** Twelve credential paths and twenty-two token environment variables are denied to sandboxed commands in every enabled profile. Only the `deny` mode is ever written: the `mask` mode would make the sandbox proxy forward the real secret to allowed hosts and needs TLS termination, which is the user's decision, not a default. Claude Code has no built-in credential list, so only the listed paths are protected; add your own with `sandbox.deny_read`.

**Source code and the rest of your files.** The project directory is the working area. In `strict`, file tools cannot read outside the working directories at all, and `.env` files are denied at any depth. In `standard`, `.env` reads raise a prompt. Writes outside the project are limited to what `sandbox.allow_write` names, plus, at process isolation, the paths the Claude process itself needs.

**Transcripts and the Claude configuration.** Claude keeps its transcripts and state in its configuration directory (`CLAUDE_CONFIG_DIR`, else `~/.claude`) and in `~/.claude.json`. At process isolation both are listed as writable for the confined process and nothing else outside the project is. In a dev container they live in a per-project named volume, so a container never sees the host's Claude directory.

**The tmux socket.** The workspace runs on a dedicated tmux server whose socket the hooks and the status line reach through `LYNA_TMUX_SOCKET`. A process that can talk to that socket can drive the workspace. At process isolation it is passed as the only entry of `network.allowUnixSockets`, so it is the one socket the confined process may connect to.

What the sandbox does not do:

- It does not judge what an allowed host serves. An allowlist bounds destinations by hostname, not content.
- At `bash` isolation it confines Bash commands only. File tools, hooks and MCP servers run outside that boundary; what limits them is the `permissions` block, not the sandbox.
- In `standard` it does not scrub subprocess environments, so an MCP server that reads a token from the environment still sees it.
- It passes `sandbox.excluded_commands` through to Claude Code's `sandbox.excludedCommands` unchanged in both enabled profiles, so that list is part of what you are auditing when you audit a profile.
- It never changes your own tmux configuration or your own Claude settings, and it never installs anything.

## Profiles

| | `standard` (default) | `strict` | `off` |
| --- | --- | --- | --- |
| Bash sandbox | on, and Bash refuses to run when it cannot start | on, and Bash refuses to run when it cannot start | off |
| Credential files | 12 paths denied | 12 paths denied | none |
| Token variables | 22 names unset for sandboxed commands | 22 names unset for sandboxed commands | none |
| Subprocess environment | inherited | credentials scrubbed from every subprocess | inherited |
| `.env` files | ask before reading | never read | readable |
| Reads outside the project | allowed | blocked outside the working directories | allowed |
| Network | allowed hosts pass, other hosts ask | only allowed hosts | open |
| Default allowed hosts | none | git hosting plus the detected registries | none |
| Blocked Bash command | may run outside the sandbox after a prompt | fails, never retried outside | not applicable |
| `bypassPermissions` | refused | allowed | refused |
| Chosen by | default, `--sandbox standard`, `sandbox.profile` | `--sandbox strict`, `sandbox.profile` | `--sandbox off` only |

The credential deny list is identical in `standard` and `strict`:

```text
~/.ssh                 ~/.aws                 ~/.gnupg               ~/.config/gh
~/.netrc               ~/.docker/config.json  ~/.kube                ~/.npmrc
~/.pypirc              ~/.config/gcloud       ~/.azure               ~/.git-credentials
```

```text
ANTHROPIC_API_KEY      AWS_ACCESS_KEY_ID      AWS_SECRET_ACCESS_KEY  AWS_SESSION_TOKEN
AZURE_CLIENT_SECRET    CARGO_REGISTRY_TOKEN   CLOUDFLARE_API_TOKEN   DIGITALOCEAN_ACCESS_TOKEN
DOCKER_PASSWORD        GH_TOKEN               GITHUB_TOKEN           GITLAB_TOKEN
GOOGLE_API_KEY         HF_TOKEN               NODE_AUTH_TOKEN        NPM_TOKEN
OPENAI_API_KEY         PYPI_API_TOKEN         SLACK_BOT_TOKEN        STRIPE_SECRET_KEY
TWINE_PASSWORD         VAULT_TOKEN
```

Each path is written as `{"path": "...", "mode": "deny"}` and each name as `{"name": "...", "mode": "deny"}`.

### standard

The default. It sandboxes Bash, hides the credentials above and keeps the unsandboxed retry behind a permission prompt.

```json
"sandbox": {
  "enabled": true,
  "failIfUnavailable": true,
  "autoAllowBashIfSandboxed": true,
  "credentials": { "files": [...], "envVars": [...] }
},
"permissions": {
  "ask": ["Read(.env)", "Read(.env.*)"],
  "disableBypassPermissionsMode": "disable"
}
```

- **Credentials.** The twelve paths are read-denied and the twenty-two variables are unset for sandboxed commands.
- **Environment.** Subprocess environments are left alone, so MCP servers that read tokens from the environment keep working. The token variables are still unset for sandboxed commands through `credentials.envVars`.
- **`.env` handling.** `Read(.env)` and `Read(.env.*)` are `ask` rules. Bare file names follow gitignore semantics in Read rules, so they match `.env` files at any depth under the working directory, and Claude asks before a file tool reads one.
- **Network.** No allowlist of its own. Allowed hosts pass and any other host raises a prompt. A `sandbox.network` object appears only when `sandbox.allowed_domains` adds hosts.
- **A blocked command.** The sandbox blocks it; Claude may then retry the same command outside the sandbox after a permission prompt. Sandboxed commands themselves run without a prompt (`autoAllowBashIfSandboxed`).
- **If the sandbox cannot start.** `failIfUnavailable` is true, so Bash refuses to run rather than running unconfined.
- **`bypassPermissions`.** Refused. `disableBypassPermissionsMode: "disable"` is written so Claude itself rejects the flag and never cycles into bypass mode during the session.

### strict

Adds a deny-by-default network allowlist, removes the unsandboxed retry, blocks reads outside the working directories and scrubs credentials from every subprocess.

```json
"sandbox": {
  "enabled": true,
  "failIfUnavailable": true,
  "autoAllowBashIfSandboxed": false,
  "allowUnsandboxedCommands": false,
  "filesystem": { "allowRead": ["~/.gitconfig", "~/.config/git"] },
  "network": { "allowedDomains": [...], "strictAllowlist": true },
  "credentials": { "files": [...], "envVars": [...] }
},
"permissions": {
  "deny": ["Read(.env)", "Read(.env.*)"],
  "blockReadsOutsideWorkingDirectories": true
},
"env": { "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "1" }
```

- **Credentials.** The same deny list, plus `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1`, which strips credentials from the environment of every subprocess Claude starts: Bash, hooks and MCP stdio servers.
- **Reads.** `blockReadsOutsideWorkingDirectories` stops file tools outside the working directories. `~/.gitconfig` and `~/.config/git` are re-opened with `filesystem.allowRead`, or sandboxed git would lose its commit identity and aliases. The credential stores stay denied: a deny entry holds inside a wider allow.
- **`.env` handling.** The same two rules, as `deny`: `.env` files are never read.
- **Network.** `strictAllowlist` is true, so only the listed hosts are reachable. The list is the five git hosting hosts, then the registries of the ecosystems detected in the project, then `sandbox.allowed_domains`:

  ```text
  github.com  api.github.com  codeload.github.com  objects.githubusercontent.com  raw.githubusercontent.com
  ```

- **A blocked command.** It fails. `allowUnsandboxedCommands` is false, so nothing ever runs outside the boundary and there is no prompt to accept.
- **Auto-allow.** `autoAllowBashIfSandboxed` is written as `false` because the subprocess scrub turns auto-allow off anyway; the settings say what actually happens instead of claiming otherwise.
- **`bypassPermissions`.** Allowed. `disableBypassPermissionsMode` is not written.

### off

No sandbox at all:

```json
"sandbox": { "enabled": false },
"permissions": { "disableBypassPermissionsMode": "disable" }
```

Nothing else is produced: no credential denies, no `.env` rules, no allowlist, open egress, and every Bash command runs unconfined.

**Why it needs an explicit flag.** A profile that protects nothing must be a decision made at the moment of the launch, not a line in a file that some earlier session left behind. So `off` is honored only as a command-line value. With `sandbox.profile = "off"` in the configuration and no flag, every launch path refuses:

```text
sandbox.profile = "off" in the configuration is not honored: pass --sandbox off to launch without a sandbox
```

The same refusal guards `sandbox show`, `sandbox status` and container launches, and the dashboard create form starts from `standard` when the configuration says `off`.

Two further restrictions:

- `off` still refuses `bypassPermissions`: the bypass rule asks for `strict` or a container, and `off` is neither.
- `off` cannot be combined with process isolation. Resolving that pair fails with `the off profile runs Claude without a sandbox, so it cannot be combined with process isolation`.

`lmux doctor` skips the platform sandbox checks entirely when the configured profile is `off`, reporting `sandbox.profile = "off"`.

## Ecosystem detection

The project root is the nearest ancestor directory that holds a `.git` entry, or the starting directory when there is none. Each marker file is looked up there, in this order, and every match adds its registry hosts:

| Ecosystem | Marker files at the project root | Hosts added in `strict` |
| --- | --- | --- |
| `node` | `package.json` | `registry.npmjs.org`, `registry.yarnpkg.com` |
| `go` | `go.mod` | `proxy.golang.org`, `sum.golang.org` |
| `rust` | `Cargo.toml` | `crates.io`, `index.crates.io`, `static.crates.io` |
| `python` | `pyproject.toml`, `requirements.txt` | `pypi.org`, `files.pythonhosted.org` |
| `ruby` | `Gemfile` | `rubygems.org`, `index.rubygems.org` |

The entries are exact hosts, never wildcards: the sandbox proxy decides from the requested hostname, and every extra host widens what a command can reach. Detection order is fixed so generated settings are stable across runs.

Detection runs for every launch, but the result is used only where an allowlist exists: the `strict` profile at `bash` and `container` isolation, and both enabled profiles at `process` isolation. In `standard` at `bash` isolation the detected registries are not written anywhere, because `standard` has no allowlist to write them into.

## Isolation levels

`--isolation` and `sandbox.isolation` accept exactly three values.

| Level | Boundary | Needs | `bypassPermissions` |
| --- | --- | --- | --- |
| `bash` (default) | Bash commands, in the Claude Code sandbox | the platform sandbox | only with `strict` |
| `process` | the whole Claude process, in the sandbox runtime | `srt` on `PATH` | only with `strict` |
| `container` | the whole workspace, in the dev container | `docker`, a built image, a running container | always |

### bash (default)

**What is isolated.** Bash tool commands, by Claude Code's own sandbox. Everything else the session does, file tools, hooks, MCP servers, the status line, runs as your user.

**Requirements.** On macOS, `sandbox-exec` (shipped in `/usr/bin`). On Linux and WSL2, `bwrap` and `socat`, plus unprivileged user namespaces. `lmux doctor` reports each one.

**How to turn it on.** It is the default. `--isolation bash`, or `isolation = "bash"` in the `[sandbox]` table.

**Limits.** The profile's file, credential and network rules apply to Bash. A file tool read is governed by the `permissions` block instead (`.env` ask or deny, `blockReadsOutsideWorkingDirectories`), and a hook or an MCP server you configured is not confined at all. If you want one boundary around all of it, use `process` or `container`.

### process

**What is isolated.** The whole `claude` process, wrapped in the sandbox runtime. The pane command becomes:

```sh
srt --settings <runtime-settings.json> -- <claude argv>
```

The `--` is load-bearing: `srt` parses options anywhere on its command line, so Claude's own `--settings=<file>` would otherwise replace the runtime's settings file.

**How to turn it on.** `--isolation process`, or `isolation = "process"` in the `[sandbox]` table.

**Caveats, stated plainly.**

- **The runtime must already be installed, and it is never installed for you.** `lyna-tmux` only looks for it and tells you the command. Without it a launch fails with `process isolation runs Claude inside the sandbox runtime, and srt is not on PATH; install it with npm install -g @anthropic-ai/sandbox-runtime`, and `doctor` prints the same install command with `(needs Node.js 20.11 or newer)`. `doctor` never runs it.
- **A relative `PATH` hit is refused.** Only an absolute path to `srt` is accepted, since a checkout could plant an executable of that name.
- **Claude's own Bash sandbox is turned off at this level.** A Seatbelt profile cannot be applied inside another deny-default one (`sandbox_apply` fails with `EPERM`), so the generated Claude settings contain `"sandbox": {"enabled": false}` and the protections move into the runtime policy.
- **Egress is deny by default for both profiles.** The runtime cannot ask before reaching a new host the way the `standard` Bash sandbox does, so `standard` gets the same allowlist as `strict` at this level.
- **The token variables stay visible to Claude itself.** They have to be, so `credentials.envVars` is not what protects them here: `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` is set for both profiles, which strips them from every subprocess instead.
- **`off` plus `process` is refused** (see the `off` section).

**What the generated settings contain.** Two files, both written before the pane starts.

The Claude settings file, handed over with `--settings`:

```json
{
  "env": { "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "1" },
  "permissions": {
    "ask": ["Read(.env)", "Read(.env.*)"],
    "disableBypassPermissionsMode": "disable"
  },
  "sandbox": { "enabled": false }
}
```

(`strict` turns the `ask` list into `deny` and adds `blockReadsOutsideWorkingDirectories`; it also drops `disableBypassPermissionsMode`, since bypass is allowed there.)

The runtime settings file, written into the private per-launch settings directory and passed to `srt`:

```json
{
  "network": {
    "allowedDomains": [
      "api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com", "downloads.claude.ai",
      "github.com", "api.github.com", "codeload.github.com",
      "objects.githubusercontent.com", "raw.githubusercontent.com",
      "<detected registries>", "<sandbox.allowed_domains>"
    ],
    "deniedDomains": [],
    "allowUnixSockets": ["<the lyna-tmux tmux server socket>"]
  },
  "filesystem": {
    "denyRead": ["<the 12 credential paths>", "<sandbox.deny_read>"],
    "allowWrite": [
      "<project root>", "<Claude configuration directory>", "<home>/.claude.json",
      "<lyna-tmux state directory>", "<sandbox.allow_write>"
    ],
    "denyWrite": []
  }
}
```

Reads are allowed except `denyRead`; writes are denied except `allowWrite`; egress is denied except `allowedDomains`. The five Claude hosts cover the API, sign-in, OAuth token exchange and native updates. The writable list is exactly what the process needs: the project, the Claude configuration directory and the `~/.claude.json` file Claude keeps next to it, and the `lyna-tmux` state directory the hooks log to. The socket entry is the tmux server the hooks and the status line talk to.

`srt` reads that file only when it starts, so editing it mid-session changes nothing; the file is pruned with the other stale per-launch settings.

**Platform limits.**

- `allowUnixSockets` is honored on macOS. On Linux the runtime blocks every Unix socket, including the one listed there.
- On Linux the runtime also needs `ripgrep` to find the paths it must protect. `doctor` adds a `ripgrep` row at this level on Linux.

### container

**What is isolated.** The whole workspace: tmux, Claude, its hooks, its MCP servers and every shell you open in it, inside a dev container. The container is the boundary, so what escapes a Bash command is still inside it.

**Requirements.** The rendered `.devcontainer` files in the project, `docker` on `PATH`, a reachable daemon, and a running container.

**How to turn it on.**

```sh
lmux sandbox devcontainer init    # write the files
lmux sandbox devcontainer up      # build, start, firewall
lmux create --isolation container # open the workspace inside it
```

or `isolation = "container"` in the `[sandbox]` table, which makes `create` and `team` take the container path with no flag.

**Limits.** The generated image ships no `bwrap` and no `socat`, so a Bash sandbox cannot start inside it. `failIfUnavailable` is therefore left out of the container settings: a missing Bash sandbox must not stop Bash. The credential denies are still written, for images that do add the sandbox prerequisites. In practice the container, not the Bash sandbox, is what confines the session, and `bypassPermissions` is allowed for exactly that reason.

A launch prepared at `container` isolation on a host that is not itself inside a container fails with:

```text
isolation level unavailable: container isolation runs the workspace inside the project's dev container;
start it with `lmux sandbox devcontainer up`, then run `lmux create --isolation container`
```

### Boundaries lyna-tmux does not manage

`--isolation` accepts `bash`, `process` and `container`, and nothing else. Stronger boundaries, a dedicated virtual machine or a separate machine you own and reach remotely, are outside what this CLI creates, starts, checks or tears down: there is no isolation value for them and `doctor` has no readiness check for them. They remain the right answer when the container boundary is not enough, but you provision and maintain them yourself. Inside such a machine `lyna-tmux` behaves as it does anywhere else: pick a profile and one of the three levels there.

## The container path, end to end

### `sandbox devcontainer init`

Writes into `.devcontainer/` of the directory it is given, or of the current directory. It never
walks up to the enclosing repository: a directory that is not a repository of its own would
otherwise get its ancestor's `.devcontainer`, a different project entirely. The absolute target is
printed before anything is written, and a target away from the current directory has to be named
on the command line.

Existing files are kept unless `--force` is given; nothing is written if any target exists, and any symbolic link in the path is refused, so a refused write never leaves a half-updated directory. Scripts get mode 0755, the staged binary 0755, other files 0644.

**Where the image gets `lyna-tmux`.** The image needs a linux binary, and until a release exists
there is nothing to download, so `init` decides where it comes from and records that decision in
the `Dockerfile`:

1. the running executable, when this machine is linux and the binary matches the image
   architecture;
2. a build from the checkout, when the `go` toolchain is on `PATH` and the target is inside this
   repository;
3. the release that matches the running version, when that version is a released `vX.Y.Z`;
4. otherwise `init` stops and names both ways to say it outright: `--binary <path>` stages a
   `linux/<arch>` binary you built yourself, and `--version vX.Y.Z` pins the installer to a
   published release. The two are mutually exclusive.

A staged binary is copied into `.devcontainer/lyna-tmux`, checked against its own digest inside the
image, installed root-owned at 0755, and added to `.devcontainer/.gitignore` so it is never
committed. `up` and `shell` read the `Dockerfile` back first: a staged binary that is gone, or that
no longer matches the digest, fails before `docker` runs at all, with the command that stages it
again.

**`devcontainer.json`**

```json
{
  "name": "lyna-tmux <project>",
  "build": { "dockerfile": "Dockerfile", "context": "." },
  "runArgs": ["--cap-add=NET_ADMIN", "--init"],
  "containerUser": "lyna",
  "remoteUser": "lyna",
  "workspaceMount": "source=${localWorkspaceFolder},target=/workspace,type=bind",
  "workspaceFolder": "/workspace",
  "mounts": ["source=lyna-tmux-claude-<project>,target=/home/lyna/.claude,type=volume"],
  "containerEnv": { "CLAUDE_CONFIG_DIR": "/home/lyna/.claude" },
  "postStartCommand": "sudo /usr/local/bin/init-firewall.sh",
  "waitFor": "postStartCommand"
}
```

`NET_ADMIN` is added so root can program `iptables` and `ipset`; the container user holds no capabilities and reaches the firewall only through `sudo`. The project name is derived from the directory: lower-cased, runs of other characters folded into single hyphens, trimmed to 40 characters. The file follows the Dev Containers specification, so the same directory works with any editor that supports it.

**`Dockerfile`** builds from `debian:13-slim` and:

- installs `bash`, `ca-certificates`, `curl`, `git`, `iptables`, `ipset`, `less`, `neovim`, `procps`, `sudo`, `tmux`, `tzdata`;
- creates the non-root account (default `lyna`, uid and gid 1000) with `/home/<user>/.claude` at mode 0700 and `/workspace` at 0755; the name `root` is refused outright;
- copies the firewall script to `/usr/local/bin/init-firewall.sh` and the allowlist to `/usr/local/etc/lyna-tmux/allowed-domains`, both owned by `root` (0755 and 0644), so the container user cannot widen the allowlist;
- installs a sudoers rule allowing exactly that one command with no arguments, and validates it with `visudo -cf`;
- installs `lyna-tmux` either by copying the staged binary and checking its sha256, or from the release that `init` pinned through that tag's `scripts/install.sh`, verified against the release checksums; then drops to the non-root user and installs Claude Code with its native installer;
- ends at `WORKDIR /workspace` with `CMD ["sleep", "infinity"]`.

**`init-firewall.sh`** is the egress firewall described below.

**`allowed-domains`** is the allowlist, one host per line, with two header comments. Default content, plus whatever `sandbox.allowed_domains` adds:

```text
api.anthropic.com
claude.ai
claude.com
platform.claude.com
downloads.claude.ai
raw.githubusercontent.com
github.com
api.github.com
codeload.github.com
```

Each entry must be at least two labels of letters, digits and inner hyphens, at most 253 characters, with a letter in the top-level label so an address can never be taken for a name. Wildcards are refused: the firewall resolves names to addresses, and a wildcard cannot be resolved.

### `sandbox devcontainer up`

Builds the image, creates or starts the container, then applies the firewall:

1. `docker build --tag lyna-tmux-<project> --file .devcontainer/Dockerfile .devcontainer` runs every time. The layer cache makes it cheap, and a fresh container then picks up edits to the rendered files. A container that is already running keeps the image it started from until `down` removes it.
2. If no container exists, `docker run --detach --init --name lyna-tmux-<project> --cap-add NET_ADMIN --security-opt no-new-privileges --user lyna --mount type=bind,source=<project>,target=/workspace --mount type=volume,source=lyna-tmux-claude-<project>,target=/home/lyna/.claude --env CLAUDE_CONFIG_DIR=/home/lyna/.claude --workdir /workspace lyna-tmux-<project>`. If one exists but is stopped, `docker start`. **No Docker socket is mounted.** `no-new-privileges` stops the container user from gaining root through a setuid program; the firewall is still applied through `docker exec --user root`, which is not affected by it.
3. `docker exec --user root lyna-tmux-<project> /usr/local/bin/init-firewall.sh`.

Each `--mount` value is CSV-quoted where needed, because `docker` reads `--mount` as a CSV record and a path containing a comma would otherwise inject extra mount options.

### The egress firewall

`init-firewall.sh` runs as root at container start, from `postStartCommand` through the sudoers rule or from `docker exec --user root`. It refuses arguments, and the allowlist path comes from a root-owned location; the two environment overrides that exist for tests cannot be set by the container user, because `sudo` resets the environment.

What it does, in order:

1. Reads and validates every domain of `/usr/local/etc/lyna-tmux/allowed-domains`, skipping comments and blank lines. An invalid domain aborts the run.
2. Reads the IPv4 name servers from `/etc/resolv.conf`.
3. Resolves every domain to IPv4 addresses with `getent ahostsv4` into a staging `ipset`, then swaps that set into place at once, so the live set is never half-populated. An unresolvable domain aborts the run.
4. Applies the rules: flush `OUTPUT`; accept on `lo`; accept `ESTABLISHED,RELATED`; accept UDP and TCP port 53 to each resolver; accept anything whose destination is in the allowlist set; `REJECT` everything else with `icmp-admin-prohibited`; set the `OUTPUT` and `FORWARD` policies to `DROP`. Where IPv6 is available, `ip6tables` gets loopback, `ESTABLISHED,RELATED`, a reject rule and the same `DROP` policies, with no allowlist: IPv6 egress is closed.
5. Proves itself. It picks a host among `example.com`, `example.net` and `example.org` that is not in the allowlist and fails if that host is reachable, then fails if the first allowed domain is not reachable. Both results are logged.

If any step after the root and argument checks fails, the exit trap re-blocks everything and prints `init-firewall: failed; egress stays blocked until this script succeeds`. A container whose firewall did not come up does not keep an open network.

Addresses are resolved when the firewall runs. Rerun `lmux sandbox devcontainer up` after editing the allowlist, or when an allowed service moves to a new address.

### `sandbox devcontainer shell`

`docker exec --interactive --tty --user lyna --workdir /workspace <container> bash -l`, forwarding `TERM`, `COLORTERM` and `LANG` so the shell renders as it does outside. It needs a terminal, and it fails with `devcontainer: container is not running (run lmux sandbox devcontainer up)` if the container is not running. The rendered files are checked first, exactly as they are before a build: a `.devcontainer` the project has rewritten fails with `devcontainer: file is not the one lyna-tmux renders`, because the isolation of the running container could no longer be read from the tree it came from.

### `create --isolation container`

When a launch asks for container isolation and the current process is **not** already inside a container, `lyna-tmux` does not start a workspace on the host's tmux server. It re-runs itself inside the running container:

```sh
docker exec --interactive --tty --user lyna --workdir /workspace \
  --env TERM --env COLORTERM --env LANG <container> \
  lmux create --sandbox <profile> --isolation=container /workspace/<subdirectory>
```

Details that matter:

- The subcommand becomes `team` instead of `create` when the launch wants agent teams.
- `--layout`, `--name`, `--model`, `--effort` and `--mode` are passed through when set, `--continue` and `--detach` when asked, and anything after `--` is forwarded to `claude`.
- The directory is rewritten to its path under `/workspace`, so opening a subdirectory of the project opens the matching subdirectory inside the container. A directory outside the project is refused.
- The sandbox profile is resolved from the **host's** configuration and passed explicitly, because the container does not share that configuration. A host configuration of `off` is refused here too.
- With `--detach`, or when standard input is not a terminal, the same command runs without `--tty`, since `docker` refuses `--tty` in that case.
- If the container is not running, `create` and `team` offer to build and start it: `The dev container <container> for <project> is not running. Build and start it now? [y/N]`. A yes runs the same steps as `sandbox devcontainer up`, with the build streaming to the terminal, and then opens the workspace. `--start-container` does it without asking, which is what a script or a `--detach` launch needs. Declined, or with no terminal to ask on, the launch stops with `container isolation opens the workspace in <container> for <project>: devcontainer: container is not running (run lmux sandbox devcontainer up)`, and the message names `--start-container`.

Inside the container the process sees a container marker (`/.dockerenv` or `/run/.containerenv`), so it proceeds as a normal launch and `sandbox status` reports `container` whatever the workspace asked for.

### `sandbox devcontainer down`

Stops and removes the container. **The image and the Claude volume are kept**, so the next `up` is fast and the Claude configuration and transcripts survive. A container that does not exist is not an error. The command prints which image and volume it kept.

## The `bypassPermissions` rule

`bypassPermissions` is the Claude Code permission mode that skips every prompt. `lyna-tmux` refuses it unless the profile is `strict` or the isolation level is `container`:

```text
sandbox: bypassPermissions requires the strict profile or container isolation (profile standard, isolation bash)
```

The reason is that the Bash sandbox alone leaves file tools, hooks and MCP servers unconfined. Skipping every prompt removes the only check on those tools, so it may only be combined with a boundary that contains them too: `strict`, which denies `.env` reads, blocks reads outside the working directories, scrubs subprocess environments and never lets a command out of the sandbox; or a container, which holds the entire session.

The rule is enforced in three places so no path around it exists: when a workspace launch is prepared, when `sandbox show` renders the settings, and when a Claude command line is built. When bypass is not allowed, the settings also carry `"disableBypassPermissionsMode": "disable"`, so Claude itself rejects the flag and cannot cycle into bypass mode during the session.

`sandbox profiles` and `sandbox status` both print the current answer as a `bypassPermissions` row.

## Residual risk

**An allowed host is allowed for everything.** An allowlist bounds destinations, not content or intent. A package registry on the list can serve any package it hosts, and a code hosting host can serve, and accept, any repository the session's credentials reach. Data that leaves through an allowed host leaves.

**The container firewall allows addresses, not names.** Names are resolved to IPv4 addresses once, when the firewall runs. Any other service sharing one of those addresses is reachable, and an allowed service that moves is unreachable until you rerun `up`. IPv6 egress is closed rather than filtered.

**What a sandbox escape costs.**

- At `bash` isolation, escaping the Bash sandbox lands in your own user account: your files, your credentials on disk (denied to sandboxed commands, not removed), and the workspace's tmux socket. File tools, hooks and MCP servers were never inside that boundary to begin with.
- At `process` isolation, an escape from the runtime lands in the same place. The token variables are in the Claude process environment by design, so a full escape reaches them. The one Unix socket the confined process may use is the workspace's tmux server socket, and on Linux the runtime blocks every Unix socket, so on Linux that path is closed for the confined process and for whatever escapes into it.
- At `container` isolation, an escape from the Bash boundary is still inside the container, where `/workspace` is a writable bind mount of your real project directory and the Claude volume holds your transcripts. `NET_ADMIN` is held by the container, so root inside it can rewrite the firewall. A container escape is a Docker or kernel vulnerability, not something these files can prevent. No Docker socket is mounted, which is what keeps a container escape from becoming host control through the daemon.
- `no-new-privileges` is applied by `lmux sandbox devcontainer up`. An editor that starts the container from `devcontainer.json` applies only the `runArgs` listed there (`--cap-add=NET_ADMIN`, `--init`).

**Out of scope.** Vulnerabilities in tmux, Claude Code, `docker` or the operating system sandbox themselves belong upstream, as do attacks that need an attacker who already controls your user account. See [SECURITY.md](../SECURITY.md) for reporting. `lyna-tmux` also does not read or write your own tmux configuration or your own Claude settings, so nothing here hardens those.

**Two deliberate non-defaults.** `enableWeakerNestedSandbox` is never written by `lyna-tmux`: it weakens isolation by reusing a container's `/proc`, so it stays an explicit decision you make in Claude's own settings. The `mask` credential mode is never written either, for the reason given above. `lmux init --project` can put the project-honored subset of a profile into `.claude/settings.json` for a team, and deliberately leaves out `network.strictAllowlist` (which Claude ignores in a repository file), `disableBypassPermissionsMode` and `enableWeakerNestedSandbox` (launcher and machine decisions, not team policy).

## Troubleshooting

`lmux doctor` reads the machine and prints the exact command or setting for every problem. It changes nothing unless you pass `--fix`, which offers the fixes lyna-tmux can carry out itself one at a time (`--yes` applies them all, `--dry-run` only lists them) and leaves the rest, terminal settings and package installs among them, to you. `lmux sandbox status` runs the subset that applies to the profile and isolation level in effect, as a readiness block. Install commands below are the ones for your package manager: the report picks it from the platform (`brew` on macOS, the distribution's own on Linux, read from `/etc/os-release`), and an unrecognized Linux distribution gets both common forms.

### macOS

| Row | When it fails | Fix printed |
| --- | --- | --- |
| `Sandbox (Seatbelt)` | `sandbox-exec` is not on `PATH`; macOS ships it in `/usr/bin` | `export PATH="/usr/bin:$PATH"` |
| `Docker` (container isolation) | `docker` is not on `PATH` | `brew install colima docker && colima start` |
| `Docker` | the daemon is not reachable | `colima start (or open -a Docker)` |
| `Sandbox runtime` (process isolation) | `srt` is not on `PATH` | `npm install -g @anthropic-ai/sandbox-runtime` (needs Node.js 20.11 or newer) |

### Linux

| Row | When it fails | Fix printed |
| --- | --- | --- |
| `bubblewrap` | `bwrap` is not on `PATH`; the Linux sandbox needs it | `sudo apt-get install bubblewrap`, `sudo dnf install bubblewrap`, `sudo pacman -S bubblewrap`, `sudo apk add bubblewrap` or `sudo zypper install bubblewrap` |
| `socat` | `socat` is not on `PATH`; the Linux sandbox needs it | the same command for `socat` |
| `ripgrep` (process isolation) | `rg` is not on `PATH` | the same command for `ripgrep` |
| `User namespaces` | see below | see below |
| `Container` | the process runs inside a container, where bubblewrap cannot mount a fresh `/proc` without extra privileges (a warning) | `Claude settings: "sandbox": {"enableWeakerNestedSandbox": true}` (only when the container is the isolation boundary) |
| `Docker` (container isolation) | `docker` is not on `PATH` | `sudo apt-get install docker.io`, `sudo dnf install moby-engine`, or `docker` for the other managers |
| `Docker` | the daemon is not reachable | `sudo systemctl start docker`, or `sudo usermod -aG docker "$USER" (then log out and back in)` when the error is a permission denial |

### Ubuntu 24.04 and later: user namespaces

The `User namespaces` row reads `/proc/sys/kernel/apparmor_restrict_unprivileged_userns`:

- file missing: OK, no AppArmor restriction on unprivileged user namespaces.
- contents `1` **and** `/etc/apparmor.d/bwrap` exists: OK, the profile grants bubblewrap an exception.
- contents `1` **and** no profile: **fail**, "AppArmor restricts unprivileged user namespaces, so bubblewrap cannot isolate commands". The printed fix is exactly:

  ```sh
  sudo tee /etc/apparmor.d/bwrap > /dev/null <<'EOF'
  abi <abi/4.0>,
  include <tunables/global>

  profile bwrap /usr/bin/bwrap flags=(unconfined) {
    userns,
    include if exists <local/bwrap>
  }
  EOF
  sudo systemctl reload apparmor
  ```

- the file cannot be read: a warning, with `sysctl kernel.apparmor_restrict_unprivileged_userns` (a value of `1` means bubblewrap needs the AppArmor profile).
- any other contents: OK, unprivileged user namespaces allowed.

### WSL

The `WSL` row appears when `/proc/version` names a WSL kernel or `WSL_DISTRO_NAME` is set.

- **WSL2**: OK. Everything in the Linux section applies.
- **WSL1**: **fail**, "WSL1 cannot run bubblewrap; the sandbox needs WSL2". Fix: `wsl --set-version <distribution> 2` (run in PowerShell; list distributions with `wsl -l -v`). The distribution name is taken from `WSL_DISTRO_NAME` and quoted when it is not a plain word.
- `WSL_DISTRO_NAME` set but `/proc/version` does not name a WSL kernel: a warning, with `wsl -l -v` (run in PowerShell; the VERSION column must be 2).

### Any other platform

The sandbox check fails with "the Claude Code sandbox supports macOS, Linux and WSL2, not `<platform>`", and the printed fix is `set sandbox.profile = "off" in config.toml and pass --sandbox off`. That is an explicit trade, and it is why `off` needs the flag.

### Other symptoms

- **Bash refuses to run every command.** `failIfUnavailable` is on in `standard` and `strict`: the sandbox could not start. Run `lmux sandbox status` and fix the failing readiness row.
- **A command fails on network access in `strict`.** The host is not on the allowlist. Add it with `sandbox.allowed_domains` (see [configuration.md](configuration.md)); there is no prompt to accept in `strict`.
- **A launch says the profile is not honored.** `sandbox.profile = "off"` is in the configuration. Pass `--sandbox off` for this launch, or change the configuration.
- **A container workspace cannot start.** Check the container with `lmux sandbox devcontainer up`; the firewall self-test runs on every `up` and its failures name the host it could not reach.
