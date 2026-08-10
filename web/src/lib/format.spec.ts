import { describe, expect, it } from 'vitest';
import {
	colorVar,
	copyableValue,
	formatBytes,
	formatDuration,
	formatValue,
	intentVar,
	parseInsightsTimestamp
} from './format';

describe('formatValue', () => {
	it('renders a gap as a dash rather than a zero', () => {
		// A null means CloudWatch had nothing for that bucket. Rendering it as
		// 0 would read as a measured collapse to zero.
		expect(formatValue(null, 'ms')).toBe('—');
		expect(formatValue(null, 'count')).toBe('—');
		expect(formatValue(NaN, '%')).toBe('—');
	});

	it('distinguishes zero from missing', () => {
		expect(formatValue(0, 'count')).toBe('0');
		expect(formatValue(0, '%')).toBe('0.0%');
	});

	it('converts seconds for display without changing the underlying unit', () => {
		// Load balancer response times arrive in seconds; a 12ms value reads
		// better as milliseconds. The conversion lives here, not in a template.
		expect(formatValue(0.0125, 's')).toBe('12.5 ms');
		expect(formatValue(1.5, 's')).toBe('1.5 s');
	});

	it('formats each unit', () => {
		expect(formatValue(182.4, 'ms')).toBe('182 ms');
		expect(formatValue(42.55, '%')).toBe('42.6%');
		expect(formatValue(1284, 'count')).toBe('1,284');
		expect(formatValue(21, 'conn')).toBe('21');
		expect(formatValue(2048, 'bytes')).toBe('2.0 KB');
	});
});

describe('copyableValue', () => {
	it('copies the plain number with no unit or separators', () => {
		// A pasted "1,284 건" is useless in a query; a pasted 1284 is not.
		expect(copyableValue(1284, 'count')).toBe('1284');
		expect(copyableValue(182.4, 'ms')).toBe('182.4');
		expect(copyableValue(0.0125, 's')).toBe('0.0125');
	});

	it('copies nothing for a gap', () => {
		expect(copyableValue(null, 'ms')).toBe('');
	});
});

describe('formatDuration', () => {
	it('switches to seconds above a thousand milliseconds', () => {
		expect(formatDuration(999)).toBe('999 ms');
		expect(formatDuration(1500)).toBe('1.5 s');
	});
});

describe('formatBytes', () => {
	it('scales through the units', () => {
		expect(formatBytes(0)).toBe('0 B');
		expect(formatBytes(512)).toBe('512 B');
		expect(formatBytes(1024)).toBe('1.0 KB');
		expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB');
	});
});

describe('parseInsightsTimestamp', () => {
	it('reads a zoneless Insights timestamp as UTC', () => {
		// Logs Insights returns UTC with no zone marker. Parsing it as local
		// time would shift every log line by the viewer's offset.
		const d = parseInsightsTimestamp('2026-08-10 07:12:04.000');
		expect(d?.toISOString()).toBe('2026-08-10T07:12:04.000Z');
	});

	it('leaves an explicit zone alone', () => {
		expect(parseInsightsTimestamp('2026-08-10T07:12:04Z')?.toISOString()).toBe(
			'2026-08-10T07:12:04.000Z'
		);
	});

	it('returns null for junk', () => {
		expect(parseInsightsTimestamp('not a time')).toBeNull();
		expect(parseInsightsTimestamp('')).toBeNull();
	});
});

describe('colorVar', () => {
	it('maps a backend colour name onto its custom property', () => {
		expect(colorVar('systemBlue')).toBe('var(--system-blue, var(--label-secondary))');
		expect(colorVar('systemGray')).toBe('var(--system-gray, var(--label-secondary))');
	});

	it('falls back when no colour was supplied', () => {
		expect(colorVar(undefined)).toBe('var(--label-secondary)');
	});
});

describe('intentVar', () => {
	it('maps every intent the backend emits', () => {
		expect(intentVar('good')).toBe('var(--intent-good)');
		expect(intentVar('warn')).toBe('var(--intent-warn)');
		expect(intentVar('bad')).toBe('var(--intent-bad)');
		expect(intentVar('neutral')).toBe('var(--label-primary)');
		expect(intentVar(undefined)).toBe('var(--label-primary)');
	});
});
