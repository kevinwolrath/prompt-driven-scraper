package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/output"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/query"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

const maxNotes = 8

// srcEvent emits an event for source idx, applying mutate to its progress.
func (r *run) srcEvent(idx int, typ contracts.EventType, msg string, data map[string]any, mutate func(*contracts.SourceProgress)) {
	srcID := r.job.Definition.SearchSources[idx].ID
	r.emit(typ, srcID, msg, data, func(j *contracts.Job) {
		sp := &j.Progress.Sources[idx]
		switch typ {
		case contracts.EventWarning:
			sp.Warnings++
			j.Progress.Warnings++
		case contracts.EventError:
			sp.Errors++
			j.Progress.Errors++
		}
		if mutate != nil {
			mutate(sp)
		}
	})
}

func (r *run) warn(idx int, msg string, data map[string]any) {
	r.srcEvent(idx, contracts.EventWarning, msg, data, nil)
}

// seed builds the first listing request for a source from the generic query.
func (r *run) seed(idx int, p *extract.Profile, st *state) {
	srcID := r.job.Definition.SearchSources[idx].ID
	r.emit(contracts.EventSourceStarted, srcID, p.Source.Name, nil, func(j *contracts.Job) {
		j.Progress.CurrentSource = srcID
		j.Progress.Sources[idx].Status = contracts.SourceRunning
	})
	st.Seeded = true
	start, warnings, err := query.BuildURL(p.Source.Request, r.job.Definition.Query.Params)
	for _, w := range warnings {
		r.warn(idx, w, nil)
	}
	if err != nil {
		st.Failed = true
		r.srcEvent(idx, contracts.EventError, err.Error(), nil, nil)
		return
	}
	if pg := p.Source.Pagination; pg != nil && pg.Param != nil {
		if start, err = query.WithParam(start, pg.Param.Name, pageStart(pg.Param)); err != nil {
			st.Failed = true
			r.srcEvent(idx, contracts.EventError, err.Error(), nil, nil)
			return
		}
	}
	r.enqueue(idx, st, task{Kind: taskListing, URL: start, Page: 1})
}

func pageStart(pp *contracts.PageParam) int {
	if pp.Start == 0 && pp.Step == 0 {
		return 1
	}
	return pp.Start
}

func (r *run) enqueue(idx int, st *state, t task) bool {
	if st.Seen[t.URL] {
		return false
	}
	st.Seen[t.URL] = true
	st.Queue = append(st.Queue, t)
	r.srcEvent(idx, contracts.EventPageDiscovered, t.URL, map[string]any{"kind": t.Kind, "page": t.Page}, func(sp *contracts.SourceProgress) {
		sp.PagesDiscovered++
	})
	return true
}

func (r *run) fetch(ctx context.Context, idx int, p *extract.Profile, rawURL string) (fetch.Response, error) {
	retries := 1
	if p.Source.Limits.Retries != nil {
		retries = *p.Source.Limits.Retries
	}
	timeout := 30 * time.Second
	if p.Source.Limits.TimeoutS > 0 {
		timeout = time.Duration(p.Source.Limits.TimeoutS) * time.Second
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fetch.Response{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * r.e.opts.RetryBackoff):
			}
		}
		resp, err := r.e.opts.Fetcher.Fetch(ctx, fetch.Request{URL: rawURL, Headers: p.Source.Request.Headers, Timeout: timeout})
		r.mu.Lock()
		r.lastFetch = r.e.opts.Now()
		r.mu.Unlock()
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var se *fetch.StatusError
		if ctx.Err() != nil || (errors.As(err, &se) && !se.Retryable()) {
			break
		}
		if attempt < retries {
			r.warn(idx, fmt.Sprintf("retrying %s: %v", rawURL, err), nil)
		}
	}
	return fetch.Response{}, lastErr
}

func (r *run) process(ctx context.Context, idx int, p *extract.Profile, st *state, t task) {
	switch t.Kind {
	case taskListing:
		r.processListing(ctx, idx, p, st, t)
	case taskDetail:
		r.processDetail(ctx, idx, p, st, t)
	}
}

