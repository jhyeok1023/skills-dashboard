<script lang="ts">
	/**
	 * How long ago the data on screen arrived.
	 *
	 * Its own component for one reason: the relative reading has to tick every
	 * second, and a one-second interval anywhere in PageView would invalidate
	 * the whole page — every panel, every chart — once a second. Here the tick
	 * touches one text node.
	 *
	 * The absolute clock time is shown too. "12초 전" is the reading you want
	 * at a glance; the wall clock is the one you need when comparing the screen
	 * against a log or another operator's.
	 */

	interface Props {
		/** Epoch milliseconds, or null before the first successful load. */
		at: number | null;
	}

	let { at }: Props = $props();

	let now = $state(Date.now());

	$effect(() => {
		if (at === null) return;
		const id = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(id);
	});

	const clock = new Intl.DateTimeFormat(undefined, {
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit',
		hour12: false
	});

	const ago = $derived.by(() => {
		if (at === null) return '';
		const seconds = Math.max(0, Math.round((now - at) / 1000));
		if (seconds < 60) return `${seconds}초 전`;
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}분 전`;
		return `${Math.floor(minutes / 60)}시간 전`;
	});
</script>

<span class="last tiny muted" data-testid="last-updated">
	{#if at === null}
		갱신 없음
	{:else}
		갱신 {clock.format(at)} · {ago}
	{/if}
</span>

<style>
	.last {
		font-variant-numeric: tabular-nums;
		white-space: normal;
	}
</style>
