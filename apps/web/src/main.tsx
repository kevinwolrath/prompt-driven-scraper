import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';
import { api, ApiError, Issue, Job, JobEvent, Profile, Progress, Record_, SourceProgress, terminal } from './api';

const DEFAULT_QUERY = JSON.stringify(
  {
    params: { fuel: 'electric', yearMin: 2024 },
    filters: [
      { field: 'fuelType', op: 'eq', value: 'electric' },
      { field: 'year', op: 'gte', value: 2024 },
    ],
  },
  null,
  2,
);

const DEFAULT_OUTPUT = JSON.stringify(
  {
    make: 'string',
    model: 'string',
    year: 'integer',
    price: 'decimal',
    kilometres: 'integer',
    batterySizeKwh: 'decimal?',
    fuelType: 'string',
    location: 'string?',
    url: 'url',
  },
  null,
  2,
);

const EVENT_TYPES = [
  'JobStarted', 'SourceStarted', 'PageDiscovered', 'PageFetched', 'RecordFound', 'RecordAccepted',
  'RecordRejected', 'Warning', 'Error', 'SourceCompleted', 'JobPausing', 'JobPaused', 'JobResumed',
  'JobFinalising', 'JobCompleted', 'JobPartial', 'JobCancelled', 'JobFailed',
];

interface LogLine { seq: number; type: string; source?: string; text: string }

