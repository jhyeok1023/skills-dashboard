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

describe('page', () => {
	it('reports the bound it actually waited, not the discovery one', async () => {
		// A page waits out the server's 90s budget; a discovery gives up at 35.
		// Naming the wrong one sends the operator looking for the wrong problem.
		vi.stubGlobal(
			'fetch',
			vi.fn().mockRejectedValue(new DOMException('The operation timed out.', 'TimeoutError'))
		);

		await expect(api.page('waf', '1h', '1m')).rejects.toThrow('95초');
	});

	it('keeps the caller able to cancel after the timeout is folded in', async () => {
		// PageView aborts the in-flight request on every range change. Combining
		// its signal with the timeout must not drop it, or an abandoned request
		// runs to completion and races the one that replaced it.
		let seen: AbortSignal | undefined;
		vi.stubGlobal(
			'fetch',
			vi.fn((_: string, init: RequestInit) => {
				seen = init.signal ?? undefined;
				return new Promise(() => {});
			})
		);

		const controller = new AbortController();
		void api.page('waf', '1h', '1m', controller.signal);
		expect(seen).toBeDefined();
		expect(seen!.aborted).toBe(false);

		controller.abort();
		expect(seen!.aborted).toBe(true);
	});
});

describe('discover', () => {
	it('passes the caveats a listing came back with', async () => {
		respondWith({
			kind: 'targetgroups',
			resources: [],
			truncated: true,
			partial: ['CLOUDFRONT 스코프 조회 실패: denied']
		});

		const res = await api.discover('targetgroups');
		expect(res.resources).toEqual([]);
		expect(res.truncated).toBe(true);
		expect(res.partial).toHaveLength(1);
	});
});
