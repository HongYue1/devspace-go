# MCP WebCoder

A self-hosted MCP server that gives AI assistants working access to local projects: reading, editing, searching, deleting, running commands, and following long builds in real folders on a real machine.

It is a single Go binary with no runtime dependencies. It listens on localhost, publishes itself through a tunnel when a hosted client needs HTTPS, and keeps every file operation inside a list of approved project roots. Source code never leaves the machine that runs the server.

---

## What this fork changes

This fork begins at upstream [cf8b26a](https://github.com/snakex21/devspace-go/commit/cf8b26a3cfaaa92943efe3c997b170ac9cdc840a) and is 54 commits ahead of it. Upstream worked as a prototype: it opened a workspace and ran tools. It also shipped with no CI, no authentication, and a 54 MB Windows executable committed into the tree. Most of the work since has gone into making the server safe to expose and predictable to change.

| | Upstream at cf8b26a | This fork |
|---|---|---|
| **MCP tools** | 11 | 16 |
| **Tests** | 3 test files | 29 test files, 239 test functions |
| **CI** | None | gofmt, `go vet`, and tests on Linux, Windows, and macOS; four targets cross-compiled; release assets published from `v*` tags |
| **Authentication** | None | Bearer token required on every route except `/healthz`, with CLI commands to print and rotate it |
| **Tunnels** | Cloudflare quick tunnel, when `cloudflared` happened to be present | ngrok reserved domains, named Cloudflare tunnels created by one command, Pinggy, or off |
| **`cloudflared`** | 54 MB Windows binary committed to the repository | Downloaded on demand for the host platform |
| **Language** | 51 locale files; an unrecognised system language fell back to Polish | English only |
| **Binaries** | Server plus a separate GUI | Server only |

The five tools added here are `remove`, `list_roots`, `job_status`, `job_kill`, and `job_list`. Beyond those, `bash` gained a background mode, `grep` gained `caseInsensitive`, `contextLines`, and `maxMatches`, `write` preserves a file's existing line endings instead of normalising them, and the build version is stamped into the binary and reported in the MCP handshake.

---

## Table of contents

- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Tools](#tools)
- [Configuration](#configuration)
- [Remote access](#remote-access)
- [Shells](#shells)
- [Security](#security)
- [Building from source](#building-from-source)
- [Platform support](#platform-support)
- [Project layout](#project-layout)
- [Development](#development)

---

## Requirements

Nothing at runtime: no Node.js, no npm, no Python. Go 1.26 or newer is needed only to build from source.

Prebuilt archives are attached to each [release](../../releases), one per target:

| Target | Asset |
|---|---|
| Windows x64 | `mcp-webcoder-<tag>-windows-amd64.zip` |
| Linux x64 | `mcp-webcoder-<tag>-linux-amd64.tar.gz` |
| macOS Intel | `mcp-webcoder-<tag>-macos-amd64.tar.gz` |
| macOS Apple silicon | `mcp-webcoder-<tag>-macos-arm64.tar.gz` |

---

## Quick start

**1. Configure.** The interactive wizard asks for the project roots, port, and shell; single settings can also be set directly.

```bash
mcp-webcoder config
mcp-webcoder config set port 7676
```

**2. Run.** Configuration is detected automatically, and a tunnel starts alongside the server unless one is turned off.

```bash
mcp-webcoder serve
```

**3. Connect a client.** The MCP endpoint is `/mcp` on whichever URL the server prints at startup:

```
https://example.ngrok-free.app/mcp     # through a tunnel
http://127.0.0.1:7676/mcp              # on the same machine
```

A legacy SSE endpoint is served at `/sse` for clients that still expect it. For a URL that survives a restart and a token to guard it, see [Remote access](#remote-access).

---

## Tools

A session starts by opening one of the configured roots as a workspace. Every later call carries the returned `workspaceId`, and every path is resolved inside that root.

| Tool | Purpose |
|---|---|
| `list_roots` | The roots this server accepts, with the git branch of each and which one is the default |
| `open_workspace` | Open a root as a workspace; returns a `workspaceId` and any `AGENTS.md` or `CLAUDE.md` found in it |
| `open_default_workspace` | The same, without sending a local path, for clients that block absolute paths |
| `read` | Read a file, optionally from an offset and for a limited number of lines |
| `write` | Create a file or replace one completely, keeping its existing line endings |
| `edit` | Replace exact text blocks, with `replaceAll`, `expectedOccurrences`, and `dryRun` |
| `mkdir` | Create a directory, including missing parents |
| `move` | Move or rename a file or directory, creating parent directories as needed |
| `remove` | Delete a file or directory, with `recursive` and `dryRun` |
| `grep` | Regex search over file contents, with an include glob, `caseInsensitive`, `contextLines`, and `maxMatches` |
| `glob` | Find files by pattern |
| `ls` | List a directory |
| `bash` | Run a shell command, in the foreground or as a background job |
| `job_status` | Read a background job, streaming output from a cursor and optionally waiting for the end |
| `job_kill` | Stop a background job and every process it started |
| `job_list` | The tracked jobs, newest first |

Project instructions are part of the handshake rather than something to hunt for: `open_workspace` returns the contents of `AGENTS.md` or `CLAUDE.md` at the root, and lists any others found deeper in the tree so they can be read before touching the code they describe.

### Long-running commands

A tool call has to answer while the client is still listening, so a foreground command is capped at 120 seconds and defaults to 30. Anything longer belongs in a background job:

| Step | Call |
|---|---|
| Start | `bash` with `background: true`, which returns a `jobId` immediately |
| Follow | `job_status` with that `jobId`; passing the previous `nextLine` as `sinceLine` returns only new output, and `wait` blocks up to 25 seconds for the job to end |
| Stop | `job_kill`, which ends the whole process tree |
| Review | `job_list` |

A `timeout` above 120 seconds starts a background job rather than failing partway through a request nothing is waiting for. A job keeps up to 1 MB of output in memory, discards the oldest lines beyond that and reports how many it discarded, and stops after an hour unless a longer `timeout` is given, up to 24 hours. The newest 64 jobs are tracked, and finished ones are forgotten two hours after they end.

### Deleting files

`remove` exists so that deleting something does not require handing `rm -rf` to a shell. It refuses the workspace root itself and any `.git` directory, requires `recursive` for a directory that is not empty, and reports what would go when called with `dryRun`.

---

## Configuration

Configuration lives beside the executable, so the whole folder stays portable:

```
.webcoder/
└── config.json
```

```json
{
  "host": "127.0.0.1",
  "port": 7676,
  "allowedRoots": ["C:/projects"],
  "publicBaseUrl": "http://127.0.0.1:7676",
  "shell": "auto",
  "lang": "auto",
  "toolMode": "full",
  "toolNaming": "short",
  "authToken": "",
  "tunnel": {
    "provider": "auto",
    "domain": "",
    "authtoken": "",
    "cloudflared": "",
    "credentials": ""
  }
}
```

| Field | Default | Description |
|---|---|---|
| `allowedRoots` | — | Project folders that may be opened. Everything else is refused |
| `shell` | `auto` | `auto`, `powershell`, `cmd`, `bash`, or `sh` |
| `lang` | `auto` | Interface language. English is the only bundled locale |
| `toolMode` | `full` | `full` registers every tool; `minimal` omits `grep`, `glob`, and `ls`, leaving search to the shell |
| `toolNaming` | `short` | `short` uses `read` and `write`; `legacy` uses `read_file`, `write_file`, `remove_path`, `run_shell`, and the rest of the older names |
| `tunnel.provider` | `auto` | `auto`, `ngrok`, `cloudflared`, `pinggy`, or `off` |
| `tunnel.domain` | — | Reserved ngrok domain, so the URL survives a restart |
| `tunnel.authtoken` | — | ngrok authtoken. `NGROK_AUTHTOKEN` is honoured too |
| `tunnel.cloudflared` | — | Named Cloudflare tunnel to run, by name or id |
| `tunnel.credentials` | — | Cloudflare credentials file; a relative path sits beside `config.json` |
| `publicBaseUrl` | — | The URL clients use. The CLI key for it is `publicUrl` |
| `authToken` | — | Bearer token every request must send. `WEBCODER_AUTH_TOKEN` is honoured too |

Any field can be changed with `mcp-webcoder config set <key> <value>`. Secrets are stored in the file and never printed back; `config get` reports them as `set (hidden)`. No environment variables are required.

---

## Remote access

Hosted clients require HTTPS, so the server publishes itself through a tunnel:

| Provider | URL | Setup |
|---|---|---|
| ngrok | Stable, with a reserved domain | Free account; the agent is fetched on first use |
| Cloudflare | A chosen hostname, or a throwaway URL per session | `mcp-webcoder tunnel setup` for a hostname; nothing for a throwaway URL |
| Pinggy | New URL every session | Requires `ssh` on `PATH` |

On `auto`, ngrok is used when a domain or authtoken is configured and Cloudflare otherwise. Naming a provider skips that choice, and `off` keeps the server local.

### A URL that survives a restart

Reserve a free domain at [dashboard.ngrok.com/domains](https://dashboard.ngrok.com/domains), copy the token from [the authtoken page](https://dashboard.ngrok.com/get-started/your-authtoken), then:

```bash
mcp-webcoder config set tunnel.domain example.ngrok-free.app
mcp-webcoder config set tunnel.authtoken TOKEN
```

The MCP URL is then `https://example.ngrok-free.app/mcp` every session. A domain pasted as a full link is accepted as well. Browsers opening the URL meet ngrok's one-time interstitial page; MCP clients send JSON and pass straight through.

### A hostname on Cloudflare

Any domain already on Cloudflare works, including on the free plan. One command claims a hostname on it:

```bash
mcp-webcoder tunnel setup mcp.example.com
```

That command fetches `cloudflared` if it is missing, has Cloudflare authorise the machine once in a browser, creates the tunnel, writes its credentials beside the config, points the hostname at it, and generates a bearer token if none exists yet. After that, `mcp-webcoder serve` publishes `https://mcp.example.com/mcp` every session, starting `cloudflared` as a child process and stopping it on exit. No service to install and no second window to keep open.

Running setup again is safe: an existing tunnel and DNS record are reused rather than replaced. A second argument names the tunnel, which is how another machine adopts one that already exists:

```bash
mcp-webcoder tunnel setup mcp.example.com webcoder
```

| Written | Contents |
|---|---|
| `.webcoder/config.json` | `tunnel.provider`, `tunnel.cloudflared`, `tunnel.credentials`, `publicBaseUrl`, `authToken` |
| `.webcoder/cloudflared-<name>.json` | Tunnel credentials, kept with the app so the folder stays portable |
| `~/.cloudflared/cert.pem` | Cloudflare's one-time authorisation for this machine |

One conflict is worth knowing about. If `~/.cloudflared/config.yml` exists and defines `ingress:` rules, `cloudflared` loads it and may route the hostname by that file instead of the port this server publishes. Both `tunnel setup` and `serve` name the file when they find it; renaming it lets the tunnel follow the server.

---

## Shells

| OS | Default | Alternatives |
|---|---|---|
| Windows | PowerShell | `cmd`, `pwsh` |
| Linux | bash | `sh`, or any shell on `PATH` |
| macOS | bash | `sh`, `zsh` |

The choice is the `shell` field in `config.json`, or a prompt in `mcp-webcoder config`. Whichever shell is active is named in the `bash` tool description, so a client knows which syntax to send.

---

## Security

A tunnel puts this server on the internet, which makes the token the difference between a private tool and an open shell.

- **Bearer token.** With `authToken` set, every request must carry `Authorization: Bearer <token>`; only `/healthz` stays open. Without one, the server answers anybody who finds the URL.
- **Path containment.** Every file operation is resolved and checked against the configured roots, and `remove` additionally refuses the roots themselves and `.git` directories.
- **No third-party upload.** Files are read and written locally; nothing is sent anywhere except to the client that asked for it.
- **Lifetime.** Stopping the server also stops the tunnel it started.

```bash
mcp-webcoder config token       # print the token for pasting into a client
mcp-webcoder config token new   # rotate it
```

`mcp-webcoder tunnel setup` generates a token when none exists.

---

## Building from source

```bash
git clone https://github.com/HongYue1/devspace-go
cd devspace-go

# current platform
go build -o mcp-webcoder ./cmd/devspace/

# every target
./scripts/unix/build.sh
make -f scripts/unix/Makefile
.\scripts\windows\build.ps1
```

Builds are pure Go with `CGO_ENABLED=0`, so any platform can cross-compile for any other.

---

## Platform support

| Platform | Server |
|---|---|
| Windows x64 | Supported |
| Linux x64 | Supported |
| macOS Intel | Supported |
| macOS Apple silicon | Supported |

---

## Project layout

```
devspace-go/
├── cmd/devspace/            CLI, config wizard, tunnel commands, server entry point
├── internal/
│   ├── config/              Portable configuration and its defaults
│   ├── locales/             English interface strings
│   ├── logger/              Structured logging
│   ├── server/              HTTP routing, MCP registration, auth, tunnel orchestration
│   ├── shells/              Shell detection and argument handling
│   ├── store/               SQLite-backed workspace sessions
│   ├── tools/               read, write, edit, grep, glob, ls, remove, bash, background jobs
│   ├── tunnel/              Finds, fetches, and runs ngrok and cloudflared
│   ├── version/             Version stamped in at build time
│   └── workspace/           Root discovery and path validation
├── scripts/                 Build scripts and a Tampermonkey auto-approve userscript
└── .github/workflows/       Test, cross-compile, and release
```

Downloaded connectors land in `tools/` beside the executable at runtime and are not committed.

---

## Development

```bash
go test ./... -count=1
go vet ./...
gofmt -l .
```

CI runs the same three checks on Linux, Windows, and macOS, then cross-compiles all four targets on Linux. Pushing a `v*` tag adds a release job that packages each target and publishes it.

One note for Windows contributors: a checkout that converts line endings makes `gofmt -l .` list nearly every file. CI therefore checks formatting on Linux only, and locally the reliable equivalent is to pipe a file through `gofmt` with its line endings normalised.
