import type { Point, Unit } from './types';

/**
 * Formatting only.
 *
 * Nothing here converts between units or aggregates anything: the backend
 * already decided what a number means and tagged it with a unit. The reference
 * implementation multiplied load-balancer seconds by 1000 inside a template
 * while app latency arrived in milliseconds, which left two numbers on one
 * screen whose units could only be reconciled by reading the markup.
 */

const GAP = '—';

function decimals(n: number): number {
	const abs = Math.abs(n);
	if (abs === 0) return 0;
	if (abs < 1) return 2;
	if (abs < 10) return 1;
	return 0;
}

export function formatNumber(value: number, maximumFractionDigits = decimals(value)): string {
	return value.toLocaleString(undefined, { maximumFractionDigits });
}

/**
 * Formats with an exact number of decimals, trailing zeros included.
 *
 * Used wherever a column of numbers is read down: "1.0 KB" and "12.5 KB" line
 * up, "1 KB" and "12.5 KB" do not.
 */
function fixed(value: number, digits: number): string {
	return value.toLocaleString(undefined, {
		minimumFractionDigits: digits,
		maximumFractionDigits: digits
	});
}

export function formatBytes(bytes: number): string {
	if (bytes === 0) return '0 B';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	const i = Math.min(Math.floor(Math.log(Math.abs(bytes)) / Math.log(1024)), units.length - 1);
	const scaled = bytes / 1024 ** i;
	return i === 0 ? `${fixed(scaled, 0)} B` : `${fixed(scaled, 1)} ${units[i]}`;
}

/**
 * Latency keeps a decimal below 100ms.
 *
 * Rounding by magnitude alone would render a 12.5ms response as "13 ms", which
 * throws away most of the resolution exactly where p50 latencies live.
 */
export function formatDuration(ms: number): string {
	const abs = Math.abs(ms);
	if (abs >= 1000) return `${formatNumber(ms / 1000, 2)} s`;
	if (abs >= 100) return `${formatNumber(ms, 0)} ms`;
	return `${formatNumber(ms, 1)} ms`;
}

/** Renders a value with its unit. A null is a gap and reads as one. */
export function formatValue(value: Point, unit: Unit): string {
	if (value === null || value === undefined || Number.isNaN(value)) return GAP;

	switch (unit) {
		case 'ms':
			return formatDuration(value);
		case 's':
			// Seconds from CloudWatch are shown in milliseconds when small,
			// because a target response time of 0.012 s reads better as 12 ms.
			return formatDuration(value * 1000);
		case '%':
			// A fixed decimal so a column of utilisations lines up.
			return `${fixed(value, 1)}%`;
		case 'bytes':
			return formatBytes(value);
		case 'count':
			return formatNumber(value, 0);
		case 'conn':
			return formatNumber(value, 0);
		case '/s':
			return `${formatNumber(value)}/s`;
		default:
			return formatNumber(value);
	}
}

/** The plain value, for the clipboard: no unit suffix, no thousands separators. */
export function copyableValue(value: Point, unit: Unit): string {
	if (value === null || value === undefined || Number.isNaN(value)) return '';
	if (unit === 's') return String(value);
	return String(value);
}

const timeFormat = new Intl.DateTimeFormat(undefined, {
	hour: '2-digit',
	minute: '2-digit',
	hour12: false
});

const dateTimeFormat = new Intl.DateTimeFormat(undefined, {
	month: '2-digit',
	day: '2-digit',
	hour: '2-digit',
	minute: '2-digit',
	second: '2-digit',
	hour12: false
});

/** Axis labels: unix seconds to a local clock time. */
export function formatAxisTime(unixSeconds: number): string {
	return timeFormat.format(new Date(unixSeconds * 1000));
}

export function formatTimestamp(unixSeconds: number): string {
	return dateTimeFormat.format(new Date(unixSeconds * 1000));
}

/**
 * CloudWatch Logs Insights returns timestamps as UTC strings with no zone
 * marker. Parsing them as local time would shift every log line by the
 * viewer's offset, so the Z is supplied explicitly.
 */
export function parseInsightsTimestamp(value: string): Date | null {
	if (!value) return null;
	const normalised = /Z$|[+-]\d{2}:?\d{2}$/.test(value)
		? value.replace(' ', 'T')
		: `${value.replace(' ', 'T')}Z`;
	const d = new Date(normalised);
	return Number.isNaN(d.getTime()) ? null : d;
}

export function formatLogTimestamp(value: string): string {
	const d = parseInsightsTimestamp(value);
	return d ? dateTimeFormat.format(d) : value;
}

/** Maps a backend colour name onto the CSS custom property that holds it. */
export function colorVar(name: string | undefined): string {
	if (!name) return 'var(--label-secondary)';
	const kebab = name.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase();
	return `var(--${kebab}, var(--label-secondary))`;
}

/**
 * Maps a backend dash name onto the segment array uPlot strokes with.
 *
 * The second channel a chart has. The backend spends colour on the subject —
 * which pod, which node — so the metric moves here, and a reader who cannot
 * separate two hues still separates a solid line from a dashed one.
 *
 * undefined, not [], for a solid line: uPlot treats an empty array as a dash
 * pattern of zero-length segments and draws nothing at all.
 */
export function dashPattern(name: string | undefined): number[] | undefined {
	switch (name) {
		case 'dashed':
			return [6, 4];
		case 'dotted':
			return [2, 3];
		default:
			return undefined;
	}
}

/**
 * The same pattern as a CSS background, for the legend and tooltip swatches.
 *
 * A swatch that shows only colour is a legend that lies the moment two series
 * share one: it has to carry both channels the line does.
 */
export function dashSwatch(color: string, name: string | undefined): string {
	const pattern = dashPattern(name);
	if (!pattern) return color;
	const [on, off] = pattern;
	return `repeating-linear-gradient(90deg, ${color} 0 ${on}px, transparent ${on}px ${on + off}px)`;
}

export function intentVar(intent: string | undefined): string {
	switch (intent) {
		case 'good':
			return 'var(--intent-good)';
		case 'warn':
			return 'var(--intent-warn)';
		case 'bad':
			return 'var(--intent-bad)';
		default:
			return 'var(--label-primary)';
	}
}
