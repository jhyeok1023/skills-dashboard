<script lang="ts">
	import type { Stat } from '$lib/types';
	import { copyableValue, formatValue, intentVar } from '$lib/format';
	import CopyValue from './CopyValue.svelte';

	/**
	 * One headline number.
	 *
	 * The value arrives finished from the backend; this component formats it
	 * and nothing more. `basis` is shown as a subtitle because two stats can
	 * legitimately count different populations of the same-sounding thing —
	 * "요청 수" over lines carrying a status, versus over lines carrying a
	 * latency — and the reference implementation showed both under one label
	 * with no way to tell them apart.
	 */

	interface Props {
		stat: Stat;
	}

	let { stat }: Props = $props();

	const display = $derived(stat.text ?? formatValue(stat.value, stat.unit));
	const clipboard = $derived(stat.text ?? copyableValue(stat.value, stat.unit));
</script>

<div class="tile" data-stat={stat.key}>
	<div class="label" data-value>{stat.label}</div>
	<div class="value" style:color={intentVar(stat.intent)}>
		<CopyValue value={display} copy={clipboard} label={stat.label} />
	</div>
	{#if stat.basis}
		<div class="basis tiny muted" data-value title={stat.basis}>{stat.basis}</div>
	{/if}
</div>

<style>
	.tile {
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 10px 12px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
		/* Grows with its content: a long basis wraps, it does not clip. */
		min-width: 0;
	}

	.label {
		font-size: 12px;
		color: var(--label-secondary);
		font-weight: 500;
	}

	.value {
		font-size: 20px;
		font-weight: 600;
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
		line-height: 1.25;
	}

	.basis {
		margin-top: 1px;
	}
</style>
