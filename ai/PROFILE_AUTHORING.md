# AI Profile Authoring Instructions

Use this document as context when asking GPT, Claude, or another model to create scraper jobs or source profiles.

## Important boundary

The scraper runtime is deterministic. You are NOT the runtime scraper and must not introduce LLM calls into the generated configuration.

Your role is to author configuration that the deterministic runtime can validate and execute.

## Job concepts

A job contains:

- `queryInput`: the user's natural-language intent.
- `query`: a normalized generic query when useful.
- `searchSources`: deterministic source profiles.
- `output`: requested generic fields and format.

## Source profile responsibilities

A source profile must describe both directions:

1. How generic query values map to requests for the source.
2. How source-specific responses map back to generic output fields.

Prefer declarative rules over site-specific executable code.

A profile may include:

- start/search URL templates
- HTTP method and safe request parameters
- pagination rules
- result/detail link discovery
- selectors or structured-data paths
- attributes to read
- ordered candidate sources for a value
- rejection/ignore rules for misleading values
- deterministic transforms such as trim, regex, replace, integer, decimal, currency, date, URL resolution
- precedence rules, e.g. detail page value overrides list-card value

## Example request

> I am looking for EV cars from 2024 onwards. Find suitable Australian vehicle sale sources and generate source profiles. I need make, model, year, price, kilometres, battery size, location and URL. Output CSV.

## Example shape

```yaml
name: EV cars 2024+
queryInput: "I'm only looking for EV cars after 2024"
query:
  fuel: electric
  yearMin: 2024
searchSources:
  - id: example-cars
    profile:
      request:
        startUrl: "https://example.invalid/cars"
        queryMap:
          fuel:
            parameter: fuel
            values:
              electric: EV
          yearMin:
            parameter: minYear
      pagination:
        next:
          selector: "a.next"
          attribute: href
      records:
        container: ".listing"
        detailUrl:
          selector: "a.details"
          attribute: href
      mapping:
        make:
          candidates:
            - structured: "vehicle.make"
            - selector: ".make"
        price:
          candidates:
            - structured: "offers.price"
            - selector: ".price"
          reject:
            - contains: "/week"
            - contains: "/month"
          transforms:
            - currency
            - decimal
output:
  format: csv
  fields:
    - { name: make, type: string, required: true }
    - { name: model, type: string, required: true }
    - { name: year, type: integer }
    - { name: price, type: decimal }
    - { name: kilometres, type: integer }
    - { name: batterySizeKwh, type: decimal }
    - { name: location, type: string }
    - { name: url, type: url, required: true }
```

## Authoring rules

- Do not invent support for an operation that is not documented by the project's schemas/runtime.
- If a source exposes multiple values that could satisfy a field, make precedence explicit.
- If metadata or page content is misleading, encode deterministic reject/ignore rules rather than merely warning the user.
- Keep source knowledge in the profile; do not require `if source == ...` code in the generic engine.
- Return machine-readable YAML or JSON when asked for a configuration.
- If the current profile language cannot express a required site behaviour, identify the missing generic capability separately instead of embedding arbitrary code.
