// Package demosite serves a small, fully deterministic vehicle marketplace
// used for demos and tests. It deliberately contains the kinds of traps real
// sites have so profiles must express negative knowledge:
//
//   - weekly finance repayments next to (and sometimes before) the real price
//   - a misleading price meta tag on detail pages (the weekly repayment)
//   - sponsored tiles inside the listing grid
//   - the same vehicle listed on two pages
//   - a vehicle with no price ("Enquire")
//   - a detail page that always returns HTTP 500
//   - the server honours the fuel filter but ignores minYear
//
// It also serves a JSON API for a second, differently shaped source.
package demosite

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Car struct {
	ID        string
	Make      string
	Model     string
	Year      int
	Fuel      string // EV, Petrol, Hybrid
	Price     int    // 0 means "Enquire"
	Weekly    int
	Km        int
	BatteryKw float64
	Location  string
}

func (c Car) Title() string { return fmt.Sprintf("%d %s %s", c.Year, c.Make, c.Model) }

// FinanceFirst cars show the weekly repayment before the real price, so a
// profile that takes "the first price-looking text" gets it wrong.
func (c Car) FinanceFirst() bool { return financeFirst[c.ID] }

var financeFirst = map[string]bool{"tesla-model-3-2024-a": true, "kia-ev6-2025": true, "bmw-ix1-2025": true}

// Cars is the marketplace inventory (deterministic).
var Cars = []Car{
	{"tesla-model-3-2024-a", "Tesla", "Model 3", 2024, "EV", 58900, 189, 12345, 60, "Sydney NSW"},
	{"byd-atto-3-2024", "BYD", "Atto 3", 2024, "EV", 44499, 142, 8200, 60.5, "Melbourne VIC"},
	{"toyota-corolla-2024", "Toyota", "Corolla", 2024, "Petrol", 31990, 99, 15000, 0, "Brisbane QLD"},
	{"hyundai-ioniq-5-2023", "Hyundai", "Ioniq 5", 2023, "EV", 61990, 199, 22000, 77.4, "Perth WA"},
	{"kia-ev6-2025", "Kia", "EV6", 2025, "EV", 72590, 231, 900, 84, "Adelaide SA"},
	{"mg4-2024", "MG", "MG4", 2024, "EV", 39990, 128, 5400, 64, "Hobart TAS"},
	{"polestar-2-2024", "Polestar", "2", 2024, "EV", 0, 0, 3100, 82, "Sydney NSW"},
	{"toyota-rav4-hybrid-2025", "Toyota", "RAV4 Hybrid", 2025, "Hybrid", 47990, 152, 100, 0, "Canberra ACT"},
	{"tesla-model-y-2025", "Tesla", "Model Y", 2025, "EV", 65400, 209, 1500, 75, "Gold Coast QLD"},
	{"volvo-ex30-2024", "Volvo", "EX30", 2024, "EV", 59990, 191, 4200, 69, "Melbourne VIC"},
	{"nissan-leaf-2022", "Nissan", "Leaf", 2022, "EV", 29990, 96, 41000, 40, "Newcastle NSW"},
	{"bmw-ix1-2025", "BMW", "iX1", 2025, "EV", 84900, 271, 2500, 64.7, "Sydney NSW"},
	{"cupra-born-2024", "Cupra", "Born", 2024, "EV", 59990, 191, 7000, 77, "Brisbane QLD"},
	{"mazda-cx5-2024", "Mazda", "CX-5", 2024, "Petrol", 38990, 125, 12000, 0, "Perth WA"},
	{"byd-seal-2025", "BYD", "Seal", 2025, "EV", 58798, 188, 600, 82.5, "Darwin NT"},
}

// BrokenDetailID always returns HTTP 500 on its detail page.
const BrokenDetailID = "volvo-ex30-2024"

const PageSize = 4

type Options struct {
	// Latency is added to every response so progress is observable.
	Latency time.Duration
}

