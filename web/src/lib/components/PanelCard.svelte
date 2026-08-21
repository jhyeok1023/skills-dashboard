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
	 *
	 * The expand button below reopens that door, so it is deliberately built so
	 * the door cannot swing: the card and the dialog render one `body` snippet,
	 * and the only argument it takes is how tall to draw the chart. There is no
	 * second code path in which a row count or a bar scale could diverge.
	 */

	interface Props {
		panel: Panel;
		window: WindowJSON;
		chartHeight?: number;
	}

	let { panel, window: win, chartHeight = 220 }: Props = $props();

	let dialog = $state<HTMLDialogElement | null>(null);

	/**
	 * `showModal()` supplies ESC-to-close, the backdrop, the focus trap and the
	 * inert page behind — all of it, for free, from the platform. A hand-rolled
	 * overlay would be a keydown listener, a focus trap and a z-index war with
	 * the sticky header, to arrive at the same behaviour.
	 *
	 * The flag beside it exists because a closed <dialog> still renders its
	 * children. Left unguarded, every panel's stats, chart and table would be
	 * built twice on every load — a log table is 300 rows, so that is 300 rows
	 * of DOM nobody can see, kept in step with every refresh. The `close` event
	 * fires for ESC and for the button alike, so there is one way back out.
	 */
	let open = $state(false);

	function expand() {
		open = true;
		dialog?.showModal();
	}

	/**
	 * Whether this panel is holding something wrong.
	 *
	 * The judgement is the backend's — it already tags each stat with an intent
	 * and attaches warnings — so it is read here rather than recomputed from a
	 * threshold the UI would have to keep in step with the server's.
	 */
	const alarm = $derived(
		panel.stats?.some((s) => s.intent === 'bad') || (panel.warnings?.length ?? 0) > 0
	);
	const severity = $derived(
		panel.stats?.some((s) => s.intent === 'bad')
			? 'bad'
			: panel.stats?.some((s) => s.intent === 'warn')
				? 'warn'
				: 'none'
	);

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
{#snippet body(height: number)}
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
		<UPlotChart timestamps={win.timestamps} series={panel.series} {height} />
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
{/snippet}

<section
	class="card panel"
	class:wide={!!panel.table}
	class:alarm
	data-panel={panel.id}
	data-severity={severity}
>
	<header class="row">
		<h2 data-value>{panel.title}</h2>
		<button
			type="button"
			class="control expand"
			aria-label="{panel.title} 크게 보기"
			onclick={expand}
		>
			<span aria-hidden="true">⤢</span>
		</button>
	</header>

	{@render body(chartHeight)}
</section>

<!--
	The panel again, larger, from the same snippet.

	It keeps refreshing while open: the poll lives on the page, this component
	just receives a newer `panel`, and the dialog re-renders with it. Nothing
	here pauses anything — a modal that froze the data would be a modal you
	could read a stale number off without being told it was stale.

	ponytail: opening and closing rebuilds the snippet, so that panel's table
	filter, sort and "더 보기" position reset, as does the uPlot instance.
	Keeping them would mean hoisting that state out of DataTable and
	UPlotChart, which is more machinery than an expand button is worth.
-->
<dialog
	bind:this={dialog}
	class="expanded"
	aria-label={panel.title}
	onclose={() => (open = false)}
	onclick={(e) => {
		// The dialog element itself is the backdrop's hit target; its content
		// sits in the child below. ESC is the platform's own, already wired.
		if (e.target === dialog) dialog?.close();
	}}
>
	{#if open}
		<div class="expanded-body" data-panel-expanded={panel.id}>
			<header class="row">
				<h2 data-value>{panel.title}</h2>
				<button type="button" class="control" onclick={() => dialog?.close()}>닫기</button>
			</header>

			{@render body(Math.round(Math.min(520, globalThis.innerHeight * 0.5)))}
		</div>
	{/if}
</dialog>

<style>
	.panel {
		display: flex;
		flex-direction: column;
		gap: 7px;
		min-width: 0;
		position: relative;
		z-index: var(--z-panel);
	}

	/*
	 * Normal traffic is the background layer; a panel with an active alarm is
	 * lifted out of it. The shadow and the z-index are the whole mechanism —
	 * both are static values, so nothing animates as data arrives.
	 */
	.panel.alarm {
		z-index: var(--z-panel-alarm);
		box-shadow: var(--shadow-card);
		border-color: color-mix(in oklch, var(--alarm-color) 45%, var(--separator));
		border-left: 3px solid var(--alarm-color);
	}

	.panel[data-severity='bad'] {
		--alarm-color: var(--intent-bad);
	}

	.panel[data-severity='warn'] {
		--alarm-color: var(--intent-warn);
	}

	/* A panel whose only flag is a warning string, with no bad stat. */
	.panel[data-severity='none'] {
		--alarm-color: var(--intent-warn);
	}

	.panel.wide {
		grid-column: 1 / -1;
	}

	header h2 {
		margin-right: auto;
		overflow-wrap: anywhere;
		font-size: 14px;
	}

	.expand {
		flex: none;
		padding: 2px 7px;
		line-height: 1.2;
		color: var(--label-secondary);
	}

	.expanded {
		/* The dialog is the backdrop's hit target, so it fills the viewport and
		   the readable part is the child inside it. */
		width: 100%;
		max-width: none;
		height: 100%;
		max-height: none;
		margin: 0;
		padding: 24px;
		border: none;
		background: transparent;
		z-index: var(--z-modal);
		overflow: auto;
		overscroll-behavior: contain;
	}

	.expanded::backdrop {
		background: color-mix(in oklch, black 45%, transparent);
	}

	.expanded-body {
		display: flex;
		flex-direction: column;
		gap: 7px;
		width: 100%;
		max-width: 1400px;
		margin: 0 auto;
		padding: 14px;
		border-radius: var(--radius-card);
		background: var(--bg-elevated);
		box-shadow: var(--shadow-card);
	}

	.stats {
		display: grid;
		gap: 6px;
		/* Tiles grow to fit their content; a long basis wraps inside its tile
		   rather than being cut off or forcing the page wider. */
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr));
	}

	.warnings {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.breakdowns {
		display: grid;
		gap: 10px;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr));
	}
</style>
