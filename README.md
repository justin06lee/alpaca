# alpaca

Self-host a model with Ollama, get a real API for it, and use it from every
other machine you own — through a terminal chat interface or anything that
speaks the OpenAI protocol.

One static binary plays both parts: `alpaca serve` on the machine with the GPU,
`alpaca chat` everywhere else.

```
  alpaca  serving llama3.2:latest · ollama 0.32.5 · port 8080

  reachable at
    ✓ lan        192.168.1.168:8080                             same network · plain http, fastest
    ✓ lan        [fd7a:115c:a1e0::2833:1540]:8080               same network · plain http, fastest
    ✓ tailscale  100.98.21.63:8080                              anywhere on your tailnet · encrypted by wireguard
    ✓ internet   [2600:1702:891b:aa00:842:dffb:d410:52bb]:8080  direct ipv6 — may need a firewall rule · tls

  run this on every other machine

    alpaca link alpaca1:eyJpIjoiYjBlYzQ1YTkzMmRjIiwibiI6Ik1hY0Jvb2tBaXIi…
```


## Setup

You need [Ollama](https://ollama.com) running with at least one model pulled:

```sh
ollama pull llama3.2
```

**On the machine with the model:**

```sh
alpaca serve
```

It prints a connect string. **On every other machine**, paste it once:

```sh
alpaca link alpaca1:eyJpIjoi...
alpaca chat
```

That is the whole setup. The connect string carries the API key, the pinned
certificate, and every route to the server, so there is nothing else to
configure and nothing to re-enter later.


## How it finds the server

You pasted one string; after that the address is alpaca's problem, not yours.
On every launch the client races every route it knows and keeps whichever
answers first:

| Route | Transport | When it wins |
|---|---|---|
| Last known good | as before | Nothing moved since last time — the usual case |
| mDNS discovery | plain HTTP | Same network, but the server's IP changed |
| LAN hints | plain HTTP | Same network, discovery blocked |
| Public endpoint | **TLS**, pinned | You are somewhere else entirely |

The public candidate starts 300 ms late on purpose. Without that stagger it
sometimes wins a race it should lose — reaching a machine in the same room by
going out to the internet and back. The delay costs nothing when LAN works,
because the public probe is never opened.

Every probe checks the server's identity, not just that something answered. A
LAN hint captured months ago may now point at whatever machine DHCP handed the
address to.

Because addresses change but identity does not, **a connect string keeps working
across reboots, DHCP leases, and moving between networks.** You only need a new
one if you rotate the key or the certificate.


## Reaching it from outside your network

In order of preference:

1. **Tailscale** — if the server is on your tailnet, it already works from
   anywhere, and WireGuard is doing the encryption. Nothing to configure;
   alpaca picks up the tailnet address automatically.
2. **IPv6** — most home connections have a globally routable IPv6 address, which
   needs no port forwarding. alpaca offers it automatically over TLS. Your
   router firewall may still need an inbound rule.
3. **UPnP / NAT-PMP** — if enabled on your router, alpaca asks for a port
   forward on startup and renews it while running. Many routers ship with this
   off, and it cannot work behind carrier-grade NAT; alpaca detects that case
   and says so rather than advertising an address that silently fails.
4. **Manual port forward** — forward a port yourself and start with
   `alpaca serve --public your.address:8080`.

Anything outside your LAN or tailnet is always TLS.


## Security

- **One API key**, generated on first run, required on every endpoint except the
  health check. Compared in constant time.
- **TLS with certificate pinning.** The server's self-signed certificate is
  pinned by SHA-256 fingerprint in the connect string, so there is no
  certificate authority to trust and nothing to renew. This is stronger than
  ordinary CA validation here: a mis-issued public certificate for your address
  would still be rejected.
- **Plain HTTP only on networks that are already private** — RFC 1918, IPv6
  unique-local, and Tailscale. Anything internet-routable is TLS-only, including
  a public IPv6 address on the server itself.
- The connect string **contains the API key**. Treat it like a password.
- Revoke everything with `alpaca serve --rotate-key` (or `--rotate-cert`). Every
  previously linked machine must re-link.

`alpaca serve` binds all interfaces by default so other machines can reach it.
Use `--bind 127.0.0.1` if you only want local access.


## Using it from other tools

The gateway speaks the OpenAI API, so most existing tooling works by pointing a
base URL at it:

```sh
curl http://192.168.1.168:8080/v1/chat/completions \
  -H "Authorization: Bearer alp_..." \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.2:latest","messages":[{"role":"user","content":"hello"}]}'
```

```python
from openai import OpenAI

client = OpenAI(base_url="http://192.168.1.168:8080/v1", api_key="alp_...")
client.chat.completions.create(
    model="llama3.2:latest",
    messages=[{"role": "user", "content": "hello"}],
)
```

Implemented: `/v1/chat/completions` (streaming and buffered), `/v1/models`,
`/v1/embeddings`, plus `/healthz` and `/api/info`.

Supported request fields include `temperature`, `top_p`, `seed`, `stop` (string
or array), `max_tokens` and `max_completion_tokens`, `response_format:
json_object`, `stream_options.include_usage`, and multimodal `content` arrays
with base64 `data:` image URLs. Remote image URLs are refused rather than
fetched — fetching them would make the gateway an SSRF proxy into your network.


## Chat interface

| Key | Action |
|---|---|
| `enter` | Send |
| `ctrl+j` | Newline (`shift+enter` is not something terminals transmit) |
| `esc` | Stop generating, or clear the composer |
| `ctrl+p` | Switch model |
| `ctrl+n` | New chat |
| `ctrl+s` | Browse saved chats |
| `ctrl+r` | Regenerate the last reply |
| `ctrl+y` | Copy the last reply |
| `pgup` / `pgdn`, `ctrl+u` / `ctrl+d` | Scroll |
| `?` | All keys |
| `ctrl+c` | Quit |

Slash commands do the same things: `/model`, `/new`, `/sessions`, `/system`,
`/retry`, `/copy`, `/clear`, `/stats`, `/help`, `/quit`.

Chats are saved automatically as JSON under `~/.config/alpaca/sessions/`.
`alpaca chat` starts a **new** conversation each time — old context does not
silently attach itself to an unrelated question. Use `--resume` or `ctrl+s` to
pick up where you left off.


## Commands

```
alpaca serve                  start the API and print a connect string
alpaca link <connect-string>  save a server (also reads stdin)
alpaca chat                   open the chat interface
alpaca ask "question"         one-shot answer on stdout
alpaca models                 list models on the server
alpaca status                 show which servers are linked and reachable
alpaca profiles               list / remove / set default
alpaca discover               find alpaca servers on this network
```

`ask` is built for pipes:

```sh
alpaca ask "explain this error" < build.log
cat main.go | alpaca ask "review this" > review.md
```

Every command takes `--help`. Multiple servers are supported via `--profile`.


## Building

```sh
make build          # ./alpaca for this machine
make test           # everything, with the race detector
make cross          # dist/ binaries for linux, macos, and windows
```

Or directly:

```sh
go build -o alpaca ./cmd/alpaca
GOOS=linux GOARCH=amd64 go build -o alpaca-linux ./cmd/alpaca
```

There is no cgo and there are no runtime dependencies, so deploying to another
machine is a matter of copying the binary.


## Configuration

Everything lives in `~/.config/alpaca` (override with `ALPACA_HOME`):

```
server.json      this machine's identity and API key   (serve)
certs/           self-signed certificate and key       (serve)
profiles.json    linked servers                        (clients)
sessions/        saved chats                           (clients)
```

Files are `0600` and written atomically, so an interrupted save cannot corrupt a
key or lose history.


## Troubleshooting

**"could not reach … on any known route"** — check `alpaca serve` is still
running. If the server moved networks, run `alpaca discover` to find it, or
re-link with a fresh connect string.

**First connection on macOS fails, then works** — macOS asks for Local Network
permission the first time a binary talks to a LAN address, and blocks that
attempt while asking. Approve it and retry; this bites once per binary.

**"public: unavailable"** — the router has UPnP/NAT-PMP disabled or you are
behind carrier-grade NAT. LAN and tailnet are unaffected. See
*Reaching it from outside your network* above.

**mDNS finds nothing** — some networks block multicast, and most VPNs do. The
LAN hints and public endpoint in the connect string still work; add
`--no-discovery` to skip the scan and save a couple of seconds.

**A model is missing** — models come from Ollama on the server, so pull it
there: `ollama pull qwen2.5`.
