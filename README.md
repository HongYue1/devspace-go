# MCP WebCoder

**Give ChatGPT & Claude secure access to your local machine. Turn any MCP host into your coding partner.**

MCP WebCoder is a self-hosted MCP server that lets AI assistants read, edit, search, and run code in your real local projects — your files, your tools, your terminal — without uploading anything to a third party. You run it on your machine and expose it through a tunnel you control.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [What AI Can Do](#what-ai-can-do)
- [Configuration](#configuration)
- [Tunnel (Remote Access)](#tunnel-remote-access)
- [Shell Support](#shell-support)
- [Security](#security)
- [Building from Source](#building-from-source)
- [Platform Support](#platform-support)
- [Project Structure](#project-structure)

### 🌍 Translations

| Language | | Language | | Language | |
|---|---|---|---|---|---|
| [Afrikaans](readme/af.md) | [العربية](readme/ar.md) | [Български](readme/bg.md) | [বাংলা](readme/bn.md) | [Català](readme/ca.md) |
| [Čeština](readme/cs.md) | [Dansk](readme/da.md) | [Deutsch](readme/de.md) | [Ελληνικά](readme/el.md) | [English](readme/en.md) |
| [Español](readme/es.md) | [Eesti](readme/et.md) | [فارسی](readme/fa.md) | [Suomi](readme/fi.md) | [Français](readme/fr.md) |
| [Gaeilge](readme/ga.md) | [עברית](readme/he.md) | [हिन्दी](readme/hi.md) | [Hrvatski](readme/hr.md) | [Magyar](readme/hu.md) |
| [Indonesia](readme/id.md) | [Italiano](readme/it.md) | [日本語](readme/ja.md) | [한국어](readme/ko.md) | [Lietuvių](readme/lt.md) |
| [Latviešu](readme/lv.md) | [Melayu](readme/ms.md) | [Malti](readme/mt.md) | [Nederlands](readme/nl.md) | [Norsk](readme/no.md) |
| [Polski](readme/pl.md) | [Português](readme/pt.md) | [Română](readme/ro.md) | [Русский](readme/ru.md) | [Slovenčina](readme/sk.md) |
| [Slovenščina](readme/sl.md) | [Српски](readme/sr.md) | [Svenska](readme/sv.md) | [Kiswahili](readme/sw.md) | [தமிழ்](readme/ta.md) |
| [ไทย](readme/th.md) | [Türkçe](readme/tr.md) | [Українська](readme/uk.md) | [اردو](readme/ur.md) | [Tiếng Việt](readme/vi.md) |
| [简体中文](readme/zh.md) | [isiZulu](readme/zu.md) |

---

## Quick Start

### 1. Download
Pick your platform from [Releases](../../releases) or build from source:
```bash
./scripts/unix/build.sh      # Linux / Mac
.\scripts\windows\build.ps1   # Windows
```

### 2. Configure
```bash
mcp-webcoder config                # Interactive prompts
mcp-webcoder config set port 7676  # Or change one setting
```

### 3. Run
```bash
mcp-webcoder                  # Starts server. Auto-detects config.
```

This also starts a tunnel: ngrok when a domain or authtoken is configured, otherwise Cloudflare from `tools/`.

### 4. Connect your MCP client
```
https://YOUR-TUNNEL.trycloudflare.com/mcp
```
Or locally: `http://127.0.0.1:7676/mcp`

For a URL that never changes and a token to guard it, see [Tunnel](#tunnel-remote-access).

---

## Installation

No Node.js, no npm, no Python. Single binary.

| Platform | Download |
|---|---|
| **Windows** | `mcp-webcoder.exe` |
| **Linux** | `mcp-webcoder` |
| **macOS Intel** | `mcp-webcoder` |
| **macOS M-chip** | `mcp-webcoder` |

Requires **Go 1.23+** only if building from source.

---

## What AI Can Do

Once connected, the AI can open one of your approved project folders as a workspace:

- **Read, write, and edit** files inside the workspace
- **Create directories and move/rename files** safely inside the workspace
- **Search code** with regex and inspect directories
- **Run shell commands** (PowerShell on Windows, bash on Unix)
- **Discover project instructions** from `AGENTS.md` / `CLAUDE.md`
- **Auto-configure** with portable `.webcoder/config.json`

10 MCP tools: `open_workspace`, `read`, `write`, `mkdir`, `move`, `edit`, `grep`, `glob`, `ls`, `bash`

---

## Configuration

All config lives **in the same folder as the executable** (portable):

```
.webcoder/
└── config.json       ← allowed roots, port, shell, language
```

### config.json
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
| `shell` | `auto` | `auto`, `powershell`, `cmd`, `bash`, `sh` |
| `lang` | `auto` | Auto-detect from OS. Supports 47 languages |
| `toolMode` | `full` | `full` (all tools) or `minimal` (shell only for search) |
| `toolNaming` | `short` | `short` (read, write) or `legacy` (read_file, write_file) |
| `tunnel.provider` | `auto` | `auto`, `ngrok`, `cloudflared`, `pinggy`, `off` |
| `tunnel.domain` | — | Reserved ngrok domain, so the URL survives a restart |
| `tunnel.authtoken` | — | ngrok authtoken. `NGROK_AUTHTOKEN` is honoured too |
| `tunnel.cloudflared` | — | Named Cloudflare tunnel to run, by name or id |
| `tunnel.credentials` | — | Cloudflare credentials file; a relative path sits beside config.json |
| `publicBaseUrl` | — | The URL clients use. The CLI key is `publicUrl` |
| `authToken` | — | Bearer token every request must send. `WEBCODER_AUTH_TOKEN` is honoured too |

Change any of them with `mcp-webcoder config set <key> <value>`. Secrets are stored in the config file and never printed back — `config get` reports them as `set (hidden)`.

No environment variables needed — everything is in the portable config file.

---

## Tunnel (Remote Access)

Web clients need HTTPS, so MCP WebCoder publishes itself through a tunnel:

| Tunnel | URL | Setup |
|---|---|---|
| **ngrok** | Stable, with a reserved domain | Free account; the agent is fetched into `tools/` on first use |
| **Cloudflare** | Your own domain, or a new URL every session | `mcp-webcoder tunnel setup` for a domain; nothing to do for a throwaway URL |
| **Pinggy** | New URL every session | Needs `ssh` on PATH |

On `auto`, ngrok goes first when a domain or authtoken is configured, otherwise Cloudflare. Name a provider to skip the rest, or set `off` to stay local.

### A URL that survives a restart

Reserve the free domain at [dashboard.ngrok.com/domains](https://dashboard.ngrok.com/domains), copy the token from [your authtoken page](https://dashboard.ngrok.com/get-started/your-authtoken), then:

```bash
mcp-webcoder config set tunnel.domain example.ngrok-free.app
mcp-webcoder config set tunnel.authtoken YOUR_TOKEN
```

The MCP URL is then `https://example.ngrok-free.app/mcp` every time. The token is stored in the config file and never printed back — `config get tunnel.authtoken` reports `set (hidden)`. A domain pasted as a full link works too.

Browsers opening the URL see ngrok's one-time interstitial page; MCP clients send JSON and pass straight through.

### Your own domain, through Cloudflare

All you need is a domain already on Cloudflare — the free plan is enough. One command claims a hostname on it:

```bash
mcp-webcoder tunnel setup mcp.example.com
```

That command fetches `cloudflared` if `tools/` does not have it, has Cloudflare authorise this machine once in your browser, creates the tunnel, writes its credentials beside your config, points the hostname at it, and generates a bearer token if you do not have one yet. Then:

```bash
mcp-webcoder serve
```

The MCP URL is `https://mcp.example.com/mcp` every time. The server starts `cloudflared` itself and stops it on exit — no service to install, nothing to keep running in a second window.

Running setup again is safe: an existing tunnel and an existing DNS record are reused rather than replaced. A second argument names the tunnel, which is how another machine adopts one that already exists:

```bash
mcp-webcoder tunnel setup mcp.example.com webcoder
```

| Written | What |
|---|---|
| `.webcoder/config.json` | `tunnel.provider`, `tunnel.cloudflared`, `tunnel.credentials`, `publicBaseUrl`, `authToken` |
| `.webcoder/cloudflared-<name>.json` | Tunnel credentials, kept with the app so the folder stays portable |
| `~/.cloudflared/cert.pem` | Cloudflare's one-time authorisation for this machine |

**One thing to watch.** If `~/.cloudflared/config.yml` exists and defines `ingress:` rules, `cloudflared` loads it and may route your hostname by that file rather than the port this server publishes. Both `tunnel setup` and `serve` name the file when they find it; rename it and the tunnel follows the server.

---

## Shell Support

| OS | Default | Alternatives |
|---|---|---|
| **Windows** | PowerShell | `cmd` / `pwsh` |
| **Linux** | bash | `sh` / any shell |
| **macOS** | bash | `sh` / `zsh` |

Set `"shell"` in config.json or run `mcp-webcoder config`.

---

## Security

- **Bearer token** — set `authToken` and every request has to carry `Authorization: Bearer <token>`; only `/healthz` stays open
- **Path containment** — all file ops validated against allowed roots
- **Tunnel access** — a tunnel puts this server on the internet, so keep a token on it and stop the server when it is not in use
- **No third-party uploads** — your code never leaves your machine

```bash
mcp-webcoder config token       # print the token to paste into the client
mcp-webcoder config token new   # replace it
```

`mcp-webcoder tunnel setup` generates a token for you. Without one, the server answers anybody who finds the URL.

---

## Building from Source

```bash
git clone https://github.com/snakex21/devspace-go
cd devspace-go

# Build everything (all platforms)
.\scripts\windows\build.ps1     # Windows
./scripts/unix/build.sh          # Linux / Mac
make -f scripts/unix/Makefile    # Linux / Mac (make)

# Build just for current platform
go build -o mcp-webcoder ./cmd/devspace/
```

---

## Platform Support

| Platform | Server |
|---|---|
| **Windows** | ✅ |
| **Linux** | ✅ |
| **macOS Intel** | ✅ |
| **macOS M-chip** | ✅ |

Cross-compiles from any platform to any platform.

---

## Project Structure

```
mcp-webcoder/
├── cmd/
│   └── devspace/           ← CLI + MCP server
├── internal/
│   ├── config/             ← Portable config system
│   ├── locales/            ← 47 language translations
│   ├── logger/             ← Structured logging (zerolog)
│   ├── server/             ← HTTP + MCP + tunnel orchestration
│   ├── shells/             ← Shell detection
│   ├── store/              ← SQLite workspace sessions
│   ├── tools/              ← read, write, edit, grep, glob, ls, bash
│   ├── tunnel/             ← Finds and fetches ngrok / cloudflared
│   ├── version/            ← Version stamped in at build time
│   └── workspace/          ← Workspace & path validation
├── scripts/
│   ├── windows/            ← PowerShell build script
│   ├── unix/               ← Bash + Makefile build scripts
│   └── userscripts/        ← Tampermonkey auto-approve script
├── readme/                 ← Translations of this file (47 languages)
├── tools/                  ← cloudflared.exe
├── go.mod / go.sum
└── README.md
```

---

Built in Go. Zero npm. Zero Node.js. One binary.