func (r *run) processListing(ctx context.Context, idx int, p *extract.Profile, st *state, t task) {
	resp, err := r.fetch(ctx, idx, p, t.URL)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if st.ListingPages == 0 {
			st.Failed = true
		}
		r.srcEvent(idx, contracts.EventError, fmt.Sprintf("listing page %d: %v", t.Page, err), map[string]any{"url": t.URL}, nil)
		return
	}
	st.ListingPages++
	page, err := p.ParsePage(resp.URL, resp.Body)
	if err != nil {
		r.srcEvent(idx, contracts.EventError, fmt.Sprintf("listing page %d: %v", t.Page, err), map[string]any{"url": t.URL}, nil)
		return
	}
	items, skipped := p.Items(page)
	total := 0
	if st.ListingPages == 1 {
		total = r.pageTotal(p, page)
	}
	r.srcEvent(idx, contracts.EventPageFetched, resp.URL, map[string]any{
		"kind": taskListing, "page": t.Page, "bytes": len(resp.Body), "items": len(items), "pagesTotal": total,
	}, func(sp *contracts.SourceProgress) {
		sp.PagesVisited++
		sp.ListingPagesVisited++
		if total > 0 {
			sp.ListingPagesTotal = total
		}
	})
	if len(items) == 0 && len(skipped) == 0 {
		r.warn(idx, fmt.Sprintf("listing page %d has no items matching %q", t.Page, p.Source.Listing.Items), map[string]any{"url": resp.URL})
	}
	for _, reason := range skipped {
		r.srcEvent(idx, contracts.EventRecordRejected, reason, map[string]any{"url": resp.URL}, func(sp *contracts.SourceProgress) {
			sp.RecordsFound++
			sp.RecordsRejected++
		})
	}

	limited := false
	srcID := r.job.Definition.SearchSources[idx].ID
	for _, it := range items {
		if max := p.Source.Limits.MaxRecords; max > 0 && st.Items >= max {
			limited = true
			break
		}
		st.Items++
		scope := extract.Scope{SourceID: srcID, Item: it, Listing: page}
		pending := &pendingItem{ListingURL: page.URL.String(), Results: map[string][]extract.Result{}}
		if p.DetailLink != nil {
			if v, ok := p.DetailLink.First(scope, extract.FromItem); ok {
				pending.DetailURL = resolve(page.URL, extract.Stringify(v))
			}
		}
		scope.DetailURL = pending.DetailURL
		for _, rule := range p.Fields {
			res, notes := rule.Evaluate(scope, extract.FromItem, extract.FromListing)
			pending.Results[rule.Name] = res
			pending.Notes = appendNotes(pending.Notes, notes)
		}
		r.srcEvent(idx, contracts.EventRecordFound, pending.DetailURL, map[string]any{"page": t.Page, "item": it.Index + 1}, func(sp *contracts.SourceProgress) {
			sp.RecordsFound++
		})
		switch {
		case p.FollowDetail && pending.DetailURL != "":
			if !r.enqueue(idx, st, task{Kind: taskDetail, URL: pending.DetailURL, Item: pending}) {
				r.srcEvent(idx, contracts.EventRecordRejected, "duplicate detail link "+pending.DetailURL, map[string]any{"duplicate": true}, func(sp *contracts.SourceProgress) {
					sp.Duplicates++
				})
			}
		case p.FollowDetail:
			r.warn(idx, fmt.Sprintf("listing item %d has no detail link; using listing values only", it.Index+1), nil)
			r.finishRecord(idx, p, st, pending, nil, true)
		default:
			r.finishRecord(idx, p, st, pending, nil, false)
		}
	}
	if limited {
		r.warn(idx, fmt.Sprintf("limits.maxRecords (%d) reached; not discovering further pages", p.Source.Limits.MaxRecords), nil)
		return
	}
	r.paginate(idx, p, st, t, page, len(items)+len(skipped))
}

func (r *run) paginate(idx int, p *extract.Profile, st *state, t task, page *extract.Page, itemCount int) {
	pg := p.Source.Pagination
	if pg == nil || itemCount == 0 {
		return
	}
	if max := p.Source.Limits.MaxPages; max > 0 && st.ListingPages >= max {
		if hasMore(p, page, t, st) {
			r.warn(idx, fmt.Sprintf("limits.maxPages (%d) reached", max), nil)
		}
		return
	}
	var next string
	switch {
	case p.Next != nil:
		if v, ok := p.Next.First(extract.Scope{Listing: page}, extract.FromListing); ok {
			next = resolve(page.URL, extract.Stringify(v))
		}
	case pg.Param != nil:
		if total := r.knownTotal(idx); total > 0 && t.Page >= total {
			return
		}
		step := pg.Param.Step
		if step == 0 {
			step = 1
		}
		n := pageStart(pg.Param) + t.Page*step
		next, _ = query.WithParam(t.URL, pg.Param.Name, n)
	}
	if next != "" {
		r.enqueue(idx, st, task{Kind: taskListing, URL: next, Page: t.Page + 1})
	}
}

func hasMore(p *extract.Profile, page *extract.Page, t task, st *state) bool {
	if p.Next != nil {
		_, ok := p.Next.First(extract.Scope{Listing: page}, extract.FromListing)
		return ok
	}
	return true
}

func (r *run) knownTotal(idx int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job.Progress.Sources[idx].ListingPagesTotal
}

// pageTotal reads a real page total from the first listing page, if the
// profile says where to find one.
func (r *run) pageTotal(p *extract.Profile, page *extract.Page) int {
	pg := p.Source.Pagination
	if pg == nil {
		return 0
	}
	total := 0
	scope := extract.Scope{Listing: page}
	if p.TotalPages != nil {
		if v, ok := p.TotalPages.First(scope, extract.FromListing); ok {
			if f, ok := toFloat(v); ok {
				total = int(f)
			}
		}
	} else if p.TotalResults != nil && pg.PageSize > 0 {
		if v, ok := p.TotalResults.First(scope, extract.FromListing); ok {
			if f, ok := toFloat(v); ok {
				total = int(math.Ceil(f / float64(pg.PageSize)))
			}
		}
	}
	if max := p.Source.Limits.MaxPages; max > 0 && total > max {
		total = max
	}
	return total
}

