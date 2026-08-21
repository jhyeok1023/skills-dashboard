<script lang="ts">
	import type { Stat } from '$lib/types';
	import { copyableValue, formatValue, intentVar } from '$lib/format';
	import { wafActionFromStatKey } from '$lib/wafAction';
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
	 *
	 * Weight carries the hierarchy, not space. A stat the backend marked bad or
	 * warn is set larger and heavier than an ordinary one, so a threshold
	 * breach is legible from across a room without the tile growing.
	 *
	 * WAF stats are deliberately not part of that: the backend sends them with
	 * no intent, because blocked traffic is this system's normal state and
	 * flagging it left every tile shouting at once. They take the colour of
	 * their action instead, which reads the value without demanding attention.
	 */

	interface Props {
		stat: Stat;
	}

	let { stat }: Props = $props();

	const display = $derived(stat.text ?? formatValue(stat.value, stat.unit));
	const clipboard = $derived(stat.text ?? copyableValue(stat.value, stat.unit));
	const action = $derived(wafActionFromStatKey(stat.key));
	const loud = $derived(stat.intent === 'bad' || stat.intent === 'warn');

	/**
	 * Flashes the tile when its number actually changes.
	 *
	 * Keyed on the rendered text so a refresh that returns the same value is
	 * silent — on a 30-second auto-refresh most numbers are unchanged, and
	 * flashing all of them would train the eye to ignore the one that moved.
	 */
	let flash = $state(false);
	let previous: string | undefined;

	$effect(() => {
		const now = display;
		if (previous !== undefined && previous !== now) {
			// Restart the animation even if one is already running. The class
			// comes off on animationend below, so the CSS duration is the only
			// place the flash's length is written down.
			flash = false;
			requestAnimationFrame(() => (flash = true));
		}
		previous = now;
	});
</script>

<div
	class="tile"
	class:flash
	class:loud
	data-stat={stat.key}
	style:color={action?.color ?? intentVar(stat.intent)}
	onanimationend={() => (flash = false)}
>
	<div class="label">
		{#if action}<span class="icon" aria-hidden="true">{action.icon}</span>{/if}
		<span data-value>{stat.label}</span>
	</div>
	<div class="value">
		<CopyValue value={display} copy={clipboard} label={stat.label} />
	</div>
	{#if stat.basis}
		<div class="basis tiny" data-value title={stat.basis}>{stat.basis}</div>
	{/if}
</div>

<style>
	.tile {
		display: flex;
		flex-direction: column;
		gap: 0;
		padding: 6px 8px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
		/* Grows with its content: a long basis wraps, it does not clip. */
		min-width: 0;
	}

	/* A tile the backend flagged is tinted as well as enlarged, so the reading
	   does not rest on size alone. */
	.tile.loud {
		background: color-mix(in oklch, currentcolor 10%, var(--bg-elevated));
	}

	.label {
		display: flex;
		align-items: baseline;
		gap: 4px;
		font-size: 11px;
		color: var(--label-secondary);
		font-weight: 500;
	}

	.icon {
		font-size: 10px;
		color: currentcolor;
		flex: none;
	}

	.value {
		font-size: 19px;
		font-weight: 600;
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
		line-height: 1.2;
	}

	/* Size and weight, not space, mark the number that needs reading first. */
	.tile.loud .value {
		font-size: 26px;
		font-weight: 700;
	}

	.basis {
		font-size: 10.5px;
		line-height: 1.35;
		color: var(--label-tertiary);
	}
</style>
