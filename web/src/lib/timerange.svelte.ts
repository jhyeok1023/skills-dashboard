import type { Meta } from './types';

/**
 * The shared time selection.
 *
 * Four hours is the product's ceiling and it is enforced twice: here, so the UI
 * cannot offer a combination the server would refuse, and again on the server,
 * because a URL can be typed by hand.
 */

export const MAX_RANGE_SECONDS = 4 * 60 * 60;

/** Fallback used before /api/meta has answered. Mirrors the Go defaults. */
const FALLBACK: Meta['ranges'] = [
	{ range: '15m', seconds: 900, periods: ['1m'], defaultPeriod: '1m' },
	{ range: '30m', seconds: 1800, periods: ['1m', '5m'], defaultPeriod: '1m' },
	{ range: '1h', seconds: 3600, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
	{ range: '2h', seconds: 7200, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
	{ range: '4h', seconds: 14400, periods: ['1m', '5m', '10m', '1h'], defaultPeriod: '5m' }
];

/** Auto-refresh choices. Off is the default: every refresh scans logs, and
 *  Logs Insights bills by the byte. */
export const REFRESH_CHOICES = [
	{ label: '수동', seconds: 0 },
	{ label: '30초', seconds: 30 },
	{ label: '1분', seconds: 60 },
	{ label: '5분', seconds: 300 }
];

class TimeRange {
	ranges = $state<Meta['ranges']>(FALLBACK);
	range = $state('1h');
	period = $state('1m');
	refreshSeconds = $state(0);

	/** Bumped whenever a manual refresh is requested. */
	nonce = $state(0);

	/** Periods valid for the current range. */
	get periods(): string[] {
		return this.ranges.find((r) => r.range === this.range)?.periods ?? ['1m'];
	}

	get maxRangeSeconds(): number {
		return MAX_RANGE_SECONDS;
	}

	/** True when range and period form a combination the server accepts. */
	isValid(range = this.range, period = this.period): boolean {
		const entry = this.ranges.find((r) => r.range === range);
		if (!entry) return false;
		if (entry.seconds > MAX_RANGE_SECONDS) return false;
		return entry.periods.includes(period);
	}

	applyMeta(meta: Meta) {
		// Never adopt a range beyond the cap, even if a future server offers one.
		this.ranges = meta.ranges.filter((r) => r.seconds <= MAX_RANGE_SECONDS);
		if (!this.ranges.some((r) => r.range === this.range)) {
			this.range = meta.defaultRange;
		}
		this.reconcilePeriod();
	}

	setRange(range: string) {
		if (!this.ranges.some((r) => r.range === range)) return;
		this.range = range;
		this.reconcilePeriod();
	}

	setPeriod(period: string) {
		if (!this.periods.includes(period)) return;
		this.period = period;
	}

	/** Keeps the period legal after a range change rather than sending a
	 *  combination the server would reject. */
	reconcilePeriod() {
		const valid = this.periods;
		if (valid.includes(this.period)) return;
		const entry = this.ranges.find((r) => r.range === this.range);
		this.period = entry?.defaultPeriod ?? valid[0] ?? '1m';
	}

	refresh() {
		this.nonce += 1;
	}

	/** The selection as URL parameters, so a view can be shared or reloaded. */
	toSearchParams(existing?: URLSearchParams): URLSearchParams {
		// A plain URLSearchParams on purpose: this is a value returned to the
		// caller and read once, not state anything subscribes to.
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const params = new URLSearchParams(existing ?? undefined);
		params.set('range', this.range);
		params.set('period', this.period);
		if (this.refreshSeconds > 0) params.set('refresh', String(this.refreshSeconds));
		else params.delete('refresh');
		return params;
	}

	/** Restores a selection from the URL, ignoring anything invalid. */
	fromSearchParams(params: URLSearchParams) {
		const range = params.get('range');
		if (range && this.ranges.some((r) => r.range === range)) {
			this.range = range;
		}
		const period = params.get('period');
		if (period && this.periods.includes(period)) {
			this.period = period;
		} else {
			this.reconcilePeriod();
		}
		const refresh = Number(params.get('refresh') ?? 0);
		this.refreshSeconds = REFRESH_CHOICES.some((c) => c.seconds === refresh) ? refresh : 0;
	}
}

export const timeRange = new TimeRange();
