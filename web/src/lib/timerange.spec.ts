import { beforeEach, describe, expect, it } from 'vitest';
import { MAX_RANGE_SECONDS, timeRange } from './timerange.svelte';
import type { Meta } from './types';

const meta: Meta = {
	maxRangeSeconds: 14400,
	defaultRange: '1h',
	ranges: [
		{ range: '15m', seconds: 900, periods: ['1m'], defaultPeriod: '1m' },
		{ range: '30m', seconds: 1800, periods: ['1m', '5m'], defaultPeriod: '1m' },
		{ range: '1h', seconds: 3600, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
		{ range: '2h', seconds: 7200, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
		{ range: '4h', seconds: 14400, periods: ['1m', '5m', '10m', '1h'], defaultPeriod: '5m' }
	],
	limits: {
		logRows: 300,
		topN: 20,
		insightsConcurrency: 6,
		queryTimeoutSeconds: 45,
		cacheTtlSeconds: 30
	}
};

describe('timeRange', () => {
	beforeEach(() => {
		timeRange.applyMeta(meta);
		timeRange.setRange('1h');
		timeRange.setPeriod('1m');
		timeRange.refreshSeconds = 0;
	});

	it('caps the offered ranges at four hours', () => {
		expect(MAX_RANGE_SECONDS).toBe(14400);
		for (const r of timeRange.ranges) {
			expect(r.seconds).toBeLessThanOrEqual(MAX_RANGE_SECONDS);
		}
	});

	it('refuses a range the server would reject, even if one is advertised', () => {
		timeRange.applyMeta({
			...meta,
			ranges: [
				...meta.ranges,
				{ range: '24h', seconds: 86400, periods: ['1h'], defaultPeriod: '1h' }
			]
		});
		expect(timeRange.ranges.some((r) => r.range === '24h')).toBe(false);
	});

	it('keeps the period legal when the range changes', () => {
		// 10m over 15m would be three buckets, which the server rejects. The
		// picker must not be able to send it.
		timeRange.setRange('1h');
		timeRange.setPeriod('10m');
		expect(timeRange.period).toBe('10m');

		timeRange.setRange('15m');
		expect(timeRange.periods).toEqual(['1m']);
		expect(timeRange.period).toBe('1m');
		expect(timeRange.isValid()).toBe(true);
	});

	it('ignores an invalid period rather than sending it', () => {
		timeRange.setRange('15m');
		timeRange.setPeriod('10m');
		expect(timeRange.period).toBe('1m');
	});

	it('adopts the default period for a range that lacks the current one', () => {
		timeRange.setRange('4h');
		timeRange.setPeriod('1h');
		expect(timeRange.period).toBe('1h');

		timeRange.setRange('2h');
		// 1h over 2h is two buckets; the picker falls back to the default.
		expect(timeRange.period).toBe('1m');
	});

	it('validates combinations the way the server does', () => {
		expect(timeRange.isValid('4h', '1h')).toBe(true);
		expect(timeRange.isValid('15m', '10m')).toBe(false);
		expect(timeRange.isValid('1h', '1h')).toBe(false);
		expect(timeRange.isValid('8h', '5m')).toBe(false);
	});

	it('round-trips through URL parameters', () => {
		timeRange.setRange('4h');
		timeRange.setPeriod('10m');
		timeRange.refreshSeconds = 60;

		const params = timeRange.toSearchParams();
		expect(params.get('range')).toBe('4h');
		expect(params.get('period')).toBe('10m');
		expect(params.get('refresh')).toBe('60');

		timeRange.setRange('1h');
		timeRange.setPeriod('1m');
		timeRange.refreshSeconds = 0;

		timeRange.fromSearchParams(params);
		expect(timeRange.range).toBe('4h');
		expect(timeRange.period).toBe('10m');
		expect(timeRange.refreshSeconds).toBe(60);
	});

	it('keeps a manual choice through the URL rather than snapping back to the default', () => {
		timeRange.refreshSeconds = 0;
		const params = timeRange.toSearchParams();
		expect(params.get('refresh')).toBe('0');

		timeRange.refreshSeconds = 300;
		timeRange.fromSearchParams(params);
		expect(timeRange.refreshSeconds).toBe(0);
	});

	it('falls back to the default when the URL says nothing', () => {
		timeRange.refreshSeconds = 300;
		timeRange.fromSearchParams(new URLSearchParams('range=1h&period=1m'));
		expect(timeRange.refreshSeconds).toBe(60);
	});

	it('ignores nonsense in the URL instead of adopting it', () => {
		timeRange.fromSearchParams(new URLSearchParams('range=99h&period=3s&refresh=7'));
		expect(timeRange.range).toBe('1h');
		expect(timeRange.periods).toContain(timeRange.period);
		expect(timeRange.refreshSeconds).toBe(60);
	});

	it('bumps a nonce so a manual refresh refetches the same window', () => {
		const before = timeRange.nonce;
		timeRange.refresh();
		expect(timeRange.nonce).toBe(before + 1);
	});
});
