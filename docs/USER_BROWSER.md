# User Browser mode

User Browser is a generic acquisition strategy using Chrome DevTools Protocol. It launches ordinary visible Chrome with a dedicated persistent profile, or attaches to Chrome that the operator deliberately started with debugging enabled. It does not inspect unrelated existing tabs, copy everyday-profile cookies, solve CAPTCHAs, or modify the browser's identity.

## Start normally

```powershell
npm run build --prefix apps/web
go run ./apps/api -addr 127.0.0.1:8080
```

In a Session's editor choose **Browser mode → User Browser**, save, then Run. Chrome opens automatically when acquisition starts. The default persistent directory is `data/user-browser`, relative to the API's working directory. To choose another dedicated directory before starting the API:

```powershell
$env:PDS_USER_BROWSER_PROFILE = 'C:\dev-env\prompt-driven-scraper\data\user-browser'
```

`PDS_BROWSER_PATH` optionally supplies the Chrome executable. The default discovery includes `C:\Program Files\Google\Chrome\Application\chrome.exe`.

The dedicated profile starts empty on its first launch, then retains legitimate cookies, storage and browser preferences across requests and application restarts. It is deliberately separate from everyday Chrome: modern Chrome does not permit remote debugging of the default data directory, and a second process must not share a locked profile. See [Chrome's remote-debugging change](https://developer.chrome.com/blog/remote-debugging-port) and [Chromium profile documentation](https://chromium.googlesource.com/chromium/src/+/refs/heads/lkgr/docs/user_data_dir.md).

## Attach to deliberately started Chrome

Start a separate visible Chrome instance using a non-default profile:

```powershell
& 'C:\Program Files\Google\Chrome\Application\chrome.exe' '--remote-debugging-address=127.0.0.1' '--remote-debugging-port=9222' '--user-data-dir=C:\dev-env\prompt-driven-scraper\data\interactive-chrome' '--no-first-run' '--no-default-browser-check'
$env:PDS_USER_BROWSER_URL = 'http://127.0.0.1:9222'
go run ./apps/api -addr 127.0.0.1:8080
```

A full `ws://127.0.0.1:PORT/devtools/browser/ID` endpoint is also accepted. This is an operator environment setting, not a downloaded profile setting. Only loopback endpoints without embedded credentials are accepted. Keep the debugging port local: CDP grants control of that browser session. To return to automatic launching, remove the variable before restarting:

```powershell
Remove-Item Env:PDS_USER_BROWSER_URL
```

Do not point this feature at your everyday Chrome User Data directory. The automatic launcher refuses the standard Chrome directory and its children. The scraper creates and navigates its own tabs in the deliberately selected browser; it never chooses arbitrary existing personal tabs.

## Modes and profile configuration

- **Automatic** follows each source's configured ordered strategies; omitted acquisition means HTTP.
- **HTTP** overrides all sources to HTTP.
- **Managed Browser** overrides all sources to the existing managed Chrome strategy.
- **User Browser** overrides all sources to visible persistent Chrome. Existing browser wait settings are reused; otherwise the listing selector is used as the expected page state.

JSON sources reject browser execution modes because browser acquisition produces HTML. In mixed-source jobs, leave Automatic selected and configure only the HTML source's strategies when necessary.

A source may opt into a generic interactive fallback without changing the entire job:

```yaml
acquisition:
  strategies:
    - type: http
    - type: browser
      waitFor: "article.listing-card"
    - type: user-browser
      waitFor: "article.listing-card"
      timeoutSeconds: 30
```

Fallback eligibility remains HTTP 401/403/429 or a recognised challenge, as before. User Browser itself requests interaction for a blocked page or missing expected page state. A missing selector can be a profile bug; inspect diagnostics rather than repeatedly pressing Continue.

## Manual interaction and Continue

1. A challenge or refusal checkpoints the run as **paused**. The UI displays **Browser interaction required**, its URL/title/reason, **Continue**, and **Cancel**.
2. Interact with the visible scraper-owned Chrome tab manually. The program performs no automatic actions on that tab while paused.
3. Once the actual source page is ready, press **Continue**. The program reads the same tab without navigating or reloading it. It then runs the same deterministic extraction, type conversion, record filters, dedupe and SQLite/output pipeline used by HTTP.
4. If still blocked, it pauses again. Each attempt requires an explicit Continue. A browser opening is not successful acquisition; extracted and persisted records remain the acceptance criterion.

Pause, Cancel and Give me what you've got retain their existing meanings. Finalising while waiting salvages previously fetched pending records; a blocked page contributes no invented records. Finished/cancelled runs clear the interaction prompt. A paused run excludes human waiting time from running duration.

## Lifecycle and persistence

Interaction URL, reason, title and target/tab ID are stored in the run's progress JSON and checkpoint; interaction request/resume events are appended chronologically. Existing SQLite tables already store those documents, so no migration is required. The browser mode is part of the immutable run configuration.

The API does not close the externally running visible Chrome or pending interaction tabs on shutdown. After an API restart, Continue reconnects to the retained tab ID. This also works when attaching to an explicitly started Chrome endpoint. Successful acquisition closes the scraper-owned work tab; later requests create new tabs in the same persistent browser profile. Cancelled/failed interaction tabs remain available for inspection and can be closed manually. The visible browser stays open until the user closes it.

If Chrome itself or the waiting tab was closed, Continue reports an attachment error; start a new run after reopening Chrome. Browser state on disk persists, but a vanished tab's exact DOM cannot be restored. An HTTP status may be unavailable when attaching to a page loaded while the API was disconnected; diagnostics omit unknown status rather than inventing 200. Only the requested origin's DOM is read; navigate back to the source before continuing from another origin.

User Browser start/Continue API calls require a local operator connection. User Browser operations reject unrelated web origins; the existing HTTP API CORS behaviour is preserved. This feature is intended for local interactive execution, not unattended hosted workers.

## Diagnostics and verification

Copied run diagnostics include selected browser mode, actual acquisition strategy, requested/final URL, available status, title, DOM size, block marker, interaction request and explicit continuation, plus extraction/filtering counts. Credential and URL redaction still applies; raw cookies, CDP endpoints and full browser DOM are not included in copied reports.

See [live evidence](real-world/USER_BROWSER_VERIFICATION.md). Automated local-fixture tests verify CDP attachment, persistent localStorage, retained-tab continuation after reconnect, engine checkpoint/SQLite restart, filtering, cancellation, finalising, mode overrides and credential redaction. Those fixtures do not establish access to Carsales; the live run is reported separately.
