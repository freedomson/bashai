# bashai

Ask for a shell command in plain English. A local LLM drafts a few candidates, a
decision model rates them, and you pick one from a list and run it.

Nothing runs on its own. `bashai` only ever *suggests*; you choose what executes.

```
query: check free disk space
⚡ fetching dynamic command list from local LLM

🎯 jev classification
  model=kev  intent=disk_usage  confidence=0.214
  LLM parsed (3 commands):
    disk_space      df -hT --type=apfs
    disk_usage      df -h
    free_disk       df -h | grep -E '^Filesystem|tmpfs' | tail
```

```
   📋 Pick a command  ↑/↓ move · enter run · esc quit

    df -hT --type=apfs
     disk_space  13%
 🔹 df -h
     disk_usage  48%
    df -h | grep -E '^Filesystem|tmpfs' | tail
     free_disk  40%

 ➕ Ask Another Question
     Return to query input
 ❌ Cancel & Exit
     Exit the program
```

`🔹` marks the highest-rated command and is pre-selected, so pressing Enter runs
it. The percentages come from the decision model. Press Enter:

```
▶ df -h /
────────────────────────────────────────────────────────────
Filesystem        Size    Used   Avail Capacity iused ifree %iused  Mounted on
/dev/disk3s1s1   926Gi    12Gi   467Gi     3%    459k  4.3G    0%   /
────────────────────────────────────────────────────────────
✔ done
```

## Why two models

Drafting commands and choosing between them are different jobs.