function App() {
  // 1. Query input
  const [queryInput, setQueryInput] = useState("I'm only looking for EV cars after 2024");
  const [queryText, setQueryText] = useState(DEFAULT_QUERY);
  // 2. Search sources
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  // 3. Output
  const [outputText, setOutputText] = useState(DEFAULT_OUTPUT);
  const [format, setFormat] = useState<'csv' | 'json'>('csv');

  const [issues, setIssues] = useState<Issue[]>([]);
  const [notice, setNotice] = useState('');
  const [run, setRun] = useState<RunState | null>(null);

  useEffect(() => {
    api.profiles()
      .then((ps) => {
        setProfiles(ps);
        setSelected(ps.map((p) => p.id));
      })
      .catch((e) => setNotice(`Cannot reach the API: ${e.message}`));
  }, []);

  const outputFields = useMemo(() => {
    try {
      return Object.keys(JSON.parse(outputText));
    } catch {
      return [];
    }
  }, [outputText]);

  const refreshProfiles = async (select: string[] = []) => {
    const ps = await api.profiles();
    setProfiles(ps);
    setSelected((cur) => Array.from(new Set([...cur, ...select])));
  };

  const start = async () => {
    setIssues([]);
    setNotice('');
    let query: unknown, fields: unknown;
    try {
      query = JSON.parse(queryText || '{}');
    } catch (e) {
      return setNotice(`Structured query is not valid JSON: ${(e as Error).message}`);
    }
    try {
      fields = JSON.parse(outputText);
    } catch (e) {
      return setNotice(`Output schema is not valid JSON: ${(e as Error).message}`);
    }
    const job = {
      name: queryInput.slice(0, 80) || 'Untitled job',
      queryInput,
      query,
      searchSources: selected.map((id) => ({ id, profileRef: id })),
      output: { format, fields },
    };
    try {
      const created = await api.createJob(job);
      setRun({ job: created, columns: outputFields });
    } catch (e) {
      const err = e as ApiError;
      setNotice(err.message);
      setIssues(err.issues ?? []);
    }
  };

  const importConfig = async (text: string) => {
    setIssues([]);
    setNotice('');
    try {
      const res = await api.validate(text);
      if (res.error) return setNotice(res.error);
      setIssues(res.issues ?? []);
      if (res.kind === 'profile') {
        if (!res.valid) return setNotice('Profile has errors and was not added.');
        await api.saveProfile(text);
        await refreshProfiles([res.profile!.id]);
        return setNotice(`Added source profile "${res.profile!.id}".`);
      }
      const job = res.job!;
      const refs: string[] = [];
      for (const src of job.searchSources) {
        if (src.profile) {
          await api.saveProfile(JSON.stringify(src.profile));
          refs.push(src.profile.id);
        } else if (src.profileRef) {
          refs.push(src.profileRef);
        }
      }
      setQueryInput(job.queryInput ?? '');
      setQueryText(JSON.stringify(job.query ?? {}, null, 2));
      setOutputText(
        JSON.stringify(
          Object.fromEntries(job.output.fields.map((f) => [f.name, f.required ? f.type : `${f.type}?`])),
          null,
          2,
        ),
      );
      setFormat(job.output.format === 'json' ? 'json' : 'csv');
      const ps = await api.profiles();
      setProfiles(ps);
      setSelected(refs);
      setNotice(res.valid ? 'Configuration loaded.' : 'Configuration loaded with errors - see below.');
    } catch (e) {
      setNotice((e as Error).message);
    }
  };

  const running = run !== null && !terminal(run.job.status);

  return (
    <main className="shell">
      <header>
        <p className="eyebrow">Prompt Driven Scraper</p>
        <h1>Deterministic scraping, profile driven.</h1>
        <p className="lede">
          AI assistants write the configuration. The runtime never calls one: same configuration, same content, same
          results.
        </p>
      </header>

      <ImportPanel onImport={importConfig} disabled={running} />

      <section className="grid">
        <div className="panel">
          <h2><span className="step">1</span>Query input</h2>
          <textarea
            className="lead-input"
            value={queryInput}
            onChange={(e) => setQueryInput(e.target.value)}
            disabled={running}
            rows={2}
          />
          <label className="sub">
            Deterministic query <small>params for source requests, filters applied to every record</small>
            <textarea className="code" value={queryText} onChange={(e) => setQueryText(e.target.value)} disabled={running} rows={9} />
          </label>
        </div>

        <div className="panel">
          <h2><span className="step">2</span>Search sources</h2>
          {profiles.length === 0 && <p className="muted">No source profiles loaded.</p>}
          <ul className="sources">
            {profiles.map((p) => (
              <li key={p.id}>
                <label className="check">
                  <input
                    type="checkbox"
                    checked={selected.includes(p.id)}
                    disabled={running}
                    onChange={(e) =>
                      setSelected((cur) => (e.target.checked ? [...cur, p.id] : cur.filter((x) => x !== p.id)))
                    }
                  />
                  <span>
                    <strong>{p.name ?? p.id}</strong>
                    <small>{p.format ?? 'html'} · {p.request.url}</small>
                  </span>
                </label>
              </li>
            ))}
          </ul>
          <p className="muted small">Add profiles by pasting them into "Load AI-generated configuration".</p>
        </div>

        <div className="panel">
          <h2><span className="step">3</span>Output</h2>
          <textarea className="code" value={outputText} onChange={(e) => setOutputText(e.target.value)} disabled={running} rows={11} />
          <div className="row">
            <label className="check inline">
              <input type="radio" checked={format === 'csv'} onChange={() => setFormat('csv')} disabled={running} /> CSV
            </label>
            <label className="check inline">
              <input type="radio" checked={format === 'json'} onChange={() => setFormat('json')} disabled={running} /> JSON
            </label>
            <span className="muted small">"decimal?" = optional</span>
          </div>
        </div>
      </section>

      <div className="runbar">
        <button className="primary" onClick={start} disabled={running || selected.length === 0}>
          <span className="step light">4</span>Run
        </button>
        {notice && <span className="notice">{notice}</span>}
      </div>
      {issues.length > 0 && <Issues issues={issues} />}

      {run && <RunPanel key={run.job.id} run={run} onUpdate={(job) => setRun((r) => (r ? { ...r, job } : r))} />}
    </main>
  );
}

function ImportPanel({ onImport, disabled }: { onImport: (text: string) => void; disabled: boolean }) {
  const [text, setText] = useState('');
  return (
    <details className="panel import">
      <summary>Load AI-generated configuration</summary>
      <p className="muted small">
        Paste a job or a single source profile (YAML or JSON) produced with <code>ai/PROFILE_AUTHORING.md</code>. It is
        validated against the DSL; unsupported keys are rejected, not ignored.
      </p>
      <textarea className="code" rows={10} value={text} onChange={(e) => setText(e.target.value)} disabled={disabled} />
      <button onClick={() => onImport(text)} disabled={disabled || !text.trim()}>Validate &amp; load</button>
    </details>
  );
}

