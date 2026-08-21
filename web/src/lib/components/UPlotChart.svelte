<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import { onDestroy, untrack } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import type { Point, Series } from '$lib/types';
	import {
		colorVar,
		dashPattern,
		dashSwatch,
		formatAxisTime,
		formatTimestamp,
		formatValue
	} from '$lib/format';
	import { tooltipRows } from '$lib/chart';
	import { wafAction } from '$lib/wafAction';

	/**
	 * Every time series on the dashboard is drawn here.
	 *
	 * Two things justify uPlot over a declarative chart component. It stays
	 * responsive with a few hundred series, and `cursor.sync` ties every chart
	 * on a page to one crosshair — which is the whole point of putting logs,
	 * latency and resource use on a shared axis in the first place.
	 *
	 * No series is ever dropped. The reference implementation plotted only the
	 * top six and then recomputed its axes from that subset, so the axis
	 * described less data than the legend claimed. Here everything is drawn and
	 * the legend toggles what you want to look at.
	 */

	interface Props {
		timestamps: number[];
		series: Series[];
		height?: number;
		/** Charts sharing a key share a crosshair. */
		syncKey?: string;
	}

	let { timestamps, series, height = 220, syncKey = 'dashboard' }: Props = $props();

	let host = $state<HTMLDivElement | null>(null);
	let wrap: HTMLDivElement | null = null;
	let tip: HTMLDivElement | null = null;
	let chart: uPlot | null = null;
	let observer: ResizeObserver | null = null;
	let resizeTimer: ReturnType<typeof setTimeout> | undefined;
	let themeObserver: MutationObserver | null = null;
	// Which series the legend has switched off. Nothing is ever dropped for the
	// user; this only reflects what they chose to hide.
	const hidden = new SvelteSet<number>();

	/**
	 * Whether the pointer is over *this* chart.
	 *
	 * `cursor.sync` moves the crosshair on every chart on the page at once,
	 * which is wanted. Showing a tooltip on every one of them is not: six
	 * panels would each pop a readout for a pointer that is over one of them.
	 * The crosshairs stay synced; only the hovered chart explains itself.
	 */
	let over = false;

	/**
	 * Identifies the *shape* of the chart — how many series, what they are
	 * called, and what colour and line pattern they take. Data changing does not
	 * change this; a different set of pods or WAF actions does.
	 *
	 * The separators are escapes rather than the literal control characters
	 * they used to be. Written literally they made this a binary file as far
	 * as git and grep are concerned, so every change to it arrived as
	 * "Bin 6661 -> 14508 bytes" and could not be reviewed at all.
	 */
	const shapeKey = $derived(
		series
			.map((s) => `${s.label}\u0000${s.color ?? ''}\u0000${s.dash ?? ''}\u0000${s.unit}`)
			.join('\u0001')
	);
	let builtKey = '';
	let builtHeight = 0;

	/** uPlot needs concrete colours, so the CSS custom properties are resolved
	 *  against the live document and re-resolved when the theme changes. */
	function resolve(cssVar: string): string {
		const name = cssVar
			.replace(/^var\(|\)$/g, '')
			.split(',')[0]
			.trim();
		const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
		return value || '#888';
	}

	function axisColors() {
		return {
			label: resolve('var(--label-secondary)'),
			grid: resolve('var(--separator)')
		};
	}

	function toData(): uPlot.AlignedData {
		const xs = timestamps;
		const ys = series.map((s) => alignedValues(s.values, xs.length));
		return [xs, ...ys] as unknown as uPlot.AlignedData;
	}

	/** Guards against a series arriving out of step with the axis. The server
	 *  validates this too; here it means a mismatch shows as a short line
	 *  rather than as data drawn against the wrong times. */
	function alignedValues(values: Point[], n: number): (number | null)[] {
		if (values.length === n) return values;
		const out = new Array<number | null>(n).fill(null);
		for (let i = 0; i < Math.min(values.length, n); i++) out[i] = values[i];
		return out;
	}

	/* ---------------------------------------------------------------------
	 * Hover tooltip
	 *
	 * uPlot already does the hard parts: it snaps the cursor to the nearest
	 * sample (`cursor.idx`), draws the crosshair, and marks the point on each
	 * line. What was missing was somewhere to read the value. That gap is why
	 * hovering a chart used to do nothing at all — uPlot's own live readout is
	 * its legend, and the legend is switched off here so long pod names can
	 * wrap in HTML instead of being clipped by the canvas.
	 *
	 * The handler writes to the DOM directly rather than through `$state`.
	 * Going through Svelte would re-run the component on every mousemove; this
	 * way a pointer sweep costs a handful of textContent writes and one
	 * transform, with no framework work and no layout-affecting properties.
	 * ------------------------------------------------------------------- */

	/** Rebuilt only when the row count changes; otherwise text is overwritten. */
	function tipRowEls(n: number): HTMLElement[] {
		if (!tip) return [];
		const list = tip.querySelector('.tip-rows') as HTMLElement;
		while (list.childElementCount > n) list.lastElementChild!.remove();
		while (list.childElementCount < n) {
			const row = document.createElement('div');
			row.className = 'tip-row';
			// textContent everywhere below: labels are pod names, paths and WAF
			// actions straight out of a log. They are never treated as markup.
			for (const cls of ['sw', 'ic', 'lb', 'vl']) {
				const span = document.createElement('span');
				span.className = cls;
				row.appendChild(span);
			}
			list.appendChild(row);
		}
		return [...list.children] as HTMLElement[];
	}

	/** Cursor waiting to be drawn; only the newest one in a frame survives. */
	let pendingCursor: uPlot | null = null;
	let tipRaf = 0;
	/** Sample index the tip DOM currently shows; -1 when hidden or stale. */
	let renderedIdx = -1;
	let tipW = 0;
	let tipH = 0;

	function hideTip() {
		if (tip) tip.style.display = 'none';
		renderedIdx = -1;
	}

	function renderTip(u: uPlot) {
		// Coalesced to one frame: mousemove fires faster than frames are drawn,
		// and cursor.sync replays every move on every chart on the page. The
		// DOM work happens once per frame, on the latest position.
		pendingCursor = u;
		if (!tipRaf) tipRaf = requestAnimationFrame(flushTip);
	}

	function flushTip() {
		tipRaf = 0;
		const u = pendingCursor;
		pendingCursor = null;
		if (!u || !tip || !wrap) return;
		const idx = u.cursor.idx;
		const cl = u.cursor.left;
		const ct = u.cursor.top;
		if (!over || idx == null || cl == null || ct == null || cl < 0 || ct < 0) {
			hideTip();
			return;
		}

		// The text only changes when the cursor crosses to another sample;
		// between samples only the transform below moves. Skipping the rewrite
		// also skips the re-measure, so those frames read a clean layout and
		// force nothing.
		if (idx !== renderedIdx) {
			const { rows, omitted } = tooltipRows(series, hidden, idx);
			if (!rows.length) {
				hideTip();
				return;
			}

			const time = tip.querySelector('.tip-time') as HTMLElement;
			time.textContent = formatTimestamp(timestamps[idx]);

			const els = tipRowEls(rows.length);
			for (let i = 0; i < rows.length; i++) {
				const r = rows[i];
				const [sw, ic, lb, vl] = els[i].children as unknown as HTMLElement[];
				sw.style.background = r.swatch;
				ic.textContent = r.icon;
				lb.textContent = r.label;
				vl.textContent = r.value;
			}

			// The readout is capped, so it has to say so. Silently showing ten of
			// forty pods would read as "these are the pods", and the legend below
			// still lists every one of them.
			const more = tip.querySelector('.tip-more') as HTMLElement;
			more.textContent = omitted > 0 ? `외 ${omitted}개` : '';
			more.style.display = omitted > 0 ? 'block' : 'none';

			tip.style.display = 'block';
			renderedIdx = idx;
			// One forced layout per sample change, not per pointer event; the
			// size feeds the edge flip below until the content changes again.
			tipW = tip.offsetWidth;
			tipH = tip.offsetHeight;
		}

		// uPlot's bbox is in device pixels; the cursor is in CSS pixels
		// relative to the plotting area.
		const dpr = uPlot.pxRatio || window.devicePixelRatio || 1;
		const px = u.bbox.left / dpr + cl;
		const py = u.bbox.top / dpr + ct;
		const gap = 12;

		let x = px + gap;
		let y = py + gap;
		// Flip to the other side of the cursor at the edges so the readout is
		// never cut off by the panel.
		if (x + tipW > wrap.clientWidth) x = Math.max(0, px - tipW - gap);
		if (y + tipH > wrap.clientHeight) y = Math.max(0, py - tipH - gap);

		// transform only: no layout, no paint of the surrounding panel, and no
		// transition — the readout tracks the pointer instead of chasing it.
		tip.style.transform = `translate(${Math.round(x)}px, ${Math.round(y)}px)`;
	}

	function tooltipPlugin(): uPlot.Plugin {
		return { hooks: { setCursor: renderTip } };
	}

	function buildOptions(width: number): uPlot.Options {
		const colors = axisColors();
		return {
			width,
			height,
			// The legend is rendered below, not by uPlot, so a long pod name can
			// wrap instead of being cut off by the canvas.
			legend: { show: false },
			cursor: {
				sync: { key: syncKey, setSeries: true },
				points: { size: 6 }
			},
			plugins: [tooltipPlugin()],
			scales: { x: { time: true } },
			axes: [
				{
					stroke: colors.label,
					grid: { stroke: colors.grid, width: 1 },
					ticks: { stroke: colors.grid },
					values: (_u, splits) => splits.map((v) => formatAxisTime(v))
				},
				{
					stroke: colors.label,
					grid: { stroke: colors.grid, width: 1 },
					ticks: { stroke: colors.grid },
					size: 60
				}
			],
			series: [
				{ label: '시각' },
				...series.map((s, i) => ({
					label: s.label,
					stroke: resolve(colorVar(s.color)),
					dash: dashPattern(s.dash),
					width: 1.5,
					show: !hidden.has(i),
					// A gap stays a gap: uPlot breaks the line rather than
					// drawing through a value that was never measured.
					spanGaps: false,
					value: (_u: uPlot, v: number | null) => formatValue(v, s.unit)
				}))
			]
		};
	}

	function create() {
		if (!host) return;
		destroy();
		const width = host.clientWidth || 600;
		chart = new uPlot(buildOptions(width), toData(), host);
		builtKey = shapeKey;
		builtHeight = height;
	}

	function destroy() {
		chart?.destroy();
		chart = null;
	}

	function toggle(i: number) {
		if (hidden.has(i)) hidden.delete(i);
		else hidden.add(i);
		chart?.setSeries(i + 1, { show: !hidden.has(i) });
		// The row set changed under the same sample index.
		renderedIdx = -1;
	}

	// Mount, resize and theme. Deliberately does not depend on the data: a
	// refresh must not tear the canvas down and build a new one.
	$effect(() => {
		if (!host) return;
		untrack(() => create());

		// Listeners rather than markup handlers: the wrapper is presentational,
		// and the tooltip is a pointer affordance only — a keyboard user reads
		// the same numbers from the legend and the panel's table.
		const enter = () => (over = true);
		const leave = () => {
			over = false;
			hideTip();
		};
		wrap?.addEventListener('pointerenter', enter);
		wrap?.addEventListener('pointerleave', leave);

		// A window drag delivers a new width every frame, and setSize
		// reallocates the canvas and redraws the whole chart — per panel.
		// Trailing debounce: the chart re-fits once, when the drag settles,
		// and a size that ends up unchanged does not redraw at all.
		observer = new ResizeObserver((entries) => {
			const w = entries[0]?.contentRect.width;
			if (!w || w <= 0) return;
			clearTimeout(resizeTimer);
			resizeTimer = setTimeout(() => {
				if (chart && Math.round(w) !== Math.round(chart.width)) {
					chart.setSize({ width: w, height: builtHeight });
				}
			}, 100);
		});
		observer.observe(host);

		// The palette changes when the theme does, and uPlot holds resolved
		// colours, so the chart is rebuilt on a theme switch.
		themeObserver = new MutationObserver(() => untrack(() => create()));
		themeObserver.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ['data-theme']
		});

		return () => {
			wrap?.removeEventListener('pointerenter', enter);
			wrap?.removeEventListener('pointerleave', leave);
			cancelAnimationFrame(tipRaf);
			tipRaf = 0;
			clearTimeout(resizeTimer);
			observer?.disconnect();
			observer = null;
			themeObserver?.disconnect();
			themeObserver = null;
			destroy();
		};
	});

	// New numbers for the same set of series are pushed into the existing
	// canvas. Rebuilding instead — which is what this component used to do on
	// every refresh — costs a canvas teardown, a fresh DOM insert and one
	// forced style recalculation per series, for every panel on the page.
	$effect(() => {
		void timestamps;
		void series;
		const key = shapeKey;
		const h = height;

		untrack(() => {
			if (!chart) return;
			// The set of series, or the size, actually changed: uPlot's options
			// are fixed at construction, so this one does need a rebuild.
			if (key !== builtKey || h !== builtHeight) {
				create();
				return;
			}
			chart.setData(toData());
			// The values under the cursor changed; the next cursor event must
			// rewrite the tip even at an unchanged sample index.
			renderedIdx = -1;
		});
	});

	onDestroy(destroy);
