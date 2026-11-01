# User Browser live verification — 1 November 2026

## Actual Carsales outcome: manual verification required, zero records

The application opened the exact dealer/Toyota/Prius/VIC/under-$10,000 URL in a separately launched **visible Chrome**, using `data/user-browser` as its persistent profile. It received HTTP 403 and a `captcha-delivery.com` iframe. The visible page offers human verification (visual/audio controls, later a Retry button); no automation clicked those controls or solved the challenge.

The run checkpointed automatically with **Browser interaction required**, **Continue**, **Cancel**, and the retained tab ID. One logical page was attempted, zero pages acquired, and zero records extracted/accepted. Extraction and filters have **not** been verified against Carsales. Opening Chrome is not a successful scrape.

- Session: `ses-20261101-034138-6b4a7b7a`.
- Current Run #2: `job-20261101-035614-f14210c0`, paused for manual interaction.
- Local run UI: `http://localhost:8082/#/sessions/ses-20261101-034138-6b4a7b7a/runs/job-20261101-035614-f14210c0`.
- [Challenge screenshot](user-browser-carsales-challenge.png) (only the challenge header was captured for publication, excluding machine-specific identifiers).
- [Paused UI screenshot](user-browser-paused-ui.png).
- [Copied diagnostics](user-browser-carsales-diagnostics.txt).

The earlier Chrome instance was no longer running before its tab could be continued. That earlier run was retained as cancelled, and a new run reused the same persistent profile. A subsequent explicit lifecycle check confirmed that stopping the API leaves the current Carsales target alive; restarting the API preserved the paused interaction and its tab ID. Closing Chrome itself necessarily destroys the tab; the program reports an attachment error rather than fabricating its DOM.

The human handoff remains pending. Once the user completes the site's verification and the actual results page is visible, Continue will read the same tab. The inherited Carsales extraction profile remains unverified; it must be corrected from the actual listing DOM if necessary before claiming filtered real records. The example configuration is labelled as an acquisition reproduction, not a verified Carsales scraper.

## Autotrader regression: real records retained

The existing public HTTP profile was executed unchanged through the new API:

| Scenario | Final run | Found | Accepted | Rejected |
| --- | --- | ---: | ---: | ---: |
| VIC dealer Toyota Prius under $10k | `job-20261101-034922-1543ee01` | 20 | 1 | 19 |
| NSW Toyota Prius over $10k | `job-20261101-034922-efa48ea5` | 10 | 10 | 0 |

Both initial attempts received HTTP 403. A bounded repeat of each exact application request completed successfully, without changing headers, acquisition code or extraction rules. A separate Chrome probe received 403 while direct HTTP could receive 200: source availability varies. These refusals were retained in the run history instead of being hidden. Both final runs' records were retrieved again after the API restart.

The VIC result remains the 2007 Prius in Coburg North at $5,990. The [earlier live price cross-checks and public record artifacts](README.md) remain applicable; this regression verifies that introducing browser execution modes did not alter the existing deterministic pipeline.

## What is implemented and tested

- Session execution selector: Automatic, HTTP, Managed Browser, User Browser; immutable job-level `browserMode` snapshots and profile-level `user-browser` strategies.
- Ordinary visible Chrome with a persistent dedicated profile, and explicit local CDP attach via `PDS_USER_BROWSER_URL`.
- No scraping logic in acquisition: rendered HTML enters existing mapping, coercion, authoritative filters, dedupe, SQLite and JSON/CSV output.
- Checkpointed human handoff using existing paused/resume/finalise/cancel states. Continue reads the retained target without navigation/reload. Repeated challenges require another explicit Continue; logical page counts remain one across handoffs.
- Interaction requests, resumption, mode, URLs, available status, title, block marker and DOM size in copied diagnostics, with credential redaction preserved.
- Waiting state persists in progress/checkpoint JSON; crash recovery restores the interaction banner. No database migration needed.
- Local operator/Origin restrictions for the new browser-control surface.

The actual UI's Copy run diagnostics button was exercised in a separate headless verification browser; it displayed Copied and returned a 7066-byte report from that browser's clipboard. No personal browser session was controlled.

Automated local fixtures separately verify real Chrome CDP attachment, same-tab continuation after reconnect without refetch, persistent localStorage, no reads of another origin's DOM, queue cancellation, HTTP → User Browser fallback, SQLite restart/crash recovery, deterministic filters, Cancel, Give me what you've got, mode overrides and credential redaction. A real local demo run through visible User Browser produced 14 fixture records. Those fixture results are **not Carsales records**.

Validation: `go test ./...` and `npm run build --prefix apps/web`. See [setup, attach instructions, lifecycle and limitations](../USER_BROWSER.md).
