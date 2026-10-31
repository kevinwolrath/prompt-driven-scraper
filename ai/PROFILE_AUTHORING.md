# Prompt Driven Scraper — instructions for AI assistants

You are helping a user configure **Prompt Driven Scraper**, a deterministic, profile-driven scraper.
Read this whole document before writing any configuration.

## 1. Your role: you configure the scraper, you do not run it

- The scraper runtime is a deterministic program. It never calls an LLM, Claude, ChatGPT or any AI
  service, and the configuration you write must not depend on one.
- You produce **configuration** (YAML or JSON). The user pastes it into the Prompt Driven Scraper UI
  ("Load AI-generated configuration") or runs it with `scraper run job.yaml`.
- Do **not** present scraped data, prices or listings as results. You have not run the scraper. If
  you looked at a website to write a profile, say what you inspected and that the selectors still
  need to be verified by running the job.
- The same configuration against the same website content must always produce the same records.
  Never write rules whose outcome depends on judgement, "best guess", time of day or randomness.

## 2. Only emit supported DSL features

The runtime decodes configuration **strictly**:

- Unknown keys are errors, not warnings. An invented key (for example `javascript:`, `llm:`,
  `xpath:`, `waitFor:`, `click:`) makes the whole configuration fail to load.
- Unknown transform names are errors.
- Configuration must conform to [`schemas/job.schema.json`](../schemas/job.schema.json) and
  [`schemas/source-profile.schema.json`](../schemas/source-profile.schema.json).

If a requirement cannot be expressed with the features in this document, **do not invent syntax**.
Write the closest valid configuration you can, and report the gap in a separate
"Unsupported requirements" section of your reply (see §9).

## 3. The three user-facing concepts

A job has exactly three concepts:

| Concept | Key | What it is |
| --- | --- | --- |
| QueryInput | `queryInput` (+ `query`) | What the user wants, in their words, plus its deterministic form. |
| SearchSources | `searchSources` | The sources to search. Each one has a **source profile**. |
| Output | `output` | The generic records the user wants: field names, types, format. |

Mapping between a website and the output fields lives **inside each source profile**. Do not create
any other top-level concepts.

### Job shape

```yaml
name: EV cars 2024+                      # required
queryInput: "I'm only looking for EV cars after 2024"   # the user's words, verbatim
query:
  params:                                # generic named values; profiles map them to requests
    fuel: electric
    yearMin: 2024
  filters:                               # applied to EVERY record after type conversion
    - { field: fuelType, op: eq, value: electric }
    - { field: year, op: gte, value: 2024 }
searchSources:
  - id: some-site
    profile: { ... }                     # inline source profile (see §4) ...
  - id: demo-motors
    profileRef: demo-motors              # ... or a reference to a profile the user already has
output:
  format: csv                            # csv | json
  fields:                                # compact form: name -> type, "?" = optional
    make: string
    model: string
    year: integer
    price: decimal
    kilometres: integer
    batterySizeKwh: decimal?
    url: url
  dedupeBy: [url]                        # optional; defaults to [url] when a url field exists
```

- `query.params` keys are **generic** names you choose (`fuel`, `yearMin`, `priceMax`, `state`). Use
  the same names across all sources. Each profile decides how (or whether) to send them.
- `query.filters` are the guarantee: sources often ignore or only partly honour search parameters,
  so every hard constraint in the user's request should also be a filter.
  - ops: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in` (value is a list), `contains` (case-insensitive)
  - text comparisons are case-insensitive; numbers compare numerically
  - `missing: pass` lets records without the field through (default `reject`)
  - a filter field must be an output field — add one (e.g. `fuelType`) if needed
- Output types: `string`, `integer`, `decimal`, `boolean`, `url` (absolute http/https). There is no
  date type yet. Fields without `?` are **required**: records missing them are rejected, so only
  make a field required if every valid record will have it.
- The list form is also accepted: `fields: [{ name: make, type: string, required: true }, ...]`.

## 4. Source profiles

A source profile owns **all** site-specific knowledge: URLs, request parameters, pagination,
listing discovery, detail pages, extraction candidates, rejection rules, precedence and quirks.

```yaml
id: example-cars                         # lowercase, digits, hyphens
name: Example Cars
format: html                             # html (default) | json  (format of every page fetched)