function Issues({ issues }: { issues: Issue[] }) {
  return (
    <ul className="issues panel">
      {issues.map((i, n) => (
        <li key={n} className={i.severity}>
          <b>{i.severity}</b> <code>{i.path}</code> {i.message}
        </li>
      ))}
    </ul>
  );
}

interface RunState { job: Job; columns: string[] }

function RunPanel({ run, onUpdate }: { run: RunState; onUpdate: (job: Job) => void }) {
  const { job, columns } = run;
  const [records, setRecords] = useState<Record_[]>([]);
  const [log, setLog] = useState<LogLine[]>([]);
  const [progressAt, setProgressAt] = useState(Date.now());
  const [, tick] = useState(0);
  const [busy, setBusy] = useState(false);
  const jobRef = useRef(job);
  jobRef.current = job;

  useEffect(() => {
    const es = api.events(job.id);
    const handle = (msg: MessageEvent) => {
      const ev = JSON.parse(msg.data) as JobEvent;
      if (ev.progress) {
        onUpdate({ ...jobRef.current, status: ev.status, progress: ev.progress, statusReason: reasonOf(ev) ?? jobRef.current.statusReason });
        setProgressAt(Date.now());
      }
      if (ev.type === 'RecordAccepted' && ev.data?.record) {
        setRecords((rs) => [...rs, ev.data!.record as Record_]);
      }
      const line = logLine(ev);
      if (line) setLog((l) => [line, ...l].slice(0, 200));
      if (terminal(ev.status)) es.close();
    };
    EVENT_TYPES.forEach((t) => es.addEventListener(t, handle as EventListener));
    return () => es.close();
  }, [job.id]);

  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);

  const act = async (action: 'pause' | 'resume' | 'cancel' | 'finalise') => {
    // Only the event stream updates job state: a control response can be
    // older than events that have already arrived.
    setBusy(true);
    try {
      await api.control(job.id, action);
    } catch {
      /* the event stream reports the real state */
    } finally {
      setBusy(false);
    }
  };

  const p = job.progress;
  const live = job.status === 'running' || job.status === 'pausing' || job.status === 'finalising';
  const elapsed = p.elapsedMs + (live ? Date.now() - progressAt : 0);
  const done = terminal(job.status);
  const hasOutput = job.status === 'completed' || job.status === 'partial';

  return (
    <section className="panel run">
      <div className="run-head">
        <div>
          <span className={`pill ${job.status}`}>{job.status}</span>
          <span className="muted"> {formatDuration(elapsed)}</span>
          {p.currentSource && live && <span className="muted"> · on {p.currentSource}</span>}
          {job.statusReason && <div className="muted small">{job.statusReason}</div>}
        </div>
        <div className="actions">
          {job.status === 'paused' ? (
            <button onClick={() => act('resume')} disabled={busy}>Resume</button>
          ) : (
            <button onClick={() => act('pause')} disabled={busy || job.status !== 'running'}>Pause</button>
          )}
          <button
            className="primary"
            onClick={() => act('finalise')}
            disabled={busy || !['running', 'pausing', 'paused'].includes(job.status)}
            title="Stop discovering, keep what has been fetched, validate, dedupe and produce output"
          >
            Give me what you've got
          </button>
          <button className="danger" onClick={() => act('cancel')} disabled={busy || done}>Cancel</button>
          {hasOutput && (
            <a className="button" href={api.outputUrl(job.id)} download>
              Download {job.status === 'partial' ? 'partial ' : ''}output
            </a>
          )}
        </div>
      </div>

      <Totals p={p} />

      <table className="source-table">
        <thead>
          <tr><th>Source</th><th>Status</th><th>Progress</th><th>Pages</th><th>Found</th><th>Accepted</th><th>Rejected</th><th>Dupes</th><th>Warn/Err</th></tr>
        </thead>
        <tbody>
          {p.sources.map((s) => <SourceRow key={s.id} s={s} />)}
        </tbody>
      </table>

      <div className="split">
        <div>
          <h3>Results <span className="muted">({records.length})</span></h3>
          <div className="scroll">
            <table className="results">
              <thead>
                <tr>{columns.map((c) => <th key={c}>{c}</th>)}<th>source</th></tr>
              </thead>
              <tbody>
                {records.map((r, i) => (
                  <tr key={i} className={r.partial ? 'partial-row' : ''} title={provenance(r)}>
                    {columns.map((c) => <td key={c}>{cell(r.values[c])}</td>)}
                    <td className="muted">{r.sourceId}</td>
                  </tr>
                ))}
                {records.length === 0 && (
                  <tr><td colSpan={columns.length + 1} className="muted">{done ? 'No records.' : 'Waiting for the first accepted record…'}</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
        <div>
          <h3>Warnings, errors &amp; rejections</h3>
          <ul className="log scroll">
            {log.map((l) => (
              <li key={l.seq} className={l.type}>
                <b>{label(l.type)}</b> {l.source && <span className="muted">[{l.source}]</span>} {l.text}
              </li>
            ))}
            {log.length === 0 && <li className="muted">Nothing yet.</li>}
          </ul>
        </div>
      </div>
    </section>
  );
}

function Totals({ p }: { p: Progress }) {
  const items: [string, number][] = [
    ['sources', p.sourcesComplete],
    ['pages visited', p.pagesVisited],
    ['records found', p.recordsFound],
    ['accepted', p.recordsAccepted],
    ['rejected', p.recordsRejected],
    ['duplicates', p.duplicates],
    ['warnings', p.warnings],
    ['errors', p.errors],
  ];
  return (
    <dl className="totals">
      {items.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{k === 'sources' ? `${v}/${p.sourcesTotal}` : v}</dd>
        </div>
      ))}
    </dl>
  );
}