A general LLM is good at the first and unreliable at the second: ask it to also
rank its own suggestions and you get prose, or a confident pick with no numbers
behind it. So `bashai` splits the work. The LLM proposes; [bashai](https://github.com/freedomson/bashai)
sends the candidates to a decision model that returns a probability for each one.
That is what fills the percentage column, and what decides which row starts
selected.

The decision model is optional. Without it you still get the list, just unranked.

## Install

Go 1.27 or newer.

```sh
git clone https://github.com/kataras/jev
cd jev
go run ./bashai
```

To build a binary, send it to `bin/` — `go build ./bashai` would try to write a
file named `bashai` over the source directory.

```sh
go build -o bin/bashai ./bashai
```

## Setup with Ollama

[Ollama](https://ollama.com) serves the drafting model. Any instruct model works;
smaller ones are faster and usually fine, since the output is a short JSON object.

```sh
ollama serve
ollama pull qwen2.5-coder:7b
```

Ollama exposes an OpenAI-compatible endpoint on port 11434. Point `bashai` at it
in `bashai.json`:

```json
{
  "url": "http://127.0.0.1:11434/v1/completions",
  "model": "qwen2.5-coder:7b"
}
```

Run `go run ./bashai` and the file is created for you on first start with every
field at its default, so you only need to edit the two lines above.

That alone is enough to use `bashai`. Commands appear without percentages, the
first one is pre-selected, and the reason is stated rather than left as a silent
zero:

```
no scores: TYPESAFE_API_KEY is not set, commands are unranked
```

## Adding the decision model

The ranking comes from a jev-compatible server — Ollay — serving a decision model
such as `kev` on port 11435. Start it, then tell `bashai` where it is:

```json
{
  "jev_base_url": "http://localhost:11435",
  "jev_model": "kev"
}
```

The jev client always sends an API key, including to a local server, so set one:

```sh
echo 'TYPESAFE_API_KEY=local' > .env
chmod 600 .env
```

`.env` is read from the working directory and is already in `.gitignore`. A real
environment variable wins over the file, so `export TYPESAFE_API_KEY=...` also
works. Values are never printed — the banner reports only a count:

```
env: loaded 1 variable(s) from .env
```

With both servers up, the percentages appear and the best-rated command is
pre-selected.

## Talking to it over HTTP

The same suggestions are served on `127.0.0.1:8770` while the CLI runs. Spaces
must be encoded in a URL, so the friendliest form sends the query as the body:

```sh
curl -d 'show me what is using the most disk space' http://127.0.0.1:8770/commands
```

```json
{
  "query": "show me what is using the most disk space",
  "host": "Host: darwin/arm64, macOS 26.6.2, BSD userland, shell zsh",
  "recommended": "disk_space",
  "classified": true,
  "commands": [
    {
      "key": "disk_space",
      "command": "df -h",
      "score": 0.449
    },
    {
      "key": "disk_usage",
      "command": "du -ah --max-depth=1 / | sort -rh | head -n 20",
      "score": 0.2293
    },
    {
      "key": "du_top",
      "command": "du -ah | sort -rh | head -n 20",
      "score": 0.3217
    }
  ]
}
```

Three call styles are accepted:

```sh
curl --get --data-urlencode "q=which processes use the most memory" http://127.0.0.1:8770/commands
curl -d '{"query":"which processes use the most memory"}'              http://127.0.0.1:8770/commands
curl -d 'which processes use the most memory'                          http://127.0.0.1:8770/commands
```

`GET /` returns the same list of examples. `score` is omitted, and `classified`
is `false`, whenever the decision model was not consulted — that is deliberate,
so an unranked command is never mistaken for one rated zero.

The endpoint returns commands and never runs them. It binds to loopback because
its output is model-generated text that becomes shell input if a caller pipes it
somewhere. There is no authentication, so think before changing `listen`.

## The session remembers

Each turn carries the last few queries, the command you ran and its output into
the next request, so follow-ups work:

```
query: now sort them by size
🧵 carrying 2 earlier turn(s) as context
```

`max_turns` controls how many turns are replayed; `0` disables it. Captured
output is trimmed to `output_limit` bytes.

Command output is untrusted input being fed back to a model. It is fenced and
labelled as inert data in the prompt, which reduces the risk of a crafted
filename or log line steering the next suggestion, but does not remove it. The
picker is the safeguard: read the command before you press Enter.

## Telling it about the host

The first line of every prompt describes the machine, so the model suggests
`du -d 1` on macOS rather than GNU's `du --max-depth=1`:

```
Host: darwin/arm64, macOS 26.6.2, BSD userland, shell zsh
```

This is detected automatically. When you are driving a remote box, pin it with
`BASHAI_HOST`, which wins over both the config file and detection:

```sh
# linux
export BASHAI_HOST="linux/$(uname -m), $(. /etc/os-release; echo $PRETTY_NAME), GNU userland, shell $(basename $SHELL)"
```

```powershell
# windows, PowerShell
$env:BASHAI_HOST = "windows/$env:PROCESSOR_ARCHITECTURE, $((Get-CimInstance Win32_OperatingSystem).Caption), PowerShell"
```

`bashai` prints both lines on first run.

## Keys

| Key | Action |
| --- | --- |
| `↑` `↓` | Move |
| `Enter` | Run the selected command |
| `Esc`, `q` | Quit |

At the query prompt, `↑`/`↓` browse history, which persists between sessions, and
`Ctrl+R` searches it. Set `"vim_mode": true` for vim keybindings; there, arrow
keys cannot reach history because readline consumes the escape, so use `Esc` then
`k`/`j`.

Mouse support is off by default so you can select text in the terminal. Turn it
on with `"mouse": true`.

## Configuration

`bashai.json` in the working directory, or wherever `BASHAI_CONFIG` points.
Missing fields keep their defaults. A copy lives in
[bashai/bashai.example.json](bashai/bashai.example.json).

| Field | Default | What it does |
| --- | --- | --- |
| `url` | `http://127.0.0.1:8080/v1/completions` | Drafting model endpoint |
| `model` | `Qwen3.6-35B-A3B-Q4_K_M` | Drafting model name |
| `jev_base_url` | `http://localhost:11435` | Decision model server |
| `jev_model` | `kev` | Decision model name |
| `max_turns` | `3` | Turns of context replayed |
| `timeout_seconds` | `180` | Per request, not per session |
| `output_limit` | `4000` | Bytes of command output kept |
| `history_limit` | `1000` | Query history entries |
| `max_tokens` | `1024` | Drafting model budget |
| `listen` | `127.0.0.1:8770` | HTTP endpoint; `""` disables |
| `mouse` | `false` | Mouse capture in the picker |
| `vim_mode` | `false` | Vim keybindings at the prompt |
| `host` | `""` | Override the host line |

| Variable | Purpose |
| --- | --- |
| `TYPESAFE_API_KEY` | Required for ranking; put it in `.env` |
| `BASHAI_CONFIG` | Config file path |
| `BASHAI_ENV_FILE` | Env file path |
| `BASHAI_HOST` | Host line; wins over config and detection |
| `BASHAI_VIM` | `1` enables vim keybindings |

## The library

`bashai` is built on `github.com/kataras/jev`, a Go client for the TypeSafe
System One API. If you want the classification layer in your own program, the API
docs are on [pkg.go.dev](https://github.com/freedomson/bashai) and there is
an agent skill in [skills/jev/SKILL.md](skills/jev/SKILL.md).

The short version — send a state and named questions, get a probability for each:

```go
client, err := jev.New()
answer, err := client.Classify(ctx, state, "Which team should handle this?", map[string]any{
    "billing":   "Payments, invoices, refunds",
    "technical": nil,
})
// answer.Choice, answer.Confidence, answer.Probabilities
```

## License

[MIT](LICENSE).
