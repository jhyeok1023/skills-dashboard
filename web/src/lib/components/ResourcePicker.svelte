<script lang="ts">
	import type { Resource } from '$lib/types';
	import { emptyFor, filterGroups, groupByLoadBalancer } from '$lib/resources';
	import CopyValue from './CopyValue.svelte';

	/**
	 * One resource field on the settings page: a 자동 조회 button, whatever that
	 * produced, and the checkboxes for what to watch.
	 *
	 * It exists because the three fields it replaces had drifted apart. Target
	 * groups reported a failed lookup and showed a loading state; RDS Proxy and
	 * Web ACL did neither, so an AccessDenied on either one repainted the page
	 * exactly as it had been. And none of the three said anything when a lookup
	 * succeeded and found nothing, which made "this region has no proxies" and
	 * "the call failed" and "you have not pressed the button" one single
	 * appearance: an empty space under a label.
	 *
	 * So every outcome is named here, in one place, for all three.
	 */

	interface Props {
		label: string;
		/**
		 * undefined until 자동 조회 has been pressed; [] after a lookup that found
		 * nothing. The distinction is the whole point — collapsing the two with
		 * `?? []` restores the defect this component was written to remove.
		 */
		resources: Resource[] | undefined;
		selected: string[];
		loading: boolean;
		error: string;
		truncated?: boolean;
		partial?: string[];
		/** Files the rows under load balancer headings. Off by default. */
		grouped?: boolean;
		/** What each row is called. Defaults to the resource's own name. */
		nameOf?: (r: Resource) => string;
		/** The line under the name, for the value that has to be exact. */
		detailOf?: (r: Resource) => string;
		/** Shown before the button has ever been pressed. */
		idleHint?: string;
		/** The noun for "this region has no ___". Defaults to the label. */
		noun?: string;
		onDiscover: () => void;
		onToggle: (r: Resource) => void;
		onRemove: (id: string) => void;
	}

	let {
		label,
		resources,
		selected,
		loading,
		error,
		truncated = false,
		partial = [],
		grouped = false,
		nameOf = (r: Resource) => r.name,
		detailOf,
		idleHint,
		noun = label,
		onDiscover,
		onToggle,
		onRemove
	}: Props = $props();

	let query = $state('');

	/**
	 * Sets, not the arrays themselves. One target group per application puts
	 * hundreds of rows on screen with most of them ticked, and every checkbox,
	 * every heading count and the orphan list all ask "is this id selected?" on
	 * every keystroke and every toggle.
	 */
	const selectedIds = $derived(new Set(selected));
	const offeredIds = $derived(new Set((resources ?? []).map((r) => r.id)));

	// Grouping does not depend on the query, so it is not redone on every
	// keystroke: only the filter below reruns while the operator types.
	const allGroups = $derived(
		grouped
			? groupByLoadBalancer(resources ?? [])
			: [{ key: '', label: '', items: resources ?? [] }]
	);
	const groups = $derived(filterGroups(allGroups, query));

	/** How many of each heading's rows are ticked, counted once per render. */
	const chosenByKey = $derived(
		new Map(groups.map((g) => [g.key, g.items.filter((r) => selectedIds.has(r.id)).length]))
	);

	/** Selected ids the last lookup did not offer, so they can still be removed. */
	const orphans = $derived(selected.filter((id) => !offeredIds.has(id)));

	const filteredAway = $derived(
		(resources?.length ?? 0) - groups.reduce((n, g) => n + g.items.length, 0)
	);

	/**
	 * Selects or clears one heading's rows.
	 *
	 * It acts on what the filter currently shows, not on the whole group. A
	 * select-all that reaches past the visible rows is how an operator ends up
	 * watching two hundred target groups they never saw.
	 */
	function toggleGroup(items: Resource[]) {
		const all = items.every((r) => selectedIds.has(r.id));
		for (const r of items) {
			if (selectedIds.has(r.id) === all) onToggle(r);
		}
	}
</script>