request:
  url: https://www.example.com/cars/search     # absolute http(s) URL
  params:                                       # request query-string parameters
    fuelType:                                   # parameter name the SITE uses
      from: fuel                                # generic query param name
      map: { electric: EV, petrol: PETROL }     # translate generic values to site values
      unmapped: omit                            # omit (default, warns) | pass | fail
    yearFrom: { from: yearMin }
    sort: { value: newest }                     # constant parameter
  headers: { Accept-Language: en-AU }           # optional

limits:
  maxPages: 20          # listing pages per source
  maxRecords: 500       # listing items processed per source
  delayMs: 1500         # politeness delay between requests (default 500)
  retries: 1            # retries for network errors, HTTP 429 and 5xx
  timeoutSeconds: 30

pagination:             # optional; omit for single-page sources
  next: { from: listing, css: "a[rel=next]", attr: href }   # follow a link ...
  # param: { name: page, start: 1, step: 1 }               # ... OR increment a parameter (not both)
  totalResults: { from: listing, css: ".result-count", transform: [number, integer] }
  pageSize: 24          # required with totalResults; lets the UI show real progress
  # totalPages: { from: listing, css: ".pages .last" }     # alternative to totalResults

listing:
  items: "article.listing-card"     # html: CSS selector, json: JSON path to the array
  skip:                             # drop items that are not real records
    - css: ".sponsored"
    - contains: "Advertisement"
  detailLink: { css: "a.title", attr: href }   # relative links are resolved automatically
  followDetail: true                # fetch each detail page; enables `from: detail`

ignore:                             # html only: remove these elements from every page first
  - ".finance-calculator"
  - "header"

fields:                             # output field name -> rule (see §5)
  make: { css: ".make" }
  ...

notes:                              # free text for humans; ignored by the engine
  - Server ignores yearFrom for private sellers; year is enforced by query.filters.
```

How the engine runs a source, in this exact order:

1. Build the first URL from `request.url` + `request.params` (+ the page parameter if any).
2. Fetch listing pages one at a time, select `listing.items`, drop `listing.skip` matches.
3. For each item: evaluate `item`/`listing` candidates; if `followDetail`, queue the detail page.
4. Detail pages are fetched in listing order before the next listing page.
5. Fields are chosen (§5), converted to output types, filtered, deduplicated, then accepted.

Pagination stops when a page has no items, there is no next link, the known page total or
`maxPages` is reached. If a source does not expose a total, leave `totalResults`/`totalPages`
unset; the UI then shows factual counts instead of a percentage. Never fake a total.

## 5. Field rules

A field rule is either **one inline candidate** or an ordered **`candidates`** list, plus optional
`reject`, `prefer` and `transform`:

```yaml
price:
  candidates:
    - { from: detail, jsonld: offers.price, jsonldType: Car }   # structured data first
    - { from: detail, css: ".price-driveaway" }
    - { css: ".price span" }                                    # listing card last
  reject:
    - css: ".finance-repayment"     # value came from inside this element
    - contains: "per week"
    - contains: "per month"
    - max: 2000                     # numeric value <= 2000 (after transforms)
  prefer:
    - contains: "drive away"
  transform: [currency, decimal]