func Handler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Demo Motors</title><h1>Demo Motors</h1>
<p>A deterministic demo marketplace for Prompt Driven Scraper.</p>
<ul><li><a href="/demo/cars">HTML listings</a></li><li><a href="/demo/api/evs">JSON API</a></li></ul>`)
	})
	mux.HandleFunc("GET /demo/cars", func(w http.ResponseWriter, r *http.Request) {
		sleep(r, opts.Latency)
		listing(w, r)
	})
	mux.HandleFunc("GET /demo/cars/{id}", func(w http.ResponseWriter, r *http.Request) {
		sleep(r, opts.Latency)
		detail(w, r)
	})
	mux.HandleFunc("GET /demo/protected/cars", func(w http.ResponseWriter, r *http.Request) {
		sleep(r, opts.Latency)
		protected(w, r)
	})
	mux.HandleFunc("GET /demo/challenge/cars", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<!doctype html><html><head><title>Just a moment...</title></head>
<body><div id="cf-chl-widget-demo">Checking your browser before accessing the site.</div></body></html>`)
	})
	mux.HandleFunc("GET /demo/api/evs", func(w http.ResponseWriter, r *http.Request) {
		sleep(r, opts.Latency)
		api(w, r)
	})
	mux.HandleFunc("GET /demo/api/evs/{id}", func(w http.ResponseWriter, r *http.Request) {
		sleep(r, opts.Latency)
		for _, c := range Cars {
			if c.ID == r.PathValue("id") && c.Fuel == "EV" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(apiItem(c))
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

func sleep(r *http.Request, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-r.Context().Done():
	case <-time.After(d):
	}
}

// inventory is the listing order: matching cars, with the first car repeated
// as a "featured" tile at the top of page 2 to create a cross-page duplicate.
func inventory(fuel string) []Car {
	var out []Car
	for _, c := range Cars {
		if fuel == "" || strings.EqualFold(c.Fuel, fuel) {
			out = append(out, c)
		}
	}
	if len(out) > PageSize {
		dup := out[0]
		out = append(out[:PageSize], append([]Car{dup}, out[PageSize:]...)...)
	}
	return out
}

func pageParam(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

var listingTmpl = template.Must(template.New("listing").Funcs(template.FuncMap{"money": money, "km": km}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Cars for sale | Demo Motors</title>
<meta property="og:title" content="Thousands of cars for sale"></head>
<body>
<header class="site-header"><nav><a href="/">Demo Motors</a></nav>
  <div class="promo">Finance from <span class="price">$99 per week</span></div></header>
<main>
<p class="summary"><span class="result-count">{{.Total}}</span> cars found</p>
<section class="results">
{{range $i, $c := .Cars}}
{{if eq $i 2}}<article class="listing-card sponsored"><a class="details" href="/ads/insurance">Car insurance from $1</a>
  <span class="make">Sponsored</span><div class="price"><span class="vehicle-price">$1</span></div></article>{{end}}
<article class="listing-card" data-listing-id="{{$c.ID}}">
  <h2><a class="details" href="/demo/cars/{{$c.ID}}">{{$c.Title}}</a></h2>
  <ul class="specs"><li class="make">{{$c.Make}}</li><li class="model">{{$c.Model}}</li><li class="year">{{$c.Year}}</li>
    <li class="fuel">{{$c.Fuel}}</li><li class="odometer">{{km $c.Km}}</li></ul>
  <div class="price">
  {{- if eq $c.Price 0}}<span class="vehicle-price">Enquire</span>
  {{- else if $c.FinanceFirst}}<span class="finance-repayment">{{money $c.Weekly}} per week</span> <span class="vehicle-price">{{money $c.Price}} <small>drive away</small></span>
  {{- else}}<span class="vehicle-price">{{money $c.Price}} <small>excl. govt charges</small></span> <span class="finance-repayment">or {{money $c.Weekly}} per week</span>{{end}}
  </div>
  <p class="location">{{$c.Location}}</p>
</article>
{{end}}
</section>
<nav class="pagination">{{if .Prev}}<a class="prev" href="{{.Prev}}">Previous</a>{{end}}
<span class="page-info">Page {{.Page}} of {{.Pages}}</span>
{{if .Next}}<a class="next" href="{{.Next}}">Next</a>{{end}}</nav>
</main></body></html>`))

func listing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	all := inventory(q.Get("fuel"))
	page := pageParam(r)
	pages := (len(all) + PageSize - 1) / PageSize
	if pages == 0 {
		pages = 1
	}
	from := (page - 1) * PageSize
	var cars []Car
	if from < len(all) {
		to := from + PageSize
		if to > len(all) {
			to = len(all)
		}
		cars = all[from:to]
	}
	link := func(p int) string {
		v := r.URL.Query()
		v.Set("page", strconv.Itoa(p))
		return "/demo/cars?" + v.Encode()
	}
	data := map[string]any{"Cars": cars, "Total": len(all), "Page": page, "Pages": pages}
	if page > 1 {
		data["Prev"] = link(page - 1)
	}
	if page < pages {
		data["Next"] = link(page + 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = listingTmpl.Execute(w, data)
}

var detailTmpl = template.Must(template.New("detail").Funcs(template.FuncMap{"money": money, "km": km}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Car.Title}} | Demo Motors</title>
<meta property="product:price:amount" content="{{.Car.Weekly}}">
<meta property="og:title" content="{{.Car.Title}}">
<script type="application/ld+json">{{.LD}}</script>
</head><body>
<main class="vehicle">
<h1 class="title">{{.Car.Title}}</h1>
<div class="pricing">
  {{if .Car.Price}}<p class="price-driveaway">{{money .Car.Price}} drive away</p>{{else}}<p class="price-driveaway">Price on application</p>{{end}}
  <aside class="finance-box"><span class="price">{{money .Car.Weekly}}</span> per week, comparison rate 9.99%</aside>
</div>
<table class="spec-table">
  <tr><th>Make</th><td data-spec="make">{{.Car.Make}}</td></tr>
  <tr><th>Model</th><td data-spec="model">{{.Car.Model}}</td></tr>
  <tr><th>Year</th><td data-spec="year">{{.Car.Year}}</td></tr>
  <tr><th>Odometer</th><td data-spec="odometer">{{km .Car.Km}}</td></tr>
  <tr><th>Fuel</th><td data-spec="fuel">{{.Car.Fuel}}</td></tr>
  {{if .Car.BatteryKw}}<tr><th>Battery capacity</th><td data-spec="battery">{{.Car.BatteryKw}} kWh</td></tr>{{end}}
</table>
<p class="location">Located in {{.Car.Location}}</p>
</main></body></html>`))

func detail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == BrokenDetailID {
		http.Error(w, "upstream error", http.StatusInternalServerError)
		return
	}
	var car *Car
	for i := range Cars {
		if Cars[i].ID == id {
			car = &Cars[i]
		}
	}
	if car == nil {
		http.NotFound(w, r)
		return
	}
	ld := map[string]any{
		"@context": "https://schema.org", "@type": "Car",
		"name": car.Title(), "brand": map[string]any{"@type": "Brand", "name": car.Make},
		"model": car.Model, "vehicleModelDate": strconv.Itoa(car.Year), "fuelType": car.Fuel,
		"mileageFromOdometer": map[string]any{"@type": "QuantitativeValue", "value": car.Km, "unitCode": "KMT"},
	}
	if car.Price > 0 {
		ld["offers"] = map[string]any{"@type": "Offer", "price": car.Price, "priceCurrency": "AUD"}
	}
	b, _ := json.Marshal(ld)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = detailTmpl.Execute(w, map[string]any{"Car": car, "LD": template.JS(b)})
}

func api(w http.ResponseWriter, r *http.Request) {
	var evs []Car
	for _, c := range Cars {
		if c.Fuel == "EV" {
			evs = append(evs, c)
		}
	}
	page := pageParam(r)
	size := 5
	from := (page - 1) * size
	results := []map[string]any{}
	for i := from; i < from+size && i < len(evs); i++ {
		results = append(results, apiItem(evs[i]))
	}
	body := map[string]any{"total": len(evs), "page": page, "pageSize": size, "results": results}
	if from+size < len(evs) {
		body["next"] = fmt.Sprintf("/demo/api/evs?page=%d", page+1)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func apiItem(c Car) map[string]any {
	item := map[string]any{
		"id": "api-" + c.ID, "title": c.Title(),
		"vehicle":  map[string]any{"make": c.Make, "model": c.Model, "year": c.Year, "odometer": map[string]any{"value": c.Km, "unit": "km"}},
		"location": c.Location,
		"href":     "/demo/api/evs/" + c.ID,
	}
	if c.BatteryKw > 0 {
		item["vehicle"].(map[string]any)["battery"] = map[string]any{"capacityKwh": c.BatteryKw}
	}
	if c.Price > 0 {
		item["pricing"] = []map[string]any{
			{"type": "weekly", "amount": c.Weekly},
			{"type": "driveaway", "amount": c.Price},
		}
	}
	return item
}

// protected behaves like a site that refuses plain HTTP clients but serves
// browsers, and renders its listings with JavaScript: plain HTTP gets 403,
// and even the raw HTML contains no listing elements. Browsers send
// Sec-Fetch-Mode on navigations; Go's HTTP client does not.
func protected(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Mode") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<!doctype html><title>Forbidden</title><p>Automated requests are not allowed.</p>")
		return
	}
	var items []map[string]any
	for _, c := range inventory(r.URL.Query().Get("fuel")) {
		if c.Price == 0 {
			continue
		}
		items = append(items, map[string]any{"id": c.ID, "title": c.Title(), "make": c.Make, "model": c.Model,
			"year": c.Year, "price": money(c.Price), "km": km(c.Km), "location": c.Location})
	}
	data, _ := json.Marshal(items)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Demo Motors (protected)</title></head>
<body><main id="results">Loading…</main>
<script>
const cars = %s;
setTimeout(() => {
  document.getElementById("results").innerHTML = cars.map(c =>
    '<article class="listing-card" data-id="' + c.id + '">' +
    '<h2><a class="details" href="/demo/cars/' + c.id + '">' + c.title + '</a></h2>' +
    '<span class="make">' + c.make + '</span> <span class="model">' + c.model + '</span> ' +
    '<span class="year">' + c.year + '</span> <span class="price">' + c.price + '</span> ' +
    '<span class="odometer">' + c.km + '</span> <span class="location">' + c.location + '</span></article>'
  ).join("");
}, 200);
</script></body></html>`, data)
}

func money(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return "$" + b.String()
}

func km(n int) string { return strings.TrimPrefix(money(n), "$") + " km" }
