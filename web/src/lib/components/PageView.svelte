<script lang="ts">
	import { untrack } from 'svelte';
	import { replaceState } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api, ApiFailure } from '$lib/api';
	import { timeRange } from '$lib/timerange.svelte';
	import type { Payload } from '$lib/types';
	import PanelCard from './PanelCard.svelte';
	import TimeRangePicker from './TimeRangePicker.svelte';

	/**
	 * Loads one page's snapshot and renders its panels.
	 *
	 * The whole page comes from a single request, so every panel on screen is
	 * plotted against one window. Fetching each panel separately would let two
	 * charts on the same screen describe spans seconds apart — which is how
	 * the old dashboard's numbers drifted out of agreement.
	 */

	interface Props {
		pageId: string;
		title: string;
		description?: string;
	}

	let { pageId, title, description }: Props = $props();

	let payload = $state<Payload | null>(null);
	let error = $state<ApiFailure | null>(null);
	let loading = $state(false);
	let controller: AbortController | null = null;

	async function load() {
		controller?.abort();
		controller = new AbortController();
		loading = true;
		error = null;
		try {
			payload = await api.page(pageId, timeRange.range, timeRange.period, controller.signal);
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') return;
			error = e instanceof ApiFailure ? e : new ApiFailure(0, String(e));
			payload = null;
		} finally {
			loading = false;
		}
	}

	// Restore the selection from the URL once, so a shared or reloaded link
	// opens the same window it was captured with.
	//
	// The read is untracked on purpose. Writing the URL below changes page.url,
	// so a tracked read here would make the two effects re-trigger each other
	// indefinitely — and an effect loop aborts before the fetch ever runs,
	// leaving the page stuck on its loading state.
	$effect(() => {
		untrack(() => timeRange.fromSearchParams(page.url.searchParams));
	});

	$effect(() => {
		// Track the selection and the manual-refresh nonce.
		const range = timeRange.range;
		const period = timeRange.period;
		void timeRange.nonce;
		void pageId;

		untrack(() => {
			const params = timeRange.toSearchParams();
			if (params.toString() === page.url.searchParams.toString()) return;
			try {
				// The path is the one already being viewed, so there is no route
				// to resolve — only the query string changes.
				// eslint-disable-next-line svelte/no-navigation-without-resolve
				replaceState(`${page.url.pathname}?${params}`, page.state);
			} catch {
				// Keeping the URL in step is a convenience. If the router is
				// not ready for it, the data still loads.
			}
		});

		if (!timeRange.isValid(range, period)) return;
		void load();
	});

	// Auto-refresh, off unless the operator asks for it.
	$effect(() => {
		const seconds = timeRange.refreshSeconds;
		if (seconds <= 0) return;
		const id = setInterval(() => timeRange.refresh(), seconds * 1000);
		return () => clearInterval(id);
	});
</script>

<div class="page stack">
	<header class="stack head">
		<h1 data-value>{title}</h1>
		{#if description}<p class="muted tiny" data-value>{description}</p>{/if}
		<TimeRangePicker window={payload?.window ?? null} {loading} />
	</header>

	{#if error}
		<div class="card error" role="alert">
			<h2 data-value>데이터를 불러오지 못했습니다</h2>
			<p data-value>{error.message}</p>
			{#if error.hint}<p class="muted tiny" data-value>{error.hint}</p>{/if}
			{#if error.isCredentialProblem}
				<p class="tiny"><a href={resolve('/settings')}>설정으로 이동</a></p>
			{/if}
		</div>
	{:else if payload}
		{#if payload.warnings?.length}
			{#each payload.warnings as warning (warning)}
				<p class="warning" data-value>{warning}</p>
			{/each}
		{/if}
		<div class="grid">
			{#each payload.panels as panel (panel.id)}
				<PanelCard {panel} window={payload.window} />
			{/each}
		</div>
	{:else}
		<div class="card"><p class="muted" data-value>조회 중…</p></div>
	{/if}
</div>

<style>
	.page {
		gap: 18px;
	}

	.head {
		gap: 6px;
	}

	.error h2 {
		margin-bottom: 6px;
		color: var(--intent-bad);
	}
</style>
