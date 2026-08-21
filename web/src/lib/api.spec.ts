import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiFailure, api } from './api';

afterEach(() => {
	vi.unstubAllGlobals();
});

function respondWith(body: unknown, init: ResponseInit = {}) {
	vi.stubGlobal(
		'fetch',
		vi.fn().mockResolvedValue(
			new Response(JSON.stringify(body), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
				...init
			})
		)
	);
}

describe('request failures', () => {
	it('reports a lookup that never came back rather than hanging', async () => {
		// fetch has no timeout of its own. Before discover() carried a signal,
		// a response that never arrived left the settings button reading
		// 조회 중… with no way to ever leave that state.
		vi.stubGlobal(
			'fetch',
			vi.fn().mockRejectedValue(new DOMException('The operation timed out.', 'TimeoutError'))
		);

		await expect(api.discover('rdsproxies')).rejects.toBeInstanceOf(ApiFailure);
		await expect(api.discover('rdsproxies')).rejects.toThrow('응답하지 않았습니다');
	});

	it('turns an unreachable server into words instead of "Failed to fetch"', async () => {
		vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));

		await expect(api.discover('webacls')).rejects.toThrow('서버에 연결하지 못했습니다.');
	});

	it('lets a deliberate cancel stay a plain AbortError', async () => {
		// PageView tells an abandoned request apart from a real failure by the
		// name on the exception. Wrapping it would make every range change look
		// like an error.
		vi.stubGlobal(
			'fetch',
			vi.fn().mockRejectedValue(new DOMException('The user aborted a request.', 'AbortError'))
		);

		const err = await api.page('overview', '1h', '5m').catch((e) => e);
		expect(err).toBeInstanceOf(DOMException);
		expect(err.name).toBe('AbortError');
	});

	it('carries the server explanation of a failed lookup through to the caller', async () => {
		respondWith(
			{
				error: 'Bad Gateway',
				detail: 'DescribeDBProxies: AccessDeniedException',
				hint: '권한 확인'
			},
			{ status: 502 }
		);

		const err = await api.discover('rdsproxies').catch((e) => e);
		expect(err).toBeInstanceOf(ApiFailure);
		expect(err.message).toContain('DescribeDBProxies');
		expect(err.hint).toBe('권한 확인');
	});
});

describe('discover', () => {
	it('passes the caveats a listing came back with', async () => {
		respondWith({
			kind: 'targetgroups',
			resources: [],
			truncated: true,
			elapsedMs: 812,
			partial: ['CLOUDFRONT 스코프 조회 실패: denied']
		});

		const res = await api.discover('targetgroups');
		expect(res.resources).toEqual([]);
		expect(res.truncated).toBe(true);
		expect(res.partial).toHaveLength(1);
	});
});

describe('page filters', () => {
	it('sends the selected namespace with the page request', async () => {
		respondWith({ window: {}, panels: [] });

		await api.page('pod-logs', '1h', '5m', undefined, 'payments');

		const fetchMock = vi.mocked(fetch);
		const requested = new URL(String(fetchMock.mock.calls[0]?.[0]), 'http://localhost');
		expect(requested.pathname).toBe('/api/page/pod-logs');
		expect(requested.searchParams.get('namespace')).toBe('payments');
	});
});

describe('config', () => {
	it('normalizes legacy null selections into arrays', async () => {
		respondWith({
			targetGroups: null,
			rdsProxies: null,
			webAcls: null,
			wafHeaders: null,
			logFormat: { okStatuses: null, excludePaths: null }
		});

		const config = await api.config();

		expect(config.targetGroups).toEqual([]);
		expect(config.rdsProxies).toEqual([]);
		expect(config.webAcls).toEqual([]);
		expect(config.wafHeaders).toEqual([]);
		expect(config.logFormat.okStatuses).toEqual([]);
		expect(config.logFormat.excludePaths).toEqual([]);
	});
});
