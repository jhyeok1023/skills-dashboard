import type {
	Config,
	DiscoveryResponse,
	Identity,
	LogFormat,
	LogFormatPreview,
	Meta,
	Payload
} from './types';

/** Raised for any non-2xx response, carrying the server's explanation. */
export class ApiFailure extends Error {
	readonly status: number;
	readonly hint: string;

	constructor(status: number, message: string, hint = '') {
		super(message);
		this.name = 'ApiFailure';
		this.status = status;
		this.hint = hint;
	}

	/** True when the dashboard has no usable AWS credentials. */
	get isCredentialProblem(): boolean {
		return this.status === 503;
	}
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(path, {
		...init,
		headers: {
			Accept: 'application/json',
			...(init?.body ? { 'Content-Type': 'application/json' } : {}),
			...init?.headers
		}
	});

	if (!res.ok) {
		let detail = `${res.status} ${res.statusText}`;
		let hint = '';
		try {
			const body = await res.json();
			if (body?.detail) detail = body.detail;
			if (body?.hint) hint = body.hint;
		} catch {
			// A non-JSON error body is still an error; the status line stands in.
		}
		throw new ApiFailure(res.status, detail, hint);
	}
	return (await res.json()) as T;
}

/** Builds the query string shared by every data endpoint. */
function windowQuery(range: string, period: string): string {
	const q = new URLSearchParams();
	if (range) q.set('range', range);
	if (period) q.set('period', period);
	const s = q.toString();
	return s ? `?${s}` : '';
}

export const api = {
	meta: () => request<Meta>('/api/meta'),
	identity: () => request<Identity>('/api/identity'),
	health: () => request<{ ok: boolean; credentials: boolean }>('/api/health'),

	page: (id: string, range: string, period: string, signal?: AbortSignal) =>
		request<Payload>(`/api/page/${id}${windowQuery(range, period)}`, { signal }),

	panel: (id: string, range: string, period: string, signal?: AbortSignal) =>
		request<Payload>(`/api/panel/${id}${windowQuery(range, period)}`, { signal }),

	config: () => request<Config>('/api/config'),

	saveConfig: (cfg: Config) =>
		request<Config>('/api/config', { method: 'PUT', body: JSON.stringify(cfg) }),

	discover: (kind: string, prefix = '') => {
		const q = prefix ? `?prefix=${encodeURIComponent(prefix)}` : '';
		return request<DiscoveryResponse>(`/api/discovery/${kind}${q}`);
	},

	previewLogFormat: (sample: string, format?: LogFormat) =>
		request<LogFormatPreview>('/api/logfmt/preview', {
			method: 'POST',
			body: JSON.stringify({ sample, format })
		})
};
