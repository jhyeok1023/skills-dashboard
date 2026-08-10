<script lang="ts">
	import { REFRESH_CHOICES, timeRange } from '$lib/timerange.svelte';
	import { formatTimestamp } from '$lib/format';
	import type { WindowJSON } from '$lib/types';

	/**
	 * The range and period selector.
	 *
	 * Only combinations the server accepts are offered, and four hours is the
	 * largest range there is — the cap is a product decision about how much a
	 * Logs Insights scan may cost, so it is visible in the control rather than
	 * discovered as an error.
	 *
	 * Auto-refresh defaults to off for the same reason: every refresh rescans
	 * the window, and Insights bills by the byte.
	 */

	interface Props {
		window?: WindowJSON | null;
		loading?: boolean;
	}

	let { window: win = null, loading = false }: Props = $props();
</script>

<div class="picker row" data-testid="time-range-picker">
	<div class="segmented" role="group" aria-label="조회 기간">
		{#each timeRange.ranges as r (r.range)}
			<button
				type="button"
				aria-pressed={timeRange.range === r.range}
				onclick={() => timeRange.setRange(r.range)}
			>
				{r.range}
			</button>
		{/each}
	</div>

	<label class="row gap-tight">
		<span class="tiny muted">간격</span>
		<select
			class="control"
			aria-label="집계 간격"
			value={timeRange.period}
			onchange={(e) => timeRange.setPeriod((e.currentTarget as HTMLSelectElement).value)}
		>
			{#each timeRange.periods as p (p)}
				<option value={p}>{p}</option>
			{/each}
		</select>
	</label>

	<label class="row gap-tight">
		<span class="tiny muted">자동 새로고침</span>
		<select
			class="control"
			aria-label="자동 새로고침 주기"
			value={String(timeRange.refreshSeconds)}
			onchange={(e) =>
				(timeRange.refreshSeconds = Number((e.currentTarget as HTMLSelectElement).value))}
		>
			{#each REFRESH_CHOICES as c (c.seconds)}
				<option value={String(c.seconds)}>{c.label}</option>
			{/each}
		</select>
	</label>

	<button type="button" class="control" onclick={() => timeRange.refresh()} disabled={loading}>
		{loading ? '조회 중…' : '새로고침'}
	</button>

	{#if win}
		<span class="span tiny muted" data-value>
			{formatTimestamp(win.start)} – {formatTimestamp(win.end)} · {win.period}초 버킷
		</span>
	{/if}
</div>

<style>
	.picker {
		gap: 12px;
		align-items: center;
	}

	.gap-tight {
		gap: 5px;
	}

	.span {
		font-variant-numeric: tabular-nums;
		margin-left: auto;
		/* The window description wraps onto its own line on a narrow viewport
		   rather than being clipped. */
		overflow-wrap: anywhere;
	}
</style>
