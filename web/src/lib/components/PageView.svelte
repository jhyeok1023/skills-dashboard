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
		namespaceFilter?: boolean;
	}

	let { pageId, title, description, namespaceFilter = false }: Props = $props();

	let payload = $state<Payload | null>(null);
	let error = $state<ApiFailure | null>(null);
	let loading = $state(false);
	let controller: AbortController | null = null;
	let requestedKey = '';
	let namespaceMode = $state<'default' | 'all' | 'custom'>('default');
	let namespaceDraft = $state('');
	let appliedNamespace = $state('');

	function applyNamespace() {
		if (namespaceMode === 'all') {
			appliedNamespace = '*';
			return;
		}
		if (namespaceMode === 'custom') {
			const value = namespaceDraft.trim();
			if (!value) return;
			appliedNamespace = value;
			return;
		}
		appliedNamespace = '';
	}

	function submitNamespace(event: SubmitEvent) {
		event.preventDefault();
		applyNamespace();
	}

	async function load() {
		controller?.abort();
		const current = new AbortController();
		controller = current;
		loading = true;
		error = null;
		try {
			payload = await api.page(
				pageId,
				timeRange.range,
				timeRange.period,
				current.signal,
				namespaceFilter ? appliedNamespace : ''
			);
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') return;
			error = e instanceof ApiFailure ? e : new ApiFailure(0, String(e));
			payload = null;
		} finally {
			if (controller === current) loading = false;
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
		untrack(() => {
			timeRange.fromSearchParams(page.url.searchParams);
			if (!namespaceFilter) return;
			const namespace = page.url.searchParams.get('namespace') ?? '';
			if (namespace === '*') {
				namespaceMode = 'all';
				appliedNamespace = '*';
			} else if (namespace) {
				namespaceMode = 'custom';
				namespaceDraft = namespace;
				appliedNamespace = namespace;
			}
		});
	});

	$effect(() => {
		// Track the selection and the manual-refresh nonce.
		const range = timeRange.range;
		const period = timeRange.period;
		const namespace = namespaceFilter ? appliedNamespace : '';
		const nonce = timeRange.nonce;

		untrack(() => {
			const params = timeRange.toSearchParams();
			if (namespace) params.set('namespace', namespace);
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
		const key = `${pageId}\u0000${range}\u0000${period}\u0000${namespace}\u0000${nonce}`;
		if (key === requestedKey) return;
		requestedKey = key;
		void load();
	});

	$effect(() => () => controller?.abort());

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
		{#if namespaceFilter}
			<form class="scope-bar" onsubmit={submitNamespace}>
				<div class="scope-name">
					<span class="scope-dot" aria-hidden="true"></span>
					<span>namespace</span>
				</div>
				<select
					class="control"
					aria-label="namespace 필터 방식"
					bind:value={namespaceMode}
					onchange={() => {
						if (namespaceMode !== 'custom') applyNamespace();
					}}
				>
					<option value="default">설정값 사용</option>
					<option value="all">전체 namespace</option>
					<option value="custom">직접 입력</option>
				</select>
				{#if namespaceMode === 'custom'}
					<input
						class="control mono namespace-input"
						aria-label="조회할 namespace"
						placeholder="예: default"
						bind:value={namespaceDraft}
					/>
					<button class="control" type="submit" disabled={!namespaceDraft.trim()}>적용</button>
				{/if}
				<span class="scope-current mono" data-value>
					{appliedNamespace === '*' ? '전체' : appliedNamespace || '설정에 저장한 값'}
				</span>
			</form>
		{/if}
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

	.scope-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
		width: fit-content;
		max-width: 100%;
		padding: 7px 8px;
		border: 1px solid var(--separator);
		border-radius: var(--radius-control);
		background: var(--bg-elevated);
		box-shadow: var(--shadow-card);
	}

	.scope-name {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		padding: 0 4px;
		font-size: 12px;
		font-weight: 600;
		color: var(--label-secondary);
	}

	.scope-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--system-blue);
		box-shadow: 0 0 0 3px color-mix(in oklch, var(--system-blue) 16%, transparent);
	}

	.namespace-input {
		width: min(18rem, 48vw);
	}

	.scope-current {
		padding: 2px 6px;
		color: var(--label-secondary);
		font-size: 11.5px;
	}
</style>