```

### Candidates (exactly one source per candidate)

| Key | Reads | Notes |
| --- | --- | --- |
| `css` (+ optional `attr`) | Text (or attribute) of every element matching the CSS selector, in document order | Standard CSS selectors only. No XPath, no `:contains()`-style text selectors |
| `attr` alone | An attribute of the context element itself | e.g. `{ attr: data-id }` on the listing item |
| `css` + `json` | Parses the selected element's text (or `attr`) as JSON, then applies the path | For `<script id="__NEXT_DATA__">` and `data-props='{...}'` |
| `jsonld` (+ `jsonldType`) | schema.org JSON-LD blocks (`@graph` and arrays flattened) | Filter with `jsonldType: Car`/`Product`/`Offer` |
| `json` | JSON path into a JSON page or JSON listing item | For `format: json` sources |
| `meta` | `<meta name|property|itemprop=...>` `content` | |
| `const` | A constant | e.g. `fuelType: { const: electric }` for an EV-only source |
| `builtin` | `detailUrl`, `pageUrl`, `listingUrl`, `sourceId` | `url: { builtin: detailUrl }` is the usual record URL |

`from` picks the context: `item` (default — the listing item element/object), `listing` (the whole
listing page) or `detail` (the detail page; requires `listing.followDetail: true`). Pagination rules
(`next`, `totalResults`, `totalPages`) must use `from: listing`.

**JSON paths** are dot paths: `offers.price`, `results.0.url`. A key applied to an array applies to
every element. A predicate keeps matching objects: `pricing[type=driveaway].amount`,
`offers[@type=Offer].price`. No wildcards, recursion or expressions.

### Precedence (deterministic)

1. Every candidate is evaluated; each may yield several values (e.g. a CSS selector matching three
   elements).
2. Each value is transformed, then checked against `reject`. Values that fail a transform or match
   a reject rule are discarded (and reported in the record's notes).
3. If any surviving value matches a `prefer` rule, the first such value (in candidate order, then
   document order) wins.
4. Otherwise the first surviving value wins.

So **candidate order is precedence**. "Detail page overrides listing page" = list `from: detail`
candidates first. "Prefer structured data" = list `jsonld`/`json` candidates before `css`.

### Matchers (`reject`, `prefer`, `listing.skip`)

Each matcher object may combine keys; **all** keys in one object must hold. A list matches if
**any** object matches.

| Key | Meaning |
| --- | --- |
| `contains` | raw text contains the string (case-insensitive unless `caseSensitive: true`) |
| `equals` | trimmed raw text equals the string |
| `regex` | Go RE2 regular expression on the raw text (case-insensitive by default) |
| `css` | in `reject`/`prefer`: the value's element **or an ancestor** matches. In `listing.skip`: the item **or a descendant** matches |
| `min` / `max` | the transformed numeric value is ≥ min / ≤ max (only in `reject`/`prefer`) |

### Transforms (closed list)

Write as a bare name or a single-key object. Applied in order; a failure discards the value.

| Transform | Effect |
| --- | --- |
| `trim`, `lower`, `upper`, `collapseWhitespace` | text clean-up (element text is already whitespace-collapsed) |
| `regex: "pattern"` | keep capture group 1 (or the whole match); **no match discards the value** |
| `replace: { from: "x", to: "y" }` | literal replace-all |
| `map: { EV: electric, Petrol: petrol }` | exact, trimmed, case-insensitive lookup; unmapped values are discarded |
| `number` | first number in the text, thousands separators removed (`"12,345 km"` → `12345`) |
| `currency` | same as `number`, for prices (`"$89,990 drive away"` → `89990`) |
| `integer`, `decimal`, `boolean` | type conversion (`boolean` accepts true/false/yes/no/y/n/1/0) |
| `absoluteUrl` | resolve against the page the value came from |

Text must be converted before it can satisfy a numeric output type: use
`[number, integer]` or `[currency, decimal]`.

## 6. Encode negative knowledge

Real sites contain values that look right and are wrong. Express what you know as rules:

| Situation | Rule |
| --- | --- |
| Weekly/monthly finance repayment near the price | `reject: [{ css: ".finance" }, { contains: "per week" }, { contains: "/mth" }]` plus a sanity `max` |
| Misleading metadata (e.g. a meta price that is the repayment) | do not list it as a candidate; add a `notes` entry saying why |
| Header promos, calculators, "similar cars" carousels | `ignore: [".promo", ".similar-vehicles"]` |
| Ads/sponsored tiles in the grid | `listing.skip: [{ css: ".sponsored" }]` |
| Several prices, only "drive away" is comparable | `prefer: [{ contains: "drive away" }]` |
| Structured data is more reliable than visible text | put `jsonld`/`json` candidates first |
| Detail page is more complete than the listing | put `from: detail` candidates first, keep listing candidates as fallbacks |
| Site filter is unreliable | still map the param, and add a `query.filters` entry |
| Value present only for some records | make the output field optional (`decimal?`) |

Prefer listing-page fallbacks for every important field: if a detail page fails, or the user asks
"give me what you've got", the record is built from listing values alone.

## 7. Determinism rules

- Do not use randomness, dates relative to "now", or anything that depends on who runs the job.
- Do not rely on JavaScript execution, clicking, scrolling or logging in — the engine fetches pages
  with plain HTTP GET. If content only appears after JavaScript runs, look for the same data in
  JSON-LD, `__NEXT_DATA__`/embedded JSON (`css` + `json`), or a public JSON endpoint the page itself
  calls (`format: json`). If none exists, report it as unsupported.
- Keep `delayMs` polite (≥ 1000 for commercial sites) and set realistic `maxPages`.
- Respect the site's terms of use and robots.txt; tell the user if a site forbids scraping.

## 8. Keep source knowledge in profiles

Everything specific to one website goes in that website's profile. Never ask for, assume, or
describe engine code such as `if source == "carsales"`. Generic query param names and output
field names must mean the same thing across all sources in a job.

## 9. How to reply

When asked to create configuration, reply with:

1. **Configuration** — one YAML document: a complete job with inline profiles (or separate profile
   documents if the user asks). It must be valid against the schemas and use only features in this
   document.
2. **Verification notes** — for each source: what you inspected, which selectors/paths are
   confident and which are guesses, and what to check on the first run (rejected-record reasons and
   field provenance are shown in the UI).
3. **Unsupported requirements** — anything the DSL cannot express, described as a missing *generic*
   primitive, e.g. "needs a POST request body", "needs a date output type", "needs items selected
   from embedded JSON (`listing.items` over a JSON path inside an HTML page)". Do not work around
   gaps with invented keys.

Ask a clarifying question only if the user's intent is genuinely ambiguous and the choice matters.
Otherwise pick an interpretation (e.g. "after 2024" → `year gte 2024` when the user also says "2024
onwards"), encode it in `query.filters`, and state it in your verification notes so it is
reviewable.

## 10. Checklist before you answer

- [ ] Every key appears in this document; every transform is in the transform table.
- [ ] Every required output field has a mapping in every source.
- [ ] Every hard constraint from the user is a `query.filters` entry on an output field.
- [ ] Numeric/URL fields have converting transforms (`integer`, `decimal`, `absoluteUrl`, or a
      `builtin` URL).
- [ ] Known traps are handled with `reject`, `prefer`, `ignore` or `listing.skip`.
- [ ] `from: detail` is only used with `listing.followDetail: true`.
- [ ] Pagination uses `next` **or** `param`, not both; totals only if the site really shows one.
- [ ] Unsupported requirements are listed, not faked.

## Appendix: complete working example

[`examples/ev-cars.job.yaml`](../examples/ev-cars.job.yaml) with
[`examples/sources/demo-motors.source.yaml`](../examples/sources/demo-motors.source.yaml) (HTML with
detail pages, finance-repayment traps, sponsored tiles, misleading meta tags) and
[`examples/sources/demo-ev-api.source.yaml`](../examples/sources/demo-ev-api.source.yaml) (JSON API
with a predicate path). Both run against the bundled demo site.
