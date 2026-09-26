---
name: record-demo
description: Re-record and publish tingly-ssh's promotional demo (the side-by-side "plain ssh vs ssh + tingly-ssh" recording) and keep the README GIF, the website player and its chapter markers in sync. Use when the demo is stale (CLI output, flags or reconnect behaviour changed), when asked to make or refresh promo material, a GIF, a screencast or the website demo, or when adding a new demo scenario.
---

# Recording and publishing the demo

The demo is the project's main pitch, so it follows two rules:

1. **Real, not mocked.** It records real `ssh` against a real sshd, through real network events
   (an address change and a link going down in a network namespace). Never hand-edit a `.cast`,
   never fake output, never speed up only one side.
2. **Every claim is checked.** `demo/record.sh` fails unless every progress update of the remote job
   reached the tingly-ssh terminal in order and exactly once, and only then shows the closing caption. If a caption, README or website sentence claims something, the
   recording must show it.

## Files

| File | Role |
| --- | --- |
| `demo/record.sh` | Builds the binary, sets up the namespace, sshd and tunnel server, drives tmux, records, and verifies continuity. Needs root. |
| `demo/cast.py` | A dependency-free asciicast v2 recorder (a pty plus timestamps). Works headless. |
| `demo/snapshot.py` | Prints the screen at chosen seconds of a cast (`pip install pyte`), for reviewing without a browser. |
| `demo/out/` | Output, gitignored: `demo.cast`, `markers.json`, `stdout-devbox-tingly.log` (the exact bytes ssh wrote, which the check reads), `tingly.txt` / `plain-ssh.txt` (pane scrollback), logs. |
| `docs/assets/demo.gif` | The README GIF, rendered from the cast. |
| `site/public/demo.cast` | The website player's copy of the cast. |
| `site/src/assets/demo-markers.json` | Chapter times (`switch`, `outage`, `back`) for the website buttons. |

## Steps

1. **Environment**: a disposable Linux VM or container, as root, with `iproute2`, `openssh-server`,
   `openssh-client`, `tmux`, `bc`, `python3` and Go. In a fresh container:
   `apt-get install -y openssh-server openssh-client iproute2 tmux bc`.
2. **Record**: `sudo ./demo/record.sh` (about 60 s). Knobs: `OUTAGE=15`, `JOB_STEPS=150`
   (0.3 s each, so the job runs 45 s and has to outlive the outage), `RESUME_WAIT=40` (the longest wait for
   the job to finish), `COLS=112 ROWS=9`. It must end with
   `continuity: 150/150 progress updates, in order, none repeated`. If it doesn't, that is a product bug
   to investigate, not a recording to retake until it passes.
3. **Review** the story as text: `demo/snapshot.py demo/out/demo.cast 12 25 40`. Check that the left bar
   freezes and ssh dies after the IP change (`Timeout, server ... not responding`), that the right bar
   pauses during the outage, jumps forward to where the job really is, and reaches `✓ done`, and that no log noise leaks into the panes.
4. **Render the GIF** with [agg](https://github.com/asciinema/agg) (a static binary from its releases page):
   `agg --font-size 15 --theme github-dark demo/out/demo.cast docs/assets/demo.gif`. Keep it under about 1 MB.
   Look at a few frames (for example extract them with Pillow) before you commit.
5. **Publish to the site**:
   `cp demo/out/demo.cast site/public/demo.cast && cp demo/out/markers.json site/src/assets/demo-markers.json`,
   then `cd site && npm ci && npm run build`.
6. **Check the site** in Chromium via Playwright (`executablePath` from `/opt/pw-browsers` if the
   project's version differs): no console errors, `scrollWidth` equal to the viewport at 390 px and
   1280 px, and the chapter buttons land on the right moments, in dark and light, `?lang=en` and `?lang=zh`.
7. **Sync the words**: if timings or behaviour changed (outage length, the plain-ssh failure message,
   linger), update the captions in `record.sh`, the GIF caption in both READMEs, and `demo` in
   `site/src/content/{en,zh}.ts`. The two languages always change together.

## Gotchas learned the hard way

- **ssh ignores `$HOME`** for `~/.ssh/config`, because it reads the passwd entry. `record.sh` puts a
  wrapper `ssh` that passes `-F` first on the laptop `PATH`.
- **Deleting a primary IPv4 address also deletes its secondaries.** Delete the old address *before*
  adding the new one, or the "new network" silently vanishes too.
- **A tmux pane split from outside tmux takes the tmux *client's* environment**, not the server's.
  Every pane command spells out the laptop environment (`PANE_CMD`).
- **The proxy's stderr is the user's terminal.** At the default `warn` level, reconnect attempts print
  into the ssh pane. The demo passes `--log-level error` in the `ProxyCommand` and says so openly in
  the ssh config it shows.
- **Reconnect backoff grows during an outage** (capped at 15 s ±20% jitter, `internal/bridge/client.go`), so recovery after
  the network returns can lag by that much. Keep `RESUME_WAIT` long enough to show the catch-up.
- tmux redraws in bursts and a progress bar overwrites itself, so neither the cast nor the pane can
  prove continuity. The laptop's `ssh` wrapper tees stdout to `stdout-<host>.log`, and the check reads that.
- **Never edit `record.sh` while it runs.** bash reads scripts incrementally, and a mid-run edit fails
  with nonsense such as `tdemo1: command not found`.
- Why a progress bar and not numbered lines: viewers read "frozen at 15%, gave up" versus "paused,
  caught up, finished" at a glance, while numbered lines only prove something if you count them.

## Adding a scenario

Add it to `record.sh` as a `mark <key>` + `caption ...` + network action block. Add the new chapter
key to `Content["demo"]["chapters"]` in `site/src/content/types.ts` (TypeScript then flags both
languages until you add the label). Extend the continuity check if the scenario makes a new claim.