func (r *run) processDetail(ctx context.Context, idx int, p *extract.Profile, st *state, t task) {
	resp, err := r.fetch(ctx, idx, p, t.URL)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.warn(idx, fmt.Sprintf("detail page failed, using listing values: %v", err), map[string]any{"url": t.URL})
		r.finishRecord(idx, p, st, t.Item, nil, true)
		return
	}
	page, err := p.ParsePage(resp.URL, resp.Body)
	if err != nil {
		r.warn(idx, fmt.Sprintf("detail page unparseable, using listing values: %v", err), map[string]any{"url": t.URL})
		r.finishRecord(idx, p, st, t.Item, nil, true)
		return
	}
	r.srcEvent(idx, contracts.EventPageFetched, resp.URL, map[string]any{"kind": taskDetail, "bytes": len(resp.Body)}, func(sp *contracts.SourceProgress) {
		sp.PagesVisited++
	})
	r.finishRecord(idx, p, st, t.Item, page, false)
}

// finishRecord merges listing and detail candidate results, applies
// precedence, coerces to the output schema, filters and deduplicates.
func (r *run) finishRecord(idx int, p *extract.Profile, st *state, item *pendingItem, detail *extract.Page, partial bool) {
	srcID := r.job.Definition.SearchSources[idx].ID
	notes := item.Notes
	results := map[string][]extract.Result{}
	for k, v := range item.Results {
		results[k] = v
	}
	if detail != nil {
		scope := extract.Scope{SourceID: srcID, Detail: detail, DetailURL: item.DetailURL}
		for _, rule := range p.Fields {
			res, n := rule.Evaluate(scope, extract.FromDetail)
			results[rule.Name] = append(results[rule.Name], res...)
			notes = appendNotes(notes, n)
		}
	}
	values := map[string]any{}
	provenance := map[string]string{}
	for _, rule := range p.Fields {
		if chosen, ok := extract.Choose(results[rule.Name]); ok {
			values[rule.Name] = chosen.Value
			provenance[rule.Name] = chosen.Origin
			if chosen.Preferred {
				provenance[rule.Name] += " (preferred)"
			}
		}
	}
	pageURL := item.DetailURL
	if pageURL == "" {
		pageURL = item.ListingURL
	}
	reject := func(reason string, dup bool) {
		data := map[string]any{"reason": reason, "values": values, "url": pageURL}
		if len(notes) > 0 {
			data["notes"] = notes
		}
		r.srcEvent(idx, contracts.EventRecordRejected, reason, data, func(sp *contracts.SourceProgress) {
			if dup {
				sp.Duplicates++
			} else {
				sp.RecordsRejected++
			}
		})
	}

	def := r.job.Definition
	coerced, reason, warnings := output.Coerce(def.Output.Fields, values)
	for _, w := range warnings {
		r.warn(idx, w, map[string]any{"url": pageURL})
	}
	if reason != "" {
		reject(reason, false)
		return
	}
	if reason := query.Apply(def.Query.Filters, coerced); reason != "" {
		reject(reason, false)
		return
	}
	key := output.Key(output.DedupeFields(def.Output), coerced)
	if st.Keys[key] {
		reject("duplicate of an accepted record", true)
		return
	}
	st.Keys[key] = true
	rec := contracts.Record{
		JobID: r.job.ID, SourceID: srcID, Key: key, Values: coerced, Provenance: provenance,
		PageURL: pageURL, Partial: partial, SeenAt: r.e.opts.Now(),
	}
	if err := r.e.store.AppendRecord(context.Background(), rec); err != nil {
		r.srcEvent(idx, contracts.EventError, "store record: "+err.Error(), nil, nil)
		return
	}
	data := map[string]any{"record": rec}
	if len(notes) > 0 {
		data["notes"] = notes
	}
	r.srcEvent(idx, contracts.EventRecordAccepted, pageURL, data, func(sp *contracts.SourceProgress) {
		sp.RecordsAccepted++
	})
}

// salvage turns listing items whose detail pages were never fetched into
// records using their listing values, for "give me what you've got".
func (r *run) salvage(st *state) {
	if st.SourceIndex >= len(r.profiles) {
		return
	}
	idx := st.SourceIndex
	p := r.profiles[idx]
	dropped := 0
	for _, t := range st.Queue {
		switch t.Kind {
		case taskDetail:
			r.finishRecord(idx, p, st, t.Item, nil, true)
		case taskListing:
			dropped++
		}
	}
	if dropped > 0 {
		r.warn(idx, fmt.Sprintf("%d discovered listing page(s) not visited", dropped), nil)
	}
}

func resolve(base *url.URL, ref string) string {
	u, err := url.Parse(ref)
	if err != nil || ref == "" {
		return ""
	}
	return base.ResolveReference(u).String()
}

func appendNotes(notes, more []string) []string {
	for _, n := range more {
		if len(notes) >= maxNotes {
			break
		}
		notes = append(notes, n)
	}
	return notes
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}
