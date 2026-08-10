import { describe, expect, it } from 'vitest';
import { MAX_TOOLTIP_ROWS, tooltipRows } from './chart';
import type { Series } from './types';

const series: Series[] = [
	{ label: 'ALLOW', unit: 'count', color: 'systemGreen', values: [10, null, 0] },
	{ label: 'BLOCK', unit: 'count', color: 'systemPink', values: [3, 4, 5] },
	{ label: 'p99', unit: 'ms', color: 'systemBlue', values: [12.5, 20, 30] }
];

describe('tooltipRows', () => {
	it('reads every visible series at one point in time, busiest first', () => {
		const { rows, omitted } = tooltipRows(series, new Set(), 0);
		expect(rows.map((r) => [r.label, r.value])).toEqual([
			['p99', '12.5 ms'],
			['ALLOW', '10'],
			['BLOCK', '3']
		]);
		expect(omitted).toBe(0);
	});

	it('renders a gap as a dash and a measured zero as zero', () => {
		// The line breaks at a null rather than drawing through it, so the
		// readout has to make the same distinction the chart does.
		const gap = tooltipRows(series, new Set(), 1).rows.find((r) => r.label === 'ALLOW');
		expect(gap?.value).toBe('—');
		const zero = tooltipRows(series, new Set(), 2).rows.find((r) => r.label === 'ALLOW');
		expect(zero?.value).toBe('0');
	});

	it('sorts a gap last, because "not measured" is not a small number', () => {
		const { rows } = tooltipRows(series, new Set(), 1);
		expect(rows.at(-1)?.label).toBe('ALLOW');
	});

	it('leaves out the series the legend switched off', () => {
		const { rows } = tooltipRows(series, new Set([1]), 0);
		expect(rows.map((r) => r.label)).toEqual(['p99', 'ALLOW']);
		// The index still refers to the original series, not the filtered list.
		expect(rows[0].index).toBe(2);
	});

	it('carries a WAF action glyph only for WAF actions', () => {
		const byLabel = Object.fromEntries(
			tooltipRows(series, new Set(), 0).rows.map((r) => [r.label, r.icon])
		);
		expect(byLabel).toEqual({ ALLOW: '✓', BLOCK: '✕', p99: '' });
	});

	it('reads past the end of a series as a gap rather than throwing', () => {
		expect(tooltipRows(series, new Set(), 99).rows.map((r) => r.value)).toEqual(['—', '—', '—']);
	});

	// The pod resource panel draws one line per pod per metric, so twenty pods
	// is forty rows over a 220px chart — the readout covered the thing it was
	// describing.
	it('caps the readout and keeps the largest values', () => {
		const many: Series[] = Array.from({ length: 40 }, (_, i) => ({
			label: `pod-${i}`,
			unit: 'percent' as never,
			color: 'systemBlue',
			values: [i]
		}));

		const { rows, omitted } = tooltipRows(many, new Set(), 0);
		expect(rows).toHaveLength(MAX_TOOLTIP_ROWS);
		expect(omitted).toBe(40 - MAX_TOOLTIP_ROWS);
		// Truncating the arrival order would have kept pod-0 through pod-9,
		// which is whichever the backend listed first rather than what is worth
		// reading at the cursor.
		expect(rows[0].label).toBe('pod-39');
		expect(rows.at(-1)?.label).toBe('pod-30');
	});

	it('counts the omission against what is visible, not what exists', () => {
		const many: Series[] = Array.from({ length: 15 }, (_, i) => ({
			label: `pod-${i}`,
			unit: 'percent' as never,
			color: 'systemBlue',
			values: [i]
		}));

		// Twelve hidden by the legend leaves three, which is under the cap.
		const hidden = new Set(Array.from({ length: 12 }, (_, i) => i));
		const { rows, omitted } = tooltipRows(many, hidden, 0);
		expect(rows).toHaveLength(3);
		expect(omitted).toBe(0);
	});
});
