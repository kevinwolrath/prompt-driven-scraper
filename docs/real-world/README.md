# Real-world verification — 1 November 2026

The current application was run on Windows against live public pages, using its normal configuration validation, Session creation and Run APIs. These are bounded first-page samples, not exhaustive searches.

| Search | Acquisition | Found | Accepted | Rejected |
| --- | --- | ---: | ---: | ---: |
| Carsales VIC dealer Toyota Prius under $10k | HTTP and browser blocked | 0 | 0 | 0 |
| Autotrader VIC dealer Toyota Prius under $10k | HTTP succeeded | 20 | 1 | 19 |
| Autotrader NSW Toyota Prius greater than $10,000 | HTTP succeeded | 10 | 10 | 0 |

## Carsales blocker

An isolated Chrome instance obtained a 403 top-level document, loading a DataDome CAPTCHA iframe. The screenshot explicitly says **You have been blocked**. The DOM contains `captcha-delivery.com` and an iframe titled `DataDome CAPTCHA`; it contains no vehicle cards. No challenge was solved and no stealth flags, proxies or cookies from personal browser sessions were used.

The application then reproduced this result through its actual HTTP → browser acquisition pipeline. Final run `job-20261101-030312-814d90fa` reports one logical page attempted, zero loaded, two acquisition attempts, HTTP 403 on both, the block marker, browser title, final URL, 1540 DOM bytes, and the configured selector wait timing out. Extraction and filtering were not reached. See [diagnostics](carsales-diagnostics.txt) and [screenshot](carsales-blocked.png).

This is an access-control blocker, not evidence that the extraction rules work. Carsales profile selectors remain unverified. The next sensible step is a permitted data feed/API or another public source; changing selectors cannot fix this refusal.

## Autotrader fix and records

The previous profile selected `article`; the actual public HTML has `section.carListing--wrapper` cards. No runtime extraction capability was missing. The new declarative `autotrader-public` profile uses observed selectors and reads make/model from each card URL, state from its displayed location and seller type from its displayed label. It does not assign the requested constraints as constants. The advertised-price span excludes previous prices and finance text. All constraints are still deterministic record filters.

The browser probe happened to receive a 403 on Autotrader while a normal HTTP request to the same used-search URL returned 200 and full server-rendered cards. The working profile therefore uses HTTP alone; browser acquisition is unnecessary here.

Three accepted records were checked against their live detail pages, including their Product JSON-LD offer price:

| Listing | Location | Extracted and source price |
| --- | --- | ---: |
| 2007 Toyota Prius Hybrid, listing 15296850 | Coburg North, VIC | $5,990 |
| 2016 Toyota Prius ALPHA S HYBRID, listing 15354943 | Lidcombe, NSW | $19,900 |
| 2012 Toyota Prius Hybrid, listing 15352636 | Minchinbury, NSW | $16,995 |

Accepted records with source URLs and field provenance: [VIC](autotrader-vic-records.json), [NSW](autotrader-nsw-records.json). The VIC run rejected nineteen over-budget cards. This demonstrates authoritative record filtering rather than trusting a source query.

## Persistence and UI

The test API uses `data/realworld.db` on port 8081, independently of the existing API on 8080. Only the test-owned process was stopped and restarted. Both finished runs reopened in a newly launched automated Chrome instance and displayed their persisted record tables and stage diagnostics: [VIC screenshot](autotrader-vic-after-restart.png), [NSW screenshot](autotrader-nsw-after-restart.png).

- VIC Session: `ses-20261101-025926-bf28f0eb`, Run `job-20261101-025926-51802be1`.
- NSW Session: `ses-20261101-025926-06334d11`, Run `job-20261101-025926-398973ba`.
- Carsales Session: `ses-20261101-025926-01fd8fdb`.

The existing **Copy run diagnostics** button was exercised using a real automated click in the isolated headless browser. The button displayed **Copied**, and reading that browser's clipboard returned a 6748-byte run report. JavaScript-only clicks lacked clipboard user activation; the automated click verified the actual user path.

## Generic changes and validation

Browser timeout errors now preserve final URL, title, status and acquired DOM for the engine's evidence. Acquisition events retain those metadata on failure and explicitly include challenge status and marker. Copied diagnostics now include chronological attempt data, and redact header values, named credentials and URL authentication/query credentials without modifying the stored snapshot.

No site-specific logic was added to the Go runtime. Reproducible configurations are in `examples/real-world/`; public source knowledge is in the YAML profile.

Validation: full `go test ./...` suite (including actual Chrome tests), targeted browser/engine/diagnostics tests, and `npm run build`. Existing demo, storage/restart and Pause/Resume/Cancel/finalise tests pass. Live evidence above is separate from those deterministic regression tests.
