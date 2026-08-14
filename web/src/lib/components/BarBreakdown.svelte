<script lang="ts">
	import { BarChart } from 'layerchart';
	import { formatNumber } from '$lib/format';

	/**
	 * A simple categorical breakdown — WAF traffic by method, responses by
	 * status code. layerchart handles these declaratively; the time series get
	 * uPlot, which earns its complexity there and not here.
	 */

	interface Item {
		key: string;
		value: number;
	}

	interface Props {
		items: Item[];
		title?: string;
		color?: string;
		height?: number;
	}

	let { items, title, color = 'var(--system-blue)', height = 200 }: Props = $props();

	// layerchart's tooltip fade is JS-driven (svelte/transition), so the global
	// reduced-motion CSS override in app.css never reaches it — the duration
	// has to be zeroed here. Read once: the OS preference does not change
	// mid-session in any way worth a listener.
	const fadeDuration = matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 100;

	const data = $derived(items.map((d) => ({ key: d.key, value: d.value })));
	const total = $derived(items.reduce((sum, d) => sum + d.value, 0));
</script>

<div class="breakdown">
	{#if title}
		<div class="row head">
			<h3 data-value>{title}</h3>
			<span class="tiny muted" data-value>합계 {formatNumber(total, 0)}</span>
		</div>
	{/if}

	{#if data.length === 0}
		<p class="muted tiny">이 구간에 데이터가 없습니다.</p>
	{:else}
		<div class="chart" style:height="{height}px">
			<!--
				Only the value axis is drawn. Category labels here would be a
				request path or a user-agent string; the chart has nowhere to
				put one at full length, and shortening it would be exactly the
				truncation this dashboard avoids. The list below carries every
				label in full, in the same order, with a copy button — so the
				chart shows shape and the list carries the facts.
			-->
			<BarChart
				{data}
				x="value"
				y="key"
				axis="x"
				orientation="horizontal"
				props={{ bars: { fill: color }, tooltip: { root: { fadeDuration } } }}
			/>
		</div>
		<!-- The chart shows shape; the list carries the exact figures, because a
		     bar cannot be read to three significant figures and cannot be copied. -->
		<ol class="values">
			{#each items as item (item.key)}
				<li>
					<span data-value class="key mono">{item.key}</span>
					<span data-value class="num count">{formatNumber(item.value, 0)}</span>
				</li>
			{/each}
		</ol>
	{/if}
</div>

<style>
	.breakdown {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}

	.head h3 {
		margin-right: auto;
	}

	.chart {
		width: 100%;
		min-width: 0;
	}

	.values {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.values li {
		display: flex;
		align-items: baseline;
		gap: 10px;
		font-size: 12.5px;
		padding: 2px 0;
		border-bottom: 1px solid var(--separator);
	}

	.values li:last-child {
		border-bottom: none;
	}

	.key {
		flex: 1 1 auto;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.count {
		flex: none;
		color: var(--label-secondary);
		/* A count must not break between its digits. It has no spaces, so
		   allowing breaks only at spaces means it never breaks at all. */
		overflow-wrap: normal;
		word-break: keep-all;
	}
</style>
