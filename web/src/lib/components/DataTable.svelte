<script lang="ts">
	import { untrack } from 'svelte';
	import type { Row, Table } from '$lib/types';
	import { formatLogTimestamp, formatNumber, formatValue } from '$lib/format';
	import { wafAction } from '$lib/wafAction';
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
		/** Rows added per press of that control. */
		step?: number;
	}

	let { table, caption, initialRows = 8, step = 50 }: Props = $props();

	// The initial value on purpose: `shown` is the operator's position in the
	// list from here on, and a refresh must not collapse a list they expanded.
	let shown = $state(untrack(() => initialRows));
	let query = $state('');
	let sortKey = $state<string | null>(null);
	let sortDir = $state<'asc' | 'desc'>('desc');

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

	interface Prepared {
		raw: Row;
		display: Record<string, string>;
		/**
		 * The WAF action a cell holds, for the cells that hold one.
		 *
		 * Nothing marks these columns on the wire. The value identifies itself —
		 * `wafAction` returns null for anything that is not an action — so a
		 * latency or a pod name is never decorated with a glyph that would mean
		 * nothing there, and a new action column needs no backend flag.
		 */
		actions: Record<string, ReturnType<typeof wafAction>>;
		/** Everything on the row, lowercased, for the filter to scan once. */
		search: string;
	}

	/**
	 * Formats every fetched row once per payload, not once per render.
	 *
	 * Formatting is not free — each cell runs a regex and, for timestamps, an
	 * Intl formatter, and a full log table is 300 rows across eight columns.
	 * Doing it in the markup meant sorting, filtering or pressing "더 보기"
	 * re-formatted all 2,400 cells to change which slice was on screen. This
	 * depends on `table` alone, so those operations re-slice already-formatted
	 * strings and nothing is recomputed.
	 */
	const prepared = $derived.by<Prepared[]>(() => {
		const cols = table.columns;
		return table.rows.map((raw) => {
			const display: Record<string, string> = {};
			const actions: Prepared['actions'] = {};
			let search = '';
			for (const col of cols) {
				let text = cell(raw[col.key], col.key, col.unit);
				const action = col.numeric || col.unit ? null : wafAction(String(raw[col.key] ?? ''));
				if (action) {
					actions[col.key] = action;
					// The filter has to match both what is shown and what was
					// recorded: an operator types 차단 as readily as BLOCK.
					search += action.key.toLowerCase() + ' ';
					text = action.label;
				}
				display[col.key] = text;
				search += text.toLowerCase() + ' ';
			}
			return { raw, display, actions, search };
		});
	});

	/**
	 * The columns that get a column. Anything marked `detail` is on the wire for
	 * one row's expanded view and would only crowd the header out here.
	 *
	 * `prepared` above still walks every column, deliberately: a detail value is
	 * formatted once per payload like any other, and lands in `search`. So the
	 * filter box finds a row by its User-Agent even though no column shows one.
	 */
	const cols = $derived(table.columns.filter((c) => !c.detail));
	const hasDetail = $derived(table.columns.length > cols.length);

	/**
	 * Which row is open, keyed by its content.
	 *
	 * Not by index and not by object identity: sorting reorders the rows under
	 * the operator, and every poll rebuilds `prepared` from a fresh payload. An
	 * index would leave the detail attached to whatever row later took that
	 * position, and an object reference would collapse the panel on every
	 * refresh. Content survives both.
	 */
	let openKey = $state<string | null>(null);

	const filtered = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return prepared;
		return prepared.filter((r) => r.search.includes(q));
	});

	const sorted = $derived.by(() => {
		const key = sortKey;
		if (!key) return filtered;
		const col = table.columns.find((c) => c.key === key);
		const sign = sortDir === 'asc' ? 1 : -1;
		// Numeric columns sort by the underlying number, never by the rendered
		// string — "1,284" sorts before "9" as text.
		const numeric = !!col?.numeric || !!col?.unit;
		return [...filtered].sort((a, b) => {
			if (numeric) {
				const x = Number(a.raw[key] ?? NaN);
				const y = Number(b.raw[key] ?? NaN);
				if (!Number.isNaN(x) || !Number.isNaN(y)) {
					if (Number.isNaN(x)) return 1;
					if (Number.isNaN(y)) return -1;
					return (x - y) * sign;
				}
			}
			return a.display[key].localeCompare(b.display[key]) * sign;
		});
	});

	const visible = $derived(sorted.slice(0, shown));
	const remaining = $derived(sorted.length - visible.length);

	// findIndex, not a filter: a log table can hold many byte-identical rows,
	// and opening one must not open all of them.
	const openIndex = $derived(
		openKey === null ? -1 : visible.findIndex((r) => r.search === openKey)
	);

	function toggle(row: Prepared) {
		openKey = openKey === row.search ? null : row.search;
	}

	function toggleSort(key: string) {
		if (sortKey === key) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
			return;
		}
		sortKey = key;
		sortDir = 'desc';
	}

	function ariaSort(key: string): 'ascending' | 'descending' | 'none' {
		if (sortKey !== key) return 'none';
		return sortDir === 'asc' ? 'ascending' : 'descending';
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
		{#if table.rows.length > initialRows}
			<div class="tools row">
				<input
					class="control filter"
					type="search"
					bind:value={query}
					placeholder="이 표에서 찾기"
					aria-label="{caption ?? '표'} 필터"
				/>
				{#if query && sorted.length !== prepared.length}
					<span class="tiny muted">{formatNumber(sorted.length, 0)}건 일치</span>
				{/if}
			</div>
		{/if}

		<div class="table-scroll">
			<table>
				<thead>
					<tr>
						{#if hasDetail}
							<th class="expander" aria-label="상세"></th>
						{/if}
						{#each cols as col (col.key)}
							<th class:numeric={col.numeric} aria-sort={ariaSort(col.key)}>
								<button type="button" class="sort" onclick={() => toggleSort(col.key)}>
									<span data-value>{col.label}</span>
									{#if sortKey === col.key}
										<span class="arrow" aria-hidden="true">{sortDir === 'asc' ? '▲' : '▼'}</span>
									{/if}
								</button>
							</th>
						{/each}
					</tr>
				</thead>
				<tbody>
					{#each visible as row, i (i)}
						<!--
							The row is clickable for the mouse; the button inside it is the
							keyboard and screen-reader path, and carries aria-expanded. The
							guard is what stops a press of the copy button inside a cell from
							also toggling the row under it.
						-->
						<tr
							class:open={i === openIndex}
							onclick={hasDetail
								? (e) => {
										if (!(e.target as HTMLElement).closest('button, a, input')) toggle(row);
									}
								: undefined}
						>
							{#if hasDetail}
								<td class="expander">
									<button
										type="button"
										class="reveal"
										aria-expanded={i === openIndex}
										onclick={() => toggle(row)}
									>
										<span aria-hidden="true">{i === openIndex ? '▾' : '▸'}</span>
										<span class="tiny">상세</span>
									</button>
								</td>
							{/if}
							{#each cols as col (col.key)}
								<td class:numeric={col.numeric} class:mono={col.mono}>
									{#if row.actions[col.key]}
										<!-- Colour never carries the action alone: the glyph beside it
										     says the same thing in greyscale and to a colourblind eye. -->
										<span class="action" data-value style:color={row.actions[col.key]?.color}>
											<span aria-hidden="true">{row.actions[col.key]?.icon}</span>
											{row.display[col.key]}
										</span>
									{:else if col.copyable && row.raw[col.key]}
										<CopyValue
											value={row.display[col.key]}
											copy={String(row.raw[col.key])}
											mono={col.mono}
											label={col.label}
										/>
									{:else}
										<span data-value>{row.display[col.key]}</span>
									{/if}
								</td>
							{/each}
						</tr>
						{#if i === openIndex}
							<!--
								Rendered only while open, never rendered-and-hidden. A panel is
								shown twice — in its card and in its expand dialog — and the two
								are asserted to hold the same number of rows, which a detail row
								sitting in the DOM under `display: none` would quietly break.

								Every column, not just the detail ones: an operator who opened a
								row wants that request in one place, not half of it here and the
								other half back up in the table.
							-->
							<tr class="detail">
								<td colspan={cols.length + 1}>
									<dl>
										{#each table.columns as col (col.key)}
											<dt data-value>{col.label}</dt>
											<dd class:mono={col.mono}>
												{#if row.actions[col.key]}
													<span class="action" data-value style:color={row.actions[col.key]?.color}>
														<span aria-hidden="true">{row.actions[col.key]?.icon}</span>
														{row.display[col.key]}
													</span>
												{:else if col.copyable && row.raw[col.key]}
													<CopyValue
														value={row.display[col.key]}
														copy={String(row.raw[col.key])}
														mono={col.mono}
														label={col.label}
													/>
												{:else}
													<span data-value>{row.display[col.key]}</span>
												{/if}
											</dd>
										{/each}
									</dl>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>

		{#if remaining > 0}
			<!-- Grown a step at a time. Jumping straight to every fetched row
			     inserted 300 rows of DOM in one frame to reveal the next few. -->
			<button
				type="button"
				class="control more"
				onclick={() => (shown = Math.min(shown + step, sorted.length))}
			>
				{formatNumber(Math.min(step, remaining), 0)}건 더 보기 (남은 {formatNumber(remaining, 0)}건)
			</button>
		{:else if shown > initialRows}
			<button type="button" class="control more" onclick={() => (shown = initialRows)}>접기</button>
		{/if}
	{/if}
</div>

<style>
	.wrap {
		display: flex;
		flex-direction: column;
		gap: 6px;
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

	.tools {
		gap: 8px;
	}

	.filter {
		flex: 1;
		min-width: 8rem;
		max-width: 20rem;
	}

	.more {
		align-self: flex-start;
	}

	/* No `white-space: nowrap`: a value that cannot wrap is a value that gets
	   clipped in a narrow column, and nothing on this dashboard may be clipped.
	   The glyph and the label wrap together rather than being cut apart. */
	.action {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		font-weight: 600;
	}

	th.expander,
	td.expander {
		width: 1%;
	}

	.reveal {
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 4px;
		border: 0;
		border-radius: var(--radius-control);
		background: none;
		color: var(--label-tertiary);
		cursor: pointer;
	}

	.reveal:hover,
	tr.open .reveal {
		background: var(--fill-secondary);
		color: var(--label-secondary);
	}

	tr.open {
		background: var(--fill-secondary);
	}

	.detail > td {
		background: var(--bg-secondary);
	}

	/* auto-fit, so a narrow panel stacks the pairs and a wide one lays them out
	   in columns. The same detail has to be readable in a card and in the
	   expand dialog, which are very different widths. */
	.detail dl {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(22rem, 100%), 1fr));
		gap: 2px 16px;
		margin: 0;
		padding: 6px 2px;
	}

	.detail dt {
		color: var(--label-tertiary);
		font-size: 0.85em;
	}

	/* Wraps rather than clips: a User-Agent is the whole reason this exists, and
	   a truncated one answers nothing. */
	.detail dd {
		margin: 0 0 6px;
		overflow-wrap: anywhere;
	}
</style>