<div class="field">
	<div class="row">
		<span class="label-text">{label}</span>
		<button type="button" class="control" onclick={onDiscover} disabled={loading}>
			{loading ? '조회 중…' : '자동 조회'}
		</button>
		{#if resources?.length}
			<span class="tiny muted">{selected.length} / {resources.length} 선택됨</span>
		{/if}
	</div>

	{#if error}
		<p class="warning tiny" data-value>{error}</p>
	{/if}

	{#each partial as note (note)}
		<p class="warning tiny" data-value>{note}</p>
	{/each}

	{#if truncated}
		<p class="warning tiny" data-value>
			목록이 너무 길어 중간에서 끊었습니다. 찾는 {noun}이(가) 없다면 아래 목록이 전부가 아닙니다.
		</p>
	{/if}

	{#if resources === undefined}
		<p class="tiny muted" data-value>
			{idleHint ?? '자동 조회를 눌러 계정에서 목록을 가져오세요.'}
		</p>
	{:else if resources.length === 0}
		<!-- The state that used to render as nothing at all. -->
		<p class="tiny muted" data-value>{emptyFor(noun)}</p>
	{:else}
		{#if resources.length > 8}
			<input
				class="control filter"
				type="search"
				placeholder="이름으로 거르기"
				aria-label="{label} 거르기"
				bind:value={query}
			/>
		{/if}

		{#if groups.length === 0}
			<p class="tiny muted" data-value>거른 조건에 맞는 항목이 없습니다.</p>
		{:else}
			<div class="picker-scroll">
				{#each groups as group (group.key)}
					{#if grouped}
						<div class="row group-head">
							<span class="group-name" data-value>{group.label}</span>
							<span class="tiny muted">{chosenByKey.get(group.key)} / {group.items.length}</span>
							<button type="button" class="control tiny" onclick={() => toggleGroup(group.items)}>
								{chosenByKey.get(group.key) === group.items.length ? '전체 해제' : '전체 선택'}
							</button>
						</div>
					{/if}
					{#each group.items as r (r.id)}
						<label class="check">
							<input type="checkbox" checked={selectedIds.has(r.id)} onchange={() => onToggle(r)} />
							<span data-value>{nameOf(r)}</span>
							{#if detailOf}
								<span class="tiny muted mono" data-value>{detailOf(r)}</span>
							{/if}
						</label>
					{/each}
				{/each}
			</div>
		{/if}

		{#if filteredAway > 0}
			<p class="tiny muted" data-value>{filteredAway}개가 걸러져 있습니다.</p>
		{/if}
	{/if}

	{#each orphans as id (id)}
		<div class="row selected">
			<CopyValue value={id} mono {label} />
			<button type="button" class="control tiny" onclick={() => onRemove(id)}>제거</button>
		</div>
	{/each}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 5px;
		min-width: 0;
	}

	.label-text {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--label-secondary);
	}

	.filter {
		max-width: 22rem;
	}

	/*
	 * One application per target group makes this list long enough to bury the
	 * save button, so it scrolls. The wrapper carries no data-value: it is
	 * scrollable on purpose, and the no-clipping rule is about values that
	 * cannot be read, not about a container that can be scrolled.
	 */
	.picker-scroll {
		display: flex;
		flex-direction: column;
		gap: 1px;
		max-height: 22rem;
		overflow-y: auto;
		overscroll-behavior-y: contain;
		padding: 2px 0;
	}

	.group-head {
		gap: 8px;
		padding: 8px 0 3px;
		align-items: baseline;
	}

	.group-head:first-child {
		padding-top: 0;
	}

	.group-name {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--label-primary);
		min-width: 0;
	}

	.check {
		display: flex;
		align-items: baseline;
		gap: 8px;
		flex-wrap: wrap;
		padding: 3px 0;
		font-weight: 400;
		font-size: 13px;
		color: var(--label-primary);
		min-width: 0;
	}

	.selected {
		gap: 8px;
	}
</style>