function SourceRow({ s }: { s: SourceProgress }) {
  const known = (s.listingPagesTotal ?? 0) > 0;
  const pct = known ? Math.min(100, Math.round((s.listingPagesVisited / s.listingPagesTotal!) * 100)) : 0;
  const complete = s.status === 'completed';
  return (
    <tr>
      <td><strong>{s.name ?? s.id}</strong></td>
      <td><span className={`pill small ${s.status}`}>{s.status}</span></td>
      <td className="bar-cell">
        {s.status === 'running' && !known && <div className="bar indeterminate" title="Total unknown"><span /></div>}
        {(known || complete) && <div className="bar"><span style={{ width: `${complete ? 100 : pct}%` }} /></div>}
        <small className="muted">
          {known ? `listing page ${s.listingPagesVisited} of ${s.listingPagesTotal}` : s.status === 'running' ? `listing page ${s.listingPagesVisited}, total unknown` : ''}
        </small>
      </td>
      <td>{s.pagesVisited}<span className="muted">/{s.pagesDiscovered}</span></td>
      <td>{s.recordsFound}</td>
      <td>{s.recordsAccepted}</td>
      <td>{s.recordsRejected}</td>
      <td>{s.duplicates}</td>
      <td>{s.warnings}/{s.errors}</td>
    </tr>
  );
}

function logLine(ev: JobEvent): LogLine | null {
  if (!['Warning', 'Error', 'RecordRejected', 'JobFailed', 'JobPartial', 'JobCancelled'].includes(ev.type)) return null;
  return { seq: ev.seq, type: ev.type, source: ev.sourceId, text: ev.message ?? '' };
}

function reasonOf(ev: JobEvent) {
  return ['JobPartial', 'JobFailed', 'JobCancelled'].includes(ev.type) ? ev.message : undefined;
}

function label(type: string) {
  return { RecordRejected: 'rejected', Warning: 'warning', Error: 'error' }[type] ?? type;
}

function provenance(r: Record_) {
  const lines = Object.entries(r.provenance ?? {}).map(([k, v]) => `${k} ← ${v}`);
  if (r.partial) lines.unshift('partial: detail page not used');
  return lines.join('\n');
}

function cell(v: unknown) {
  if (v === null || v === undefined) return <span className="muted">—</span>;
  const s = String(v);
  if (/^https?:\/\//.test(s)) return <a href={s} target="_blank" rel="noreferrer">link</a>;
  return s;
}

function formatDuration(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(s / 60);
  return m > 0 ? `${m}m ${String(s % 60).padStart(2, '0')}s` : `${s}s`;
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
