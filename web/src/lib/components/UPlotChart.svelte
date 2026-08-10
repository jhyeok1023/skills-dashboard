<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import { onDestroy } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import type { Point, Series } from '$lib/types';
	import { colorVar, formatAxisTime, formatValue } from '$lib/format';

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
	let chart: uPlot | null = null;
	let observer: ResizeObserver | null = null;
	let themeObserver: MutationObserver | null = null;
	// Which series the legend has switched off. Nothing is ever dropped for the
	// user; this only reflects what they chose to hide.
	const hidden = new SvelteSet<number>();

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
	}

	function destroy() {
		chart?.destroy();
		chart = null;
	}

	function toggle(i: number) {
		if (hidden.has(i)) hidden.delete(i);
		else hidden.add(i);
		chart?.setSeries(i + 1, { show: !hidden.has(i) });
	}

	$effect(() => {
		// Re-read so the effect tracks both inputs.
		void timestamps;
		void series;
		void height;

		if (!host) return;
		create();

		observer = new ResizeObserver((entries) => {
			const w = entries[0]?.contentRect.width;
			if (chart && w && w > 0) chart.setSize({ width: w, height });
		});
		observer.observe(host);

		// The palette changes when the theme does, and uPlot holds resolved
		// colours, so the chart is rebuilt on a theme switch.
		themeObserver = new MutationObserver(() => create());
		themeObserver.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ['data-theme']
		});

		return () => {
			observer?.disconnect();
			observer = null;
			themeObserver?.disconnect();
			themeObserver = null;
			destroy();
		};
	});

	onDestroy(destroy);
</script>

<div class="chart" bind:this={host} data-chart style:--chart-height="{height}px"></div>

{#if series.length}
	<ul class="legend">
		{#each series as s, i (s.label + i)}
			<li>
				<button
					type="button"
					onclick={() => toggle(i)}
					aria-pressed={!hidden.has(i)}
					class:off={hidden.has(i)}
					title="{s.label} 표시 전환"
				>
					<span class="swatch" style:background={colorVar(s.color)} aria-hidden="true"></span>
					<span data-value>{s.label}</span>
				</button>
			</li>
		{/each}
	</ul>
{:else}
	<p class="muted tiny empty">이 구간에 데이터가 없습니다.</p>
{/if}

<style>
	.chart {
		width: 100%;
		min-height: var(--chart-height);
		/* uPlot draws into a canvas sized by the observer; the container itself
		   must never push the page sideways. */
		overflow: hidden;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 10px;
		list-style: none;
		margin: 10px 0 0;
		padding: 0;
	}

	.legend button {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		border: none;
		background: transparent;
		border-radius: 5px;
		padding: 1px 5px;
		font-size: 12px;
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
		width: 9px;
		height: 9px;
		border-radius: 2px;
		flex: none;
	}

	.empty {
		margin-top: 8px;
	}

	:global(.uplot .u-legend) {
		display: none;
	}
</style>
