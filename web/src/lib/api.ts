import type {
	CheckResult,
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

/**
 * `timeoutMs` is only ever reported, never enforced — the caller supplies its
 * own signal. It is threaded through so the message names the bound that
 * actually elapsed: a page waits far longer than a discovery, and telling the
 * operator it gave up after 35 seconds when it waited 95 sends them looking for
 * the wrong thing.
 */
async function request<T>(
	path: string,
	init?: RequestInit,
	timeoutMs = REQUEST_TIMEOUT_MS
): Promise<T> {
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
				`서버가 ${Math.round(timeoutMs / 1000)}초 안에 응답하지 않았습니다.`,
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

/**
 * PAGE_TIMEOUT_MS bounds a page or panel fetch, for the same reason and with
 * the same ordering: it sits just past the server's own 90s page budget
 * (api.pageBudget), so a page that runs long still gets to answer with the
 * warnings it built rather than being cut off by the browser.
 *
 * Without it there was no bound at all. If the server never answered — a
 * WriteTimeout closing the connection, a process killed mid-request — the
 * skeleton pulsed indefinitely with nothing on screen to say why.
 */
const PAGE_TIMEOUT_MS = 95_000;

/**
 * Combines the caller's cancellation with the page timeout. PageView aborts the
 * in-flight request whenever the selection changes, and that signal has to
 * survive: it is how an abandoned request is told apart from a failed one.
 */
function pageSignal(signal?: AbortSignal): AbortSignal {
	const timeout = AbortSignal.timeout(PAGE_TIMEOUT_MS);
	return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

/** Builds the query string shared by every data endpoint. */
function windowQuery(range: string, period: string, namespace = ''): string {
	const q = new URLSearchParams();
	if (range) q.set('range', range);
	if (period) q.set('period', period);
	if (namespace) q.set('namespace', namespace);
	const s = q.toString();
	return s ? `?${s}` : '';
}

function normalizeConfig(config: Config): Config {
	return {
		...config,
		targetGroups: config.targetGroups ?? [],
		rdsProxies: config.rdsProxies ?? [],
		webAcls: config.webAcls ?? [],
		wafHeaders: config.wafHeaders ?? [],
		logFormat: {
			...config.logFormat,
			okStatuses: config.logFormat.okStatuses ?? [],
			excludePaths: config.logFormat.excludePaths ?? []
		}
	};
}

export const api = {
	meta: () => request<Meta>('/api/meta'),
	identity: () => request<Identity>('/api/identity'),
	health: () => request<{ ok: boolean; credentials: boolean }>('/api/health'),

	page: (id: string, range: string, period: string, signal?: AbortSignal, namespace = '') =>
		request<Payload>(
			`/api/page/${id}${windowQuery(range, period, namespace)}`,
			{ signal: pageSignal(signal) },
			PAGE_TIMEOUT_MS
		),

	panel: (id: string, range: string, period: string, signal?: AbortSignal) =>
		request<Payload>(
			`/api/panel/${id}${windowQuery(range, period)}`,
			{ signal: pageSignal(signal) },
			PAGE_TIMEOUT_MS
		),

	config: async () => normalizeConfig(await request<Config>('/api/config')),

	/**
	 * POST, though it reads: every call sends a real request to a real service.
	 * Its own timeout is longer than the server's 10s probe budget, so the
	 * server's answer — which knows *what* failed — wins the race.
	 */
	check: () =>
		request<CheckResult>('/api/check', {
			method: 'POST',
			signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS)
		}),

	/**
	 * Normalised on the way back for the same reason `config` is: the response
	 * is the stored config, and the form binds straight to it, so one list that
	 * came back `null` would take the whole page down on the next `.filter`.
	 */
	saveConfig: async (cfg: Config) =>
		normalizeConfig(
			await request<Config>('/api/config', { method: 'PUT', body: JSON.stringify(cfg) })
		),

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
