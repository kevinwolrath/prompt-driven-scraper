import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';
import {
  api, ApiError, ApiInfo, Definition, Issue, Job, JobEvent, Profile, Progress, Record_, RunSummary, Session, SessionSummary,
  SourceProgress, terminal,
} from './api';

// New sessions start empty: configuration normally comes from an AI session
// (Create configuration with AI -> Load AI-generated configuration).
const EMPTY_DEFINITION: Definition = {
  queryInput: '',
  query: {},
  searchSources: [],
  output: { format: 'csv', fields: [] },
};

// The bundled demo configuration, offered on empty sessions.
const DEMO_DEFINITION: Definition = {
  queryInput: "I'm only looking for EV cars after 2024",
  query: {
    params: { fuel: 'electric', yearMin: 2024 },
    filters: [
      { field: 'fuelType', op: 'eq', value: 'electric' },
      { field: 'year', op: 'gte', value: 2024 },
    ],
  },
  searchSources: [
    { id: 'demo-motors', profileRef: 'demo-motors' },
    { id: 'demo-ev-api', profileRef: 'demo-ev-api' },
  ],
  output: {
    format: 'csv',
    fields: [
      { name: 'make', type: 'string', required: true },
      { name: 'model', type: 'string', required: true },
      { name: 'year', type: 'integer', required: true },
      { name: 'price', type: 'decimal', required: true },
      { name: 'kilometres', type: 'integer', required: true },
      { name: 'batterySizeKwh', type: 'decimal' },
      { name: 'fuelType', type: 'string', required: true },
      { name: 'location', type: 'string' },
      { name: 'url', type: 'url', required: true },
    ],
  },
};

const EVENT_TYPES = [
  'JobStarted', 'SourceStarted', 'PageDiscovered', 'AcquisitionAttempt', 'PageFetched', 'RecordFound', 'RecordAccepted',
  'RecordRejected', 'Warning', 'Error', 'SourceCompleted', 'JobPausing', 'JobPaused', 'JobResumed',
  'JobFinalising', 'JobCompleted', 'JobPartial', 'JobCancelled', 'JobFailed',
];

interface LogLine { seq: number; type: string; source?: string; text: string }

// ---------------------------------------------------------------------------
// Routing: #/sessions/<id> and #/sessions/<id>/runs/<runId>
// ---------------------------------------------------------------------------

interface Route { sessionId?: string; runId?: string }

function parseRoute(hash: string): Route {
  const m = hash.match(/^#\/sessions\/([^/]+)(?:\/runs\/([^/]+))?/);
  return m ? { sessionId: decodeURIComponent(m[1]), runId: m[2] && decodeURIComponent(m[2]) } : {};
}

function go(sessionId?: string, runId?: string) {
  window.location.hash = sessionId ? `#/sessions/${sessionId}${runId ? `/runs/${runId}` : ''}` : '#/';
}

function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return route;
}

// ---------------------------------------------------------------------------
// App shell: session list + selected session
// ---------------------------------------------------------------------------

