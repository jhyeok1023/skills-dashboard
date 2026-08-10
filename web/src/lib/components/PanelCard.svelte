<script lang="ts">
	import type { Panel, WindowJSON } from '$lib/types';
	import UPlotChart from './UPlotChart.svelte';
	import StatTile from './StatTile.svelte';
	import DataTable from './DataTable.svelte';
	import BarBreakdown from './BarBreakdown.svelte';

	/**
	 * One panel, rendered the same way wherever it appears.
	 *
	 * There is no compact variant that shows fewer rows or fewer series. The
	 * reference implementation had one — eight bars in the grid, thirty in the
	 * expanded dialog — and because the bar scale was computed from whatever
	 * was shown, the same panel read differently in the two places.
	 */

	interface Props {
		panel: Panel;
		window: WindowJSON;
		chartHeight?: number;
	}

	let { panel, window: win, chartHeight = 220 }: Props = $props();

	/**
	 * Groups the table's rows into breakdowns using the columns the backend
	 * named. The rows are not re-filtered or re-summed here — this only decides
	 * which rows belong to which chart.
	 */
	const breakdowns = $derived.by(() => {
		const bars = panel.bars;
		const rows = panel.table?.rows;
		if (!bars || !rows?.length) return [];

		const out: { title: string; items: { key: string; value: number }[] }[] = [];
		for (const row of rows) {
			const group = bars.groupColumn ? String(row[bars.groupColumn] ?? '') : '';
			const key = String(row[bars.keyColumn] ?? '');
			const value = Number(row[bars.valueColumn] ?? 0);
			if (!key || !Number.isFinite(value)) continue;

			let bucket = out.find((b) => b.title === group);
			if (!bucket) {
				bucket = { title: group, items: [] };
				out.push(bucket);
			}
			bucket.items.push({ key, value });
		}
		return out;
	});
</script>

<!-- A panel carrying a table takes the full grid width. Squeezed into half a
     row, a path column wraps to two characters per line — readable in the
     letter-of-the-law sense, useless in practice. -->
<section class="card panel" class:wide={!!panel.table} data-panel={panel.id}>
	<header class="row">
		<h2 data-value>{panel.title}</h2>
	</header>

	{#if panel.warnings?.length}
		<div class="warnings">
			{#each panel.warnings as warning (warning)}
				<p class="warning" data-value>{warning}</p>
			{/each}
		</div>
	{/if}

	{#if panel.stats?.length}
		<div class="stats">
			{#each panel.stats as stat (stat.key)}
				<StatTile {stat} />
			{/each}
		</div>
	{/if}

	{#if panel.series?.length}
		<UPlotChart timestamps={win.timestamps} series={panel.series} height={chartHeight} />
	{/if}

	{#if breakdowns.length}
		<div class="breakdowns">
			{#each breakdowns as b (b.title)}
				<BarBreakdown items={b.items} title={b.title || undefined} />
			{/each}
		</div>
	{/if}

	{#if panel.table}
		<DataTable table={panel.table} />
	{/if}
</section>

<style>
	.panel {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 0;
	}

	.panel.wide {
		grid-column: 1 / -1;
	}

	header h2 {
		margin-right: auto;
		overflow-wrap: anywhere;
	}

	.stats {
		display: grid;
		gap: 8px;
		/* Tiles grow to fit their content; a long basis wraps inside its tile
		   rather than being cut off or forcing the page wider. */
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 11rem), 1fr));
	}

	.warnings {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.breakdowns {
		display: grid;
		gap: 16px;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr));
	}
</style>
