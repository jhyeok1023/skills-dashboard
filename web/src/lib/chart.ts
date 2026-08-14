import type { Series } from './types';
import { colorVar, dashSwatch, formatValue } from './format';
import { wafAction } from './wafAction';

/**
 * The readout behind the chart's hover tooltip.
 *
 * Kept out of the component because it is the only part with a decision in it —
 * which series are shown, and what a missing sample reads as — and because the
 * component's own version of it runs inside a mousemove handler, where a test
 * cannot reach.
 *
 * A null sample is a gap, and `formatValue` renders it as a dash. It must never
 * be shown as a zero: the whole point of `spanGaps: false` on the line is that
 * "not measured" and "measured zero" are different facts.
 */

export interface TooltipRow {
	/** Index into the original series array, so the caller can key on it. */
	index: number;
	label: string;
	value: string;
	color: string;
	/** The swatch's background: the colour, broken up when the line is dashed. */
	swatch: string;
	/** A WAF action glyph, or '' when the series is not a WAF action. */
	icon: string;
	/** The raw sample the row reports, null for a gap. Ranks the readout. */
	point: number | null;
}

export interface Readout {
	rows: TooltipRow[];
	/** Visible series left out because the readout was capped. */
	omitted: number;
}

/**
 * MAX_TOOLTIP_ROWS is what stops the readout from swallowing the chart.
 *
 * The pod resource panel draws one line per pod per metric, so twenty pods is
 * forty rows over a 220px chart — the readout covered the thing it was
 * describing. The cap is on the readout only: every series is still drawn, and
 * the legend still lists all of them.
 */
export const MAX_TOOLTIP_ROWS = 10;

interface SeriesStyle {
	color: string;
	swatch: string;
	icon: string;
}

/**
 * Colour, swatch and icon depend on the series alone, never on the cursor,
 * but this readout is rebuilt every time the cursor crosses a sample. Cached
 * per series array — a refresh delivers a new array and naturally starts a
 * new entry — so a 200-series panel pays the regexes once per payload instead
 * of once per hover step.
 */
const styleCache = new WeakMap<Series[], SeriesStyle[]>();

function seriesStyles(series: Series[]): SeriesStyle[] {
	let styles = styleCache.get(series);
	if (!styles) {
		styles = series.map((s) => {
			const color = colorVar(s.color);
			return { color, swatch: dashSwatch(color, s.dash), icon: wafAction(s.label)?.icon ?? '' };
		});
		styleCache.set(series, styles);
	}
	return styles;
}

export function tooltipRows(
	series: Series[],
	hidden: ReadonlySet<number>,
	idx: number,
	limit = MAX_TOOLTIP_ROWS
): Readout {
	const styles = seriesStyles(series);
	const rows: TooltipRow[] = [];
	for (let i = 0; i < series.length; i++) {
		if (hidden.has(i)) continue;
		const s = series[i];
		// `?? null` and not `||`: a measured zero is a value, not a gap.
		const point = s.values[idx] ?? null;
		rows.push({
			index: i,
			label: s.label,
			value: formatValue(point, s.unit),
			color: styles[i].color,
			swatch: styles[i].swatch,
			icon: styles[i].icon,
			point
		});
	}

	// Sorted by the value under the cursor, not by the order the series happen
	// to arrive in. What the cap keeps has to be what the operator came to
	// read — the busiest pod at that instant — and truncating an arbitrary
	// order would keep whichever series the backend listed first instead.
	// A gap sorts last: "not measured" is not a small number.
	rows.sort((a, b) => (b.point ?? -Infinity) - (a.point ?? -Infinity));

	if (limit <= 0 || rows.length <= limit) return { rows, omitted: 0 };
	return { rows: rows.slice(0, limit), omitted: rows.length - limit };
}