function App() {
  const route = useRoute();
  const [sessions, setSessions] = useState<SessionSummary[] | null>(null);
  const [info, setInfo] = useState<ApiInfo | null>(null);
  const [apiProblem, setApiProblem] = useState('');
  const [error, setError] = useState('');

  const refresh = useCallback(() => {
    api.sessions().then(setSessions).catch((e) => setError(e.message));
  }, []);

  useEffect(() => {
    // Check the API first so an old or missing API is explained, not shown
    // as a generic error (Vite reloads the UI; the Go API must be restarted).
    api.info()
      .then((i) => {
        if (!i.features?.includes('sessions')) throw new ApiError('Not Found');
        setInfo(i);
        refresh();
      })
      .catch((e: Error) =>
        setApiProblem(
          e.message === 'Not Found'
            ? 'The API answering on /api is an older build without Sessions. Stop it and start the current one from the repository root: go run ./apps/api'
            : `Cannot reach the API (${e.message}). Start it from the repository root: go run ./apps/api`,
        ),
      );
  }, [refresh]);

  // With no session selected, open the most recently used one.
  useEffect(() => {
    if (!route.sessionId && sessions && sessions.length > 0) {
      window.location.replace(`#/sessions/${sessions[0].id}`);
    }
  }, [route.sessionId, sessions]);

  const newSession = async () => {
    try {
      const { session } = await api.createSession('New session', EMPTY_DEFINITION);
      refresh();
      go(session.id);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="layout">
      <aside className="sidebar">
        <p className="eyebrow">Prompt Driven Scraper</p>
        <button className="primary wide" onClick={newSession} disabled={!info}>+ New Session</button>
        <h3>Sessions</h3>
        {sessions === null && !apiProblem && <p className="muted small">Loading…</p>}
        {sessions?.length === 0 && <p className="muted small">No sessions yet.</p>}
        <ul className="session-list">
          {sessions?.map((s) => (
            <li key={s.id}>
              <a href={`#/sessions/${s.id}`} className={s.id === route.sessionId ? 'active' : ''}>
                <strong>{s.name}</strong>
                <small className="muted">
                  {s.lastRun ? (
                    <>
                      <span className={`dot ${s.lastRun.status}`} /> {s.lastRun.status} · {s.runCount} run{s.runCount === 1 ? '' : 's'}
                    </>
                  ) : (
                    'not run yet'
                  )}
                </small>
              </a>
            </li>
          ))}
        </ul>
        {info && (
          <p className={`storage small ${info.store === 'memory' ? 'warn' : 'muted'}`}>
            {info.store === 'sqlite'
              ? <>Saved in SQLite: <code>{info.database}</code></>
              : info.store === 'memory'
                ? 'In-memory storage: sessions are lost when the API stops.'
                : `Storage: ${info.store}`}
          </p>
        )}
      </aside>

      <main className="shell">
        {apiProblem && <p className="banner">{apiProblem}</p>}
        {error && <p className="notice">{error}</p>}
        {route.sessionId ? (
          <SessionView key={route.sessionId} id={route.sessionId} runId={route.runId} onChanged={refresh} />
        ) : (
          sessions?.length === 0 && (
            <header>
              <h1>Deterministic scraping, profile driven.</h1>
              <p className="lede">
                AI assistants write the configuration. The runtime never calls one: same configuration, same content,
                same results. Sessions and their runs are saved, so you can come back to them later.
              </p>
              <ol className="workflow">
                <li>Create a session</li>
                <li>Copy the AI authoring prompt into ChatGPT or Claude and describe what to scrape</li>
                <li>Load the configuration it produces</li>
                <li>Check Query, Search Sources and Output, then Run</li>
                <li>Come back to previous runs at any time</li>
              </ol>
              <button className="primary" onClick={newSession} disabled={!info}>+ New Session</button>
            </header>
          )
        )}
      </main>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Session: editable current configuration + its runs
// ---------------------------------------------------------------------------

interface Editor {
  name: string;
  queryInput: string;
  queryText: string;
  selected: string[];
  outputText: string;
  format: 'csv' | 'json';
}

function toEditor(s: Session): Editor {
  const d = s.definition ?? {};
  const fields = d.output?.fields ?? [];
  return {
    name: s.name,
    queryInput: d.queryInput ?? '',
    queryText: JSON.stringify(d.query ?? {}, null, 2),
    selected: (d.searchSources ?? []).map((src) => src.profileRef ?? src.profile?.id ?? src.id),
    outputText: JSON.stringify(Object.fromEntries(fields.map((f) => [f.name, f.required ? f.type : `${f.type}?`])), null, 2),
    format: d.output?.format === 'json' ? 'json' : 'csv',
  };
}

function toDefinition(e: Editor): Definition {
  let query: unknown, fields: Record<string, string>;
  try {
    query = JSON.parse(e.queryText || '{}');
  } catch (err) {
    throw new Error(`Structured query is not valid JSON: ${(err as Error).message}`);
  }
  try {
    fields = JSON.parse(e.outputText || '{}');
  } catch (err) {
    throw new Error(`Output schema is not valid JSON: ${(err as Error).message}`);
  }
  return {
    queryInput: e.queryInput,
    query,
    searchSources: e.selected.map((id) => ({ id, profileRef: id })),
    output: {
      format: e.format,
      fields: Object.entries(fields).map(([name, t]) => ({
        name,
        type: String(t).replace(/\?$/, ''),
        required: !String(t).endsWith('?'),
      })),
    },
  };
}

function SessionView({ id, runId, onChanged }: { id: string; runId?: string; onChanged: () => void }) {
  const [saved, setSaved] = useState<Editor | null>(null);
  const [ed, setEd] = useState<Editor | null>(null);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [issues, setIssues] = useState<Issue[]>([]);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  const loadRuns = useCallback(async () => {
    const res = await api.session(id);
    setRuns(res.runs);
    return res;
  }, [id]);

  useEffect(() => {
    loadRuns()
      .then((res) => {
        const e = toEditor(res.session);
        setSaved(e);
        setEd(e);
        setIssues(res.issues);
      })
      .catch((e) => setNotice(e.message));
    api.profiles().then(setProfiles).catch(() => {});
  }, [loadRuns]);

  const dirty = useMemo(() => JSON.stringify(ed) !== JSON.stringify(saved), [ed, saved]);
  if (!ed) return <p className="muted">{notice || 'Loading session…'}</p>;
  const set = (patch: Partial<Editor>) => setEd({ ...ed, ...patch });

  const save = async (): Promise<boolean> => {
    setNotice('');
    let def: Definition;
    try {
      def = toDefinition(ed);
    } catch (e) {
      setNotice((e as Error).message);
      return false;
    }
    try {
      const res = await api.updateSession(id, ed.name, def);
      const e = toEditor(res.session);
      setSaved(e);
      setEd(e);
      setIssues(res.issues);
      onChanged();
      return true;
    } catch (e) {
      setNotice((e as Error).message);
      return false;
    }
  };

  const run = async () => {
    setBusy(true);
    try {
      if (dirty && !(await save())) return;
      const job = await api.runSession(id);
      await loadRuns();
      onChanged();
      go(id, job.id);
    } catch (e) {
      const err = e as ApiError;
      setNotice(err.message);
      if (err.issues?.length) setIssues(err.issues);
    } finally {
      setBusy(false);
    }
  };

  const importConfig = async (text: string) => {
    setNotice('');
    try {
      const res = await api.validate(text);
      if (res.error) return setNotice(res.error);
      setIssues(res.issues ?? []);
      if (res.kind === 'profile') {
        if (!res.valid) return setNotice('Profile has errors and was not added.');
        await api.saveProfile(text);
        setProfiles(await api.profiles());
        set({ selected: Array.from(new Set([...ed.selected, res.profile!.id])) });
        return setNotice(`Added source profile "${res.profile!.id}". Save to keep it in this session.`);
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
      setProfiles(await api.profiles());
      setEd({
        ...toEditor({ id, name: job.name || ed.name, definition: job as Definition, createdAt: '', updatedAt: '' }),
        selected: refs,
      });
      setNotice(res.valid ? 'Configuration loaded. Save or run to keep it.' : 'Configuration loaded with errors - see below.');
    } catch (e) {
      setNotice((e as Error).message);
    }
  };

  return (
    <>
      <div className="session-head">
        <input
          className="session-name"
          value={ed.name}
          onChange={(e) => set({ name: e.target.value })}
          aria-label="Session name"
        />
        <div className="actions">
          <span className="muted small">{dirty ? 'Unsaved changes' : 'Saved'}</span>
          {runId ? (
            <>
              <a className="button" href={`#/sessions/${id}`}>Edit configuration</a>
              <button className="primary" onClick={run} disabled={busy || ed.selected.length === 0}>Run again</button>
            </>
          ) : (
            dirty && <button onClick={save} disabled={busy}>Save</button>
          )}
        </div>
      </div>
      {notice && <p className="notice">{notice}</p>}
      {issues.some((i) => i.severity === 'error') && !isEmpty(ed) && <Issues issues={issues} />}

      {runId ? (
        <RunView key={runId} runId={runId} sessionId={id} onStatusChange={() => { loadRuns(); onChanged(); }} />
      ) : (
        <>
          <AiPromptPanel />
          <ImportPanel onImport={importConfig} disabled={false} />
          {isEmpty(ed) && (
            <p className="muted small demo-hint">
              This session is empty. Load a configuration above, fill in the panels below, or{' '}
              <button className="link" onClick={() => setEd({ ...toEditor({ id, name: ed.name, definition: DEMO_DEFINITION, createdAt: '', updatedAt: '' }) })}>
                use the bundled demo example
              </button>
              .
            </p>
          )}
          <ConfigPanels ed={ed} set={set} profiles={profiles} />
          <div className="runbar">
            <button className="primary" onClick={run} disabled={busy || ed.selected.length === 0}
              title={ed.selected.length === 0 ? 'Select at least one search source' : 'Save and start a new run'}>
              <span className="step light">4</span>{runs.length ? 'Run again' : 'Run'}
            </button>
            <button onClick={save} disabled={!dirty || busy}>Save without running</button>
            {ed.selected.length === 0 && <span className="muted small">Select at least one search source to run.</span>}
          </div>
        </>
      )}

      <RunsTable sessionId={id} runs={runs} openRunId={runId} />
    </>
  );
}

function isEmpty(e: Editor) {
  return !e.queryInput && e.selected.length === 0 && (e.outputText === '{}' || !e.outputText.trim());
}

function ConfigPanels({ ed, set, profiles }: { ed: Editor; set: (p: Partial<Editor>) => void; profiles: Profile[] }) {
  return (
    <section className="grid">
      <div className="panel">
        <h2><span className="step">1</span>Query input</h2>
        <textarea className="lead-input" value={ed.queryInput} onChange={(e) => set({ queryInput: e.target.value })} rows={2} />
        <label className="sub">
          Deterministic query <small>params for source requests, filters applied to every record</small>
          <textarea className="code" value={ed.queryText} onChange={(e) => set({ queryText: e.target.value })} rows={9} />
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
                  checked={ed.selected.includes(p.id)}
                  onChange={(e) =>
                    set({ selected: e.target.checked ? [...ed.selected, p.id] : ed.selected.filter((x) => x !== p.id) })
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
        <textarea className="code" value={ed.outputText} onChange={(e) => set({ outputText: e.target.value })} rows={11} />
        <div className="row">
          <label className="check inline">
            <input type="radio" checked={ed.format === 'csv'} onChange={() => set({ format: 'csv' })} /> CSV
          </label>
          <label className="check inline">
            <input type="radio" checked={ed.format === 'json'} onChange={() => set({ format: 'json' })} /> JSON
          </label>
          <span className="muted small">"decimal?" = optional</span>
        </div>
      </div>
    </section>
  );
}

function RunsTable({ sessionId, runs, openRunId }: { sessionId: string; runs: RunSummary[]; openRunId?: string }) {
  return (
    <section className="panel">
      <div className="run-head">
        <h2>Previous runs</h2>
        {openRunId && <a href={`#/sessions/${sessionId}`}>Edit configuration</a>}
      </div>
      {runs.length === 0 ? (
        <p className="muted small">Not run yet. Each run keeps its own copy of the configuration and results.</p>
      ) : (
        <table className="runs">
          <thead>
            <tr><th>Run</th><th>Status</th><th>Started</th><th>Duration</th><th>Results</th><th>Warn/Err</th><th /></tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id} className={r.id === openRunId ? 'selected' : ''}>
                <td><strong>#{r.runNumber}</strong></td>
                <td><span className={`pill small ${r.status}`}>{r.status}</span></td>
                <td>{formatTime(r.startedAt ?? r.createdAt)}</td>
                <td>{formatDuration(r.runningMs)}</td>
                <td>{r.recordsAccepted}</td>
                <td>{r.warnings}/{r.errors}</td>
                <td><a href={`#/sessions/${sessionId}/runs/${r.id}`}>Open</a></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

// AiPromptPanel gets an AI assistant into the right context. The prompt is
// served by the API, which builds it from ai/PROFILE_AUTHORING.md, the schemas
// and the examples, so the UI never holds its own copy.
function AiPromptPanel() {
  const [prompt, setPrompt] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    // Fetched up front so copying happens directly inside the click handler,
    // which some browsers require for clipboard access.
    api.authoringPrompt().then(setPrompt).catch((e) => setError(`Prompt unavailable: ${e.message}`));
  }, []);

  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  return (
    <section className="panel ai-prompt">
      <div>
        <h2>Create configuration with AI</h2>
        <p className="muted small">
          Starting a new ChatGPT or Claude session? Copy the authoring prompt first. It teaches the AI the exact
          configuration language this scraper supports. Then describe what you want to scrape and paste the
          configuration it gives you below.
        </p>
        {error && <p className="small error-text">{error}</p>}
      </div>
      <div className="actions">
        <CopyButton text={prompt} label="Copy AI authoring prompt" className="primary" />
        <button onClick={() => setOpen(true)} disabled={!prompt}>View prompt</button>
      </div>
      <dialog ref={dialog} className="prompt-dialog" onClose={() => setOpen(false)}>
        <div className="dialog-head">
          <h2>AI authoring prompt</h2>
          <div className="actions">
            <CopyButton text={prompt} label="Copy" className="primary" />
            <button onClick={() => setOpen(false)}>Close</button>
          </div>
        </div>
        <p className="muted small">
          Built from <code>ai/PROFILE_AUTHORING.md</code>, the JSON Schemas and the example configuration.
        </p>
        <pre className="prompt-text">{prompt}</pre>
      </dialog>
    </section>
  );
}

function CopyButton({ text, label, className }: { text: string | null; label: string; className?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  useEffect(() => {
    if (state === 'idle') return;
    const t = setTimeout(() => setState('idle'), 2000);
    return () => clearTimeout(t);
  }, [state]);
  const copy = async () => {
    if (!text) return;
    setState((await copyText(text)) ? 'copied' : 'failed');
  };
  return (
    <button className={className} onClick={copy} disabled={!text} aria-live="polite">
      {state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label}
    </button>
  );
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Clipboard API is unavailable on plain-HTTP origins other than localhost.
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  }
}

function ImportPanel({ onImport, disabled }: { onImport: (text: string) => void; disabled: boolean }) {
  const [text, setText] = useState('');
  return (
    <details className="panel import">
      <summary>Load AI-generated configuration</summary>
      <p className="muted small">
        Paste the job or source profile (YAML or JSON) your AI assistant produced. It is validated against the DSL;
        unsupported keys are rejected, not ignored.
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

// RunView shows one run. Its state comes from the run document plus the
// run's event stream, which replays history for finished runs and continues
// live for running ones.
function RunView({ runId, sessionId, onStatusChange }: { runId: string; sessionId: string; onStatusChange: () => void }) {
  const [job, setJob] = useState<Job | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    api.run(runId).then(setJob).catch((e) => setError(e.message));
  }, [runId]);
  if (error) return <p className="notice">{error}</p>;
  if (!job) return <p className="muted">Loading run…</p>;
  return <RunPanel job={job} sessionId={sessionId} onUpdate={setJob} onStatusChange={onStatusChange} />;
}

function RunPanel({ job, sessionId, onUpdate, onStatusChange }: { job: Job; sessionId: string; onUpdate: (job: Job) => void; onStatusChange: () => void }) {
  const columns = (job.definition.output?.fields ?? []).map((f) => f.name);
  const [records, setRecords] = useState<Record_[]>([]);
  const [events, setEvents] = useState<LogLine[]>([]);
  const [showAll, setShowAll] = useState(false);
  const [log, setLog] = useState<LogLine[]>([]);
  const [progressAt, setProgressAt] = useState(Date.now());
  const [, tick] = useState(0);
  const [busy, setBusy] = useState(false);
  const jobRef = useRef(job);
  jobRef.current = job;

  useEffect(() => {
    const wasTerminal = terminal(job.status);
    const es = api.events(job.id);
    const handle = (msg: MessageEvent) => {
      const ev = JSON.parse(msg.data) as JobEvent;
      if (ev.progress) {
        onUpdate({ ...jobRef.current, status: ev.status, progress: ev.progress, statusReason: reasonOf(ev, jobRef.current.statusReason) });
        setProgressAt(Date.now());
      }
      if (ev.type === 'RecordAccepted' && ev.data?.record) {
        setRecords((rs) => [...rs, ev.data!.record as Record_]);
      }
      const line = logLine(ev);
      if (line) setLog((l) => [line, ...l].slice(0, 500));
      setEvents((l) => [{ seq: ev.seq, type: ev.type, source: ev.sourceId, text: ev.message ?? '' }, ...l].slice(0, 2000));
      // Keep the session's run list in step with live status changes.
      if (!wasTerminal && ev.type.startsWith('Job')) onStatusChange();
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
          <h2 className="run-title">Run #{job.runNumber ?? '?'}</h2>
          <span className={`pill ${job.status}`}>{job.status}</span>
          <span className="muted"> {formatDuration(elapsed)}</span>
          {p.currentSource && live && <span className="muted"> · on {p.currentSource}</span>}
          {job.statusReason && <div className="muted small">{job.statusReason}</div>}
          <div className="muted small">
            created {formatTime(job.createdAt)}
            {job.startedAt && <> · started {formatTime(job.startedAt)}</>}
            {job.finishedAt && <> · finished {formatTime(job.finishedAt)}</>}
          </div>
        </div>
        <div className="actions">
          {!done && (
            <>
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
              <button className="danger" onClick={() => act('cancel')} disabled={busy}>Cancel</button>
            </>
          )}
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
          <tr><th>Source</th><th>Status</th><th>Progress</th><th>Pages loaded</th><th>Found</th><th>Accepted</th><th>Rejected</th><th>Dupes</th><th>Warn/Err</th></tr>
        </thead>
        <tbody>
          {p.sources.map((s) => <SourceRow key={s.id} s={s} />)}
        </tbody>
      </table>

      <SourceDiagnostics sources={p.sources} />

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
          <div className="run-head">
            <h3>{showAll ? 'All events' : 'Warnings, errors & rejections'}</h3>
            <label className="check inline small">
              <input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} /> all events
            </label>
          </div>
          <ul className="log scroll">
            {(showAll ? events : log).map((l) => (
              <li key={l.seq} className={l.type}>
                <b>{label(l.type)}</b> {l.source && <span className="muted">[{l.source}]</span>} {l.text}
              </li>
            ))}
            {(showAll ? events : log).length === 0 && <li className="muted">Nothing yet.</li>}
          </ul>
        </div>
      </div>

      <details className="snapshot">
        <summary>Configuration snapshot used by this run</summary>
        <p className="muted small">
          Fixed when the run started, with source profiles inline. Later edits to the session or its profiles do not
          change it. <a href={`#/sessions/${sessionId}`}>Edit the session's current configuration</a>
        </p>
        <pre className="snapshot-json">{JSON.stringify(job.definition, null, 2)}</pre>
      </details>
    </section>
  );
}

// SourceDiagnostics tells apart the three ways a source can come up empty:
// the page could not be acquired, it was acquired but nothing was extracted,
// or records were extracted and then rejected by the filters.
function SourceDiagnostics({ sources }: { sources: SourceProgress[] }) {
  const shown = sources.filter((s) => s.status !== 'pending');
  if (shown.length === 0) return null;
  return (
    <div className="diagnostics">
      {shown.map((s) => {
        const attempted = s.pagesAttempted ?? s.pagesVisited;
        const loaded = s.pagesVisited;
        const used = (s.acquisition ?? []).filter((a) => a.attempts > 0);
        const extractionFailed = s.failureStage === 'extraction' || (loaded > 0 && s.recordsFound === 0 && s.status !== 'running');
        return (
          <div key={s.id} className="diag">
            <strong>{s.name ?? s.id}</strong>
            <dl>
              <dt>Acquisition</dt>
              <dd>
                {used.length === 0 && <span className="muted">not started</span>}
                {used.map((a, i) => {
                  const ok = a.succeeded > 0;
                  const label = ({ http: 'HTTP', browser: 'Browser' } as Record<string, string>)[a.strategy] ?? a.strategy;
                  return (
                    <span key={a.strategy} className={`attempt ${ok ? 'ok' : 'bad'}`}>
                      {ok ? '✓' : '✗'} {label}{i > 0 ? ' fallback' : ''}: {ok && a.failed === 0 ? 'page loaded' : a.lastResult}
                      <small className="muted"> ({a.succeeded}/{a.attempts} ok)</small>
                    </span>
                  );
                })}
                <small className="muted"> · pages loaded {loaded} of {attempted} attempted</small>
              </dd>
              <dt>Extraction</dt>
              <dd>
                {loaded === 0 ? (
                  <span className="muted">not reached (no page acquired)</span>
                ) : extractionFailed ? (
                  <span className="attempt bad">
                    ✗ {s.recordsFound} items on {loaded} loaded page{loaded === 1 ? '' : 's'}: check listing.items and the field rules
                  </span>
                ) : (
                  <span className="attempt ok">✓ {s.recordsFound} items found on {loaded} loaded page{loaded === 1 ? '' : 's'}</span>
                )}
              </dd>
              <dt>Filtering</dt>
              <dd>
                {s.recordsFound === 0 ? (
                  <span className="muted">not reached</span>
                ) : (
                  <span>
                    {s.recordsAccepted} accepted · {s.recordsRejected} rejected · {s.duplicates} duplicate{s.duplicates === 1 ? '' : 's'}
                    {s.recordsAccepted === 0 && <span className="attempt bad"> (all rejected: see rejection reasons)</span>}
                  </span>
                )}
              </dd>
            </dl>
            {s.status === 'failed' && (
              <p className="attempt bad">Source failed at {s.failureStage ?? 'an unknown stage'}: {s.failureReason}</p>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Totals({ p }: { p: Progress }) {
  const items: [string, React.ReactNode][] = [
    ['sources', `${p.sourcesComplete}/${p.sourcesTotal}`],
    ['pages loaded / attempted', `${p.pagesVisited}/${p.pagesAttempted ?? p.pagesVisited}`],
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
          <dd>{v}</dd>
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
      <td title="loaded / attempted">{s.pagesVisited}<span className="muted">/{s.pagesAttempted ?? s.pagesVisited}</span></td>
      <td>{s.recordsFound}</td>
      <td>{s.recordsAccepted}</td>
      <td>{s.recordsRejected}</td>
      <td>{s.duplicates}</td>
      <td>{s.warnings}/{s.errors}</td>
    </tr>
  );
}

function logLine(ev: JobEvent): LogLine | null {
  if (ev.type === 'AcquisitionAttempt') {
    return ev.data?.ok ? null : { seq: ev.seq, type: ev.type, source: ev.sourceId, text: `${ev.message} (${ev.data?.url})` };
  }
  if (!['Warning', 'Error', 'RecordRejected', 'JobFailed', 'JobPartial', 'JobCancelled'].includes(ev.type)) return null;
  return { seq: ev.seq, type: ev.type, source: ev.sourceId, text: ev.message ?? '' };
}

function reasonOf(ev: JobEvent, current?: string) {
  if (['JobPartial', 'JobFailed', 'JobCancelled', 'JobPaused'].includes(ev.type)) return ev.message;
  if (ev.type === 'JobResumed') return undefined;
  return current;
}

function label(type: string) {
  return ({ RecordRejected: 'rejected', Warning: 'warning', Error: 'error', AcquisitionAttempt: 'acquisition' } as Record<string, string>)[type] ?? type;
}

function formatTime(iso?: string) {
  if (!iso) return '';
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
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
