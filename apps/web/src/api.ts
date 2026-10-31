// Minimal mirror of pkg/contracts for the UI. The Go types are authoritative.

export type JobStatus =
  | 'queued' | 'running' | 'pausing' | 'paused' | 'finalising'
  | 'completed' | 'partial' | 'cancelled' | 'failed';

export const terminal = (s: JobStatus) =>
  s === 'completed' || s === 'partial' || s === 'cancelled' || s === 'failed';

export interface SourceProgress {
  id: string;
  name?: string;
  status: 'pending' | 'running' | 'completed' | 'stopped' | 'skipped' | 'failed';
  pagesVisited: number;
  pagesDiscovered: number;
  listingPagesTotal?: number;
  listingPagesVisited: number;
  recordsFound: number;
  recordsAccepted: number;
  recordsRejected: number;
  duplicates: number;
  warnings: number;
  errors: number;
}

export interface Progress {
  currentSource?: string;
  sourcesTotal: number;
  sourcesComplete: number;
  pagesVisited: number;
  pagesDiscovered: number;
  recordsFound: number;
  recordsAccepted: number;
  recordsRejected: number;
  duplicates: number;
  warnings: number;
  errors: number;
  elapsedMs: number;
  sources: SourceProgress[];
}

export interface Job {
  id: string;
  status: JobStatus;
  statusReason?: string;
  progress: Progress;
}

export interface JobEvent {
  seq: number;
  type: string;
  sourceId?: string;
  message?: string;
  timestamp: string;
  status: JobStatus;
  progress?: Progress;
  data?: Record<string, unknown>;
}

export interface Record_ {
  sourceId: string;
  values: Record<string, unknown>;
  provenance?: Record<string, string>;
  partial?: boolean;
}

export interface Issue { path: string; message: string; severity: 'error' | 'warning' }

export interface Profile { id: string; name?: string; description?: string; format?: string; request: { url: string } }

export interface ValidateResult {
  kind: 'job' | 'profile';
  valid: boolean;
  error?: string;
  issues?: Issue[];
  job?: {
    name: string;
    queryInput: string;
    query?: unknown;
    searchSources: { id: string; profileRef?: string; profile?: Profile }[];
    output: { format: string; fields: { name: string; type: string; required?: boolean }[] };
  };
  profile?: Profile;
}

export class ApiError extends Error {
  constructor(message: string, public issues: Issue[] = []) { super(message); }
}

async function call<T>(method: string, path: string, body?: string): Promise<T> {
  const res = await fetch(path, { method, body, headers: body ? { 'Content-Type': 'application/json' } : undefined });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(data.error ?? res.statusText, data.issues ?? []);
  return data as T;
}

async function text(path: string): Promise<string> {
  const res = await fetch(path);
  if (!res.ok) throw new ApiError(`${res.status} ${res.statusText}`);
  return res.text();
}

export const api = {
  authoringPrompt: () => text('/api/ai/authoring-prompt'),
  profiles: () => call<Profile[]>('GET', '/api/profiles'),
  saveProfile: (text: string) => call<{ profile: Profile; issues: Issue[] }>('POST', '/api/profiles', text),
  validate: (text: string) => call<ValidateResult>('POST', '/api/validate', text),
  createJob: (job: unknown) => call<Job>('POST', '/api/jobs', JSON.stringify(job)),
  control: (id: string, action: 'pause' | 'resume' | 'cancel' | 'finalise') =>
    call<Job>('POST', `/api/jobs/${id}/${action}`),
  events: (id: string) => new EventSource(`/api/jobs/${id}/events`),
  outputUrl: (id: string) => `/api/jobs/${id}/output`,
};
