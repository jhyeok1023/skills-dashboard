<script lang="ts">
	import type { Table } from '$lib/types';
	import { formatLogTimestamp, formatNumber, formatValue } from '$lib/format';
	import CopyValue from './CopyValue.svelte';

	/**
	 * A list plus the honest size of what it lists.
	 *
	 * `total` came from a separate, uncapped aggregate; `rows` is what fitted
	 * under the limit. The header therefore states both, and never derives one
	 * from the other. Deriving a total from `rows.length` is precisely how the
	 * reference implementation's headline froze at 300 while the chart beside
	 * it kept counting.
	 *
	 * The row count is also the same whether the panel is shown small or
	 * expanded. Varying it with the container changed the denominator of every
	 * bar in the old dashboard, so the same panel read differently in two
	 * places.
	 */

	interface Props {
		table: Table;
		caption?: string;
		/** Rows rendered before a "show the rest" control appears. */
		initialRows?: number;
	}

	let { table, caption, initialRows = 8 }: Props = $props();

	let expanded = $state(false);
	const visible = $derived(expanded ? table.rows : table.rows.slice(0, initialRows));
	const remaining = $derived(table.rows.length - visible.length);

	function cell(value: unknown, key: string, unit?: string): string {
		if (value === null || value === undefined || value === '') return '—';
		if (key === 'timestamp') return formatLogTimestamp(String(value));
		if (unit) {
			const n = typeof value === 'number' ? value : Number(value);
			if (!Number.isNaN(n)) return formatValue(n, unit as never);
		}
		if (typeof value === 'number') return formatNumber(value);
		const asNumber = Number(value);
		if (value !== '' && !Number.isNaN(asNumber) && /^\d+(\.\d+)?$/.test(String(value))) {
			return formatNumber(asNumber);
		}
		return String(value);
	}
</script>

<div class="wrap">
	<div class="head row">
		{#if caption}<h3 data-value>{caption}</h3>{/if}
		<!--
			Three different numbers, stated separately: how many exist, how many
			were fetched, how many are on screen. Collapsing them into one is
			how a header comes to report a capped array's length as a total.
			The visible count is the same everywhere this panel appears.
		-->
		<span class="counts tiny muted" data-value>
			전체 {formatNumber(table.total, 0)}건 · 조회 {formatNumber(table.rows.length, 0)}건 · 표시
			{formatNumber(visible.length, 0)}건
		</span>
		{#if table.truncated}
			<span class="badge" data-intent="warn" data-value>
				상위 {formatNumber(table.limit, 0)}건만 조회됨
			</span>
		{/if}
	</div>

	{#if table.rows.length === 0}
		<p class="muted tiny">이 구간에 해당하는 항목이 없습니다.</p>
	{:else}
		<div class="table-scroll">
			<table>
				<thead>
					<tr>
						{#each table.columns as col (col.key)}
							<th class:numeric={col.numeric} data-value>{col.label}</th>
						{/each}
					</tr>
				</thead>
				<tbody>
					{#each visible as row, i (i)}
						<tr>
							{#each table.columns as col (col.key)}
								<td class:numeric={col.numeric} class:mono={col.mono}>
									{#if col.copyable && row[col.key]}
										<CopyValue
											value={cell(row[col.key], col.key, col.unit)}
											copy={String(row[col.key])}
											mono={col.mono}
											label={col.label}
										/>
									{:else}
										<span data-value>{cell(row[col.key], col.key, col.unit)}</span>
									{/if}
								</td>
							{/each}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>

		{#if remaining > 0}
			<button type="button" class="control more" onclick={() => (expanded = true)}>
				나머지 {formatNumber(remaining, 0)}건 더 보기
			</button>
		{:else if expanded && table.rows.length > initialRows}
			<button type="button" class="control more" onclick={() => (expanded = false)}>접기</button>
		{/if}
	{/if}
</div>

<style>
	.wrap {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}

	.head {
		gap: 8px;
	}

	.head h3 {
		margin-right: auto;
	}

	.counts {
		font-variant-numeric: tabular-nums;
	}

	.more {
		align-self: flex-start;
	}
</style>
