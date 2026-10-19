# Web acceptance harness

These scripts exercise a local `svntui web` binary using real temporary SVN repositories and an isolated installed browser. Main workflow actions use the UI; native SVN checks repository revisions, changed paths, bytes, properties and conflict flags. API protocol checks are reported separately.

The scripts create test data and perform destructive SVN operations **only in newly generated fixtures**. Use a fresh fixture for each workflow suite. No script installs a runtime, starts a remote device, publishes anything, or deletes a fixture/evidence directory.

## Requirements and configuration

- Python 3.9+ and native `svn` / `svnadmin` on PATH. Optional `svnmucc` speeds up the real 125-revision seed.
- Node 24 and an installed Playwright module (acceptance used 1.62.1).
- An installed native Chromium-family browser. Chrome, Chromium and Windows Edge use Playwright's Chromium driver; this does not test Safari.
- A built `svntui` containing the frontend assets under review.

Set paths for the current host; there are no account-specific defaults:

```sh
export SVNTUI_PLAYWRIGHT=/absolute/path/to/node_modules/playwright
export SVNTUI_CHROMIUM=/absolute/path/to/native/browser
export SVNTUI_NODE=/absolute/path/to/node
export SVNTUI_PYTHON=/absolute/path/to/python3
export SVNTUI_BINARY=/absolute/path/to/svntui
```

`SVNTUI_PLAYWRIGHT` may be omitted when `require('playwright')` resolves normally. Instead of `SVNTUI_CHROMIUM`, `SVNTUI_BROWSER_CHANNEL=chrome` or `msedge` selects an installed Playwright channel. `SVNTUI_SVN` optionally overrides the SVN executable used by native postcondition checks; fixture creation uses SVN tools on PATH.

`SVNTUI_CHROMIUM_ARGS` is an optional JSON array of process arguments. On Linux its default is `["--no-sandbox","--disable-dev-shm-usage"]`, scoped to the task-owned browser process. Other platforms default to `[]`. No browser or system settings are changed.

## Fixtures

```sh
python3 scripts/web-acceptance/fixture.py --mode basic
python3 scripts/web-acceptance/fixture.py --mode full
```

Each prints a `fixture.json` path inside a unique operating-system temporary directory. `--root <new-empty-directory>` selects an explicit root. Both modes create a repository, two WCs, isolated XDG config/state/cache and command evidence.

| Mode / option | Data |
| --- | --- |
| `--mode basic` | Modified selected/unselected and nested special-name paths, binary, add/move/revert/delete targets, untracked files and changelist; zero conflicts; no large file. |
| `--mode full` | All basic data plus real text/property/edit-delete tree conflicts and default 1,400,000-byte baseline/modified large text. |
| `--revisions 2` | Fast focused lifecycle fixture. Defaults to 125 real revisions for history paging. |
| `--large-bytes 0` | Omit the large file. |
| `--large-bytes 5000000` | Specialized fixture whose diff exceeds the shared 8 MiB SVN output cap. |
| `--verify <fixture.json>` | Read-only verification of the prepared state; writes evidence without changing the WC/repository. |

POSIX fixtures include the literal hostile markup filename and an outside-directory symlink. Windows uses legal ampersand/apostrophe filenames and the hostile commit message; unavailable symlink privilege is recorded explicitly. Commands that parse peg revisions use empty peg suffixes; working-copy diff operands remain literal.

For large paginated UI and unique-match checks, use the default full fixture. Its natural final `No newline at end of file` is a unique late match; no fixture edits are needed to compare builds.

## POSIX startup and main workflows

The bridge starts a fresh loopback server with the fixture's isolated XDG environment, obtains the secret startup URL through a 320-column PTY **in RAM**, and launches Node with the URL in its child environment. It ends only its owned server using Ctrl-C and waits for shutdown.

```sh
python3 scripts/web-acceptance/run-browser.py \
  --binary "$SVNTUI_BINARY" \
  --fixture /absolute/path/to/fixture.json \
  --node "$SVNTUI_NODE" \
  --script scripts/web-acceptance/workflows.cjs \
  --evidence /absolute/path/to/retained-evidence/basic-001
```

The runner reads basic/full mode from the manifest. `SVNTUI_ACCEPTANCE_MODE` explicitly overrides it. Basic workflows cover status/file filters, selection, diff views/search, literal markup, draft reload, selected nested Chinese/space/@ commit, failed-commit retention, project add/remove, changelists, add/move, modified-delete protection, clean-path delete, stale UI confirmation, revert, SafePoint inspection/restore, audit, selected/revision/root updates, cleanup and history filters/paging/diff. Full mode covers advertised conflict choices, marker protection and all three real conflict resolutions.

The default Node timeout is 600 seconds; `SVNTUI_BROWSER_TIMEOUT` changes it. `SVNTUI_ACCEPTANCE_START_AT=<step-name>` is diagnostic resume mode and labels earlier steps `not_run_in_resume`; it is not a full-suite pass.

