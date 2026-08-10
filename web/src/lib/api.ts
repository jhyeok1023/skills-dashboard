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
	let res: Response;
	try {
		res = await fetch(path, {
			...init,
			headers: {
				Accept: 'application/json',
				...(init?.body ? { 'Content-Type': 'application/json' } : {}),
				...init?.headers
			}
		});
	} catch (e) {
		// A transport failure never reached the server, so there is no body to
		// read a reason out of — and the raw rejection is a bare "TypeError:
		// Failed to fetch", which a caller renders verbatim into the page. Turn
		// it into the same shape as every other failure, with words that say
		// which of the two very different things happened.
		//
		// A deliberate cancel is passed through untouched: PageView tells an
		// abandoned request apart from a real error by its name, and wrapping it
		// would make every range change look like a failure.
		if (e instanceof DOMException && e.name === 'AbortError') throw e;
		if (e instanceof DOMException && e.name === 'TimeoutError') {
			throw new ApiFailure(
				0,
				`서버가 ${Math.round(REQUEST_TIMEOUT_MS / 1000)}초 안에 응답하지 않았습니다.`,
				'대시보드를 실행한 터미널의 로그를 확인하세요.'
			);
		}
		throw new ApiFailure(
			0,
			'서버에 연결하지 못했습니다.',
			'대시보드가 아직 실행 중인지 확인하세요.'
		);
	}

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

/**
 * REQUEST_TIMEOUT_MS bounds a discovery call from the browser's side.
 *
 * It is deliberately longer than the server's own 30s budget for a discovery
 * (api.handleDiscovery). The server knows *why* a listing failed — the denied
 * permission, the throttled account — and that message is worth far more than
 * "timed out", so the server's answer has to win the race. This only catches
 * the case where nothing comes back at all, which is otherwise a button that
 * says 조회 중… forever: fetch has no timeout of its own.
 */
const REQUEST_TIMEOUT_MS = 35_000;

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
		return request<DiscoveryResponse>(`/api/discovery/${kind}${q}`, {
			signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS)
		});
	},

	previewLogFormat: (sample: string, format?: LogFormat) =>
		request<LogFormatPreview>('/api/logfmt/preview', {
			method: 'POST',
			body: JSON.stringify({ sample, format })
		})
};