</script>

<!-- The tooltip lives outside .chart, which clips its canvas; positioned
     against this wrapper it can overhang the plot area at the edges. -->
<div class="chart-wrap" bind:this={wrap}>
	<div class="chart" bind:this={host} data-chart style:--chart-height="{height}px"></div>

	<div class="tip" bind:this={tip} aria-hidden="true">
		<div class="tip-time"></div>
		<div class="tip-rows"></div>
		<div class="tip-more"></div>
	</div>
</div>

{#if series.length}
	<ul class="legend">
		{#each series as s, i (s.label + i)}
			{@const action = wafAction(s.label)}
			<li>
				<button
					type="button"
					onclick={() => toggle(i)}
					aria-pressed={!hidden.has(i)}
					class:off={hidden.has(i)}
					title="{s.label} 표시 전환"
				>
					<span
						class="swatch"
						style:background={dashSwatch(colorVar(s.color), s.dash)}
						aria-hidden="true"
					></span>
					{#if action}<span class="icon" aria-hidden="true">{action.icon}</span>{/if}
					<span data-value>{s.label}</span>
				</button>
			</li>
		{/each}
	</ul>
{:else}
	<p class="muted tiny empty">이 구간에 데이터가 없습니다.</p>
{/if}

<style>
	.chart-wrap {
		position: relative;
		width: 100%;
		min-width: 0;
	}

	.chart {
		width: 100%;
		min-height: var(--chart-height);
		/* uPlot draws into a canvas sized by the observer; the container itself
		   must never push the page sideways. */
		overflow: hidden;
	}

	/* --- hover readout ------------------------------------------------- */

	.tip {
		position: absolute;
		top: 0;
		left: 0;
		display: none;
		z-index: var(--z-tooltip);
		/* The pointer must reach the canvas underneath, or the readout would
		   chase itself out from under the cursor. */
		pointer-events: none;
		max-width: min(22rem, 90%);
		padding: 5px 7px;
		border: 1px solid var(--separator);
		border-radius: 6px;
		background: var(--bg-elevated);
		box-shadow: var(--shadow-card);
		font-size: 11.5px;
		line-height: 1.4;
		/* No transition: the readout tracks the pointer rather than easing
		   towards it. */
		transition: none;
		/* transform is written on every cursor move; promoting the layer keeps
		   that off the main paint path. */
		will-change: transform;
	}

	.tip-more {
		display: none;
		margin-top: 2px;
		color: var(--label-secondary);
	}

	.tip-time {
		font-variant-numeric: tabular-nums;
		font-weight: 600;
		color: var(--label-secondary);
		margin-bottom: 2px;
	}

	.tip :global(.tip-row) {
		display: grid;
		grid-template-columns: 14px 0.9em 1fr auto;
		align-items: baseline;
		gap: 5px;
	}

	/* A line, not a square: the swatch has to show the dash pattern as well as
	   the colour, because on a pod panel the pattern is what says which of the
	   two metrics the row is. See dashSwatch in format.ts. */
	.tip :global(.sw) {
		width: 14px;
		height: 3px;
		border-radius: 1px;
		align-self: center;
	}

	.tip :global(.ic) {
		font-size: 10px;
		color: var(--label-secondary);
		text-align: center;
	}

	/*
	 * One line per row.
	 *
	 * The row count is capped, but a pod name wrapping to three lines put the
	 * cap back over the chart it was meant to uncover — ten rows measured 370px
	 * against a 220px plot. This is the one place a label is deliberately cut:
	 * the readout exists to rank and compare values at an instant, and the full
	 * names are listed in full, unclipped and wrapping, in the legend directly
	 * below it. Elsewhere nothing is truncated.
	 */
	.tip :global(.lb) {
		color: var(--label-secondary);
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}

	.tip :global(.vl) {
		font-variant-numeric: tabular-nums;
		font-weight: 650;
		color: var(--label-primary);
		white-space: nowrap;
	}

	/* --- legend --------------------------------------------------------- */

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 2px 8px;
		list-style: none;
		margin: 6px 0 0;
		padding: 0;
	}

	.legend button {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		border: none;
		background: transparent;
		border-radius: 5px;
		padding: 1px 4px;
		font-size: 11.5px;
		color: var(--label-secondary);
		/* Long pod names wrap here rather than being clipped by the canvas. */
		white-space: normal;
		overflow-wrap: anywhere;
		text-align: left;
		max-width: 100%;
	}

	.legend button:hover {
		background: var(--fill-secondary);
	}

	.legend button.off {
		opacity: 0.45;
		text-decoration: line-through;
	}

	.swatch {
		width: 14px;
		height: 3px;
		border-radius: 1px;
		flex: none;
	}

	.icon {
		font-size: 10px;
		flex: none;
	}

	.empty {
		margin-top: 8px;
	}

	:global(.uplot .u-legend) {
		display: none;
	}
</style>