### Draft survives a server restart

Run the same bridge twice against the same basic manifest and separate evidence directories:

```sh
export SVNTUI_ACCEPTANCE_PHASE=draft-write
# Run the bridge above with evidence .../draft-write
export SVNTUI_ACCEPTANCE_PHASE=draft-read
# Run it again with evidence .../draft-read
unset SVNTUI_ACCEPTANCE_PHASE
```

The first process saves a nonsecret checkpoint under the fixture root. The second starts a new server on a different random port and a fresh browser context, then checks the persisted selection/message. `SVNTUI_ACCEPTANCE_CHECKPOINT` selects another checkpoint path. No cookies or browser storage state are transferred.

## Native Windows

Use `run-browser-windows.py` with the same arguments. It uses standard-library ctypes ConPTY APIs, a 320-column private console, real console handles for the owned server, RAM-only startup capture and Ctrl-C/wait shutdown. Metadata records owned controller/worker/server PIDs, paths and binary hash for cleanup diagnosis.

```powershell
$env:SVNTUI_PLAYWRIGHT = 'C:\tools\node_modules\playwright'
$env:SVNTUI_CHROMIUM = 'C:\tools\browser\msedge.exe'
$env:SVNTUI_NODE = 'C:\tools\node\node.exe'
$env:SVNTUI_PYTHON = 'C:\tools\python\python.exe'
python scripts/web-acceptance/run-browser-windows.py `
  --binary C:\task\svntui.exe `
  --fixture C:\task\fixture\fixture.json `
  --node $env:SVNTUI_NODE `
  --script scripts/web-acceptance/workflows.cjs `
  --evidence C:\task\retained-evidence\basic-001
```

The ConPTY Web adaptation must be run natively on Windows before claiming a Windows pass. A `cleanup_required` result is not successful cleanup; use its owned PID/path metadata to diagnose it. No taskkill or unrelated process termination is used.

## Additional scripts

Select a different `--script` in either startup bridge:

| Script | Purpose |
| --- | --- |
| `browser-probe.cjs` | Authenticated initial UI discovery, waiting for fixture rows/error before a screenshot. Explicitly not workflow acceptance. |
| `protocol-checks.cjs` | API-only session replay, cookie policy, CSRF, Origin/Host, path/input allowlist, explicit output bound, stale previews, YES and duplicate execution/one-commit checks. Use a fresh basic fixture; full mode checks guards/output only. |
| `regressions.cjs` | Real component/network interleavings. Set `SVNTUI_REGRESSION_PHASE` to `scope`, `registration`, `draft-order`, `draft-order-reverse`, `escape`, or `status-failure-navigation`. Use a fresh basic two-revision fixture per mode. |
| `ui-reconnect.cjs` | Actual UI cleanup job, failed SSE handshake after UI reload, disconnected snapshot and automatic retry recovery. It does not assume Chromium offline emulation closes existing SSE sockets. |
| `ui-large-paged.cjs` | Same default full large fixture: unified/split display-row and DOM bounds, whole-input late match, cross-page previous/next matches, source line numbers, page clamp and measured response times. |
| `ui-single-hit.cjs` | Manual paging away from one unique late match, then next/previous match must return to its page in both views. |

`svn-fixture-check.py` is the JSON stdin/stdout native verification/setup helper used by runners. It is distinct from release/build verification tools. `verify-api.py --url <bare-origin> --evidence <dir>` is a read-only inspector; optional session bootstrap is its sole POST. `SVNTUI_STARTUP_TOKEN` supplies its token in memory. API checks do not count as UI operation passes.

### Two local servers in one browser context

On POSIX, the dedicated controller checks that authenticating B does not invalidate A:

```sh
python3 scripts/web-acceptance/run-two-servers.py \
  --binary "$SVNTUI_BINARY" \
  --fixture-a /absolute/path/to/a/fixture.json \
  --fixture-b /absolute/path/to/b/fixture.json \
  --node "$SVNTUI_NODE" \
  --evidence /absolute/path/to/retained-evidence/two-servers
```

It requires A200/B200/A200. `SVNTUI_COOKIE_BASELINE=1` is only for explicit old-build bug reproduction and is labelled as such.

## Evidence and completion

Evidence contains summaries, screenshots and sanitized method/path/status metadata. Startup URL fragments are removed before capture. No startup token, cookie/CSRF value, trace or storageState is written. Shared redaction also covers navigation-error messages from both servers.

Keep evidence outside fixtures, archive it and record the exact source/assets, binary hash, native tools and browser version before cleanup. The bridges record binary/script hashes; browser metadata records the observed browser/runtime. A pass only covers the script, fixture, binary and native platform actually executed. Do not infer Safari, another OS, a final package or a publication gate from a local Chrome run. The scripts leave all fixture/evidence cleanup to their owning controller.
