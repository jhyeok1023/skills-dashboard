<script lang="ts">
	import favicon from '$lib/assets/favicon.svg';
	import '$lib/styles/app.css';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { api } from '$lib/api';
	import { timeRange } from '$lib/timerange.svelte';
	import type { Identity } from '$lib/types';
	import CopyValue from '$lib/components/CopyValue.svelte';

	let { children } = $props();

	let identity = $state<Identity | null>(null);
	let credentialProblem = $state('');
	let theme = $state<'auto' | 'light' | 'dark'>('auto');

	const nav = [
		{ href: resolve('/'), match: '/', label: '개요' },
		{ href: resolve('/logs/pod'), match: '/logs/pod', label: '팟 로그' },
		{ href: resolve('/logs/waf'), match: '/logs/waf', label: 'WAF' },
		// No badge in the bar itself: a badge would have to probe on every page
		// load, and that is traffic the operator did not ask to send.
		{ href: resolve('/check'), match: '/check', label: '트래픽 점검' },
		{ href: resolve('/infra/targetgroup'), match: '/infra/targetgroup', label: '타겟 그룹' },
		{ href: resolve('/infra/kubernetes'), match: '/infra/kubernetes', label: '팟 · 노드' },
		{ href: resolve('/infra/database'), match: '/infra/database', label: 'RDS Proxy' },
		{ href: resolve('/settings'), match: '/settings', label: '설정' }
	];

	function isActive(match: string): boolean {
		return match === '/' ? page.url.pathname === '/' : page.url.pathname.startsWith(match);
	}

	$effect(() => {
		// The range/period table comes from the server, so the picker can never
		// offer a combination the server would reject.
		api
			.meta()
			.then((meta) => timeRange.applyMeta(meta))
			.catch(() => {});

		api
			.identity()
			.then((id) => {
				identity = id;
				credentialProblem = '';
			})
			.catch((e) => {
				credentialProblem = e?.message ?? 'AWS 자격증명을 확인할 수 없습니다.';
			});
	});

	$effect(() => {
		const saved = localStorage.getItem('theme');
		if (saved === 'light' || saved === 'dark' || saved === 'auto') theme = saved;
	});

	$effect(() => {
		const root = document.documentElement;
		if (theme === 'auto') root.removeAttribute('data-theme');
		else root.setAttribute('data-theme', theme);
		localStorage.setItem('theme', theme);
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<title>모니터링 대시보드</title>
</svelte:head>

<div class="shell">
	<header class="topbar">
		<a class="brand" href={resolve('/')}>모니터링</a>

		<nav aria-label="주요 화면">
			{#each nav as item (item.href)}
				<a href={item.href} aria-current={isActive(item.match) ? 'page' : undefined}>{item.label}</a
				>
			{/each}
		</nav>

		<div class="right row">
			{#if identity}
				<span class="tiny muted account">
					<CopyValue value={identity.account} mono inline label="AWS 계정 ID" />
					<span data-value>· {identity.region}</span>
				</span>
			{:else if credentialProblem}
				<a class="tiny problem" href={resolve('/settings')} data-value>자격증명 필요</a>
			{/if}

			<select
				class="control theme"
				aria-label="테마"
				value={theme}
				onchange={(e) =>
					(theme = (e.currentTarget as HTMLSelectElement).value as 'auto' | 'light' | 'dark')}
			>
				<option value="auto">시스템</option>
				<option value="light">라이트</option>
				<option value="dark">다크</option>
			</select>
		</div>
	</header>

	<main>
		{@render children()}
	</main>
</div>

<style>
	/*
	 * A fixed chrome with a scrolling data area, rather than one long page.
	 *
	 * The nav stays put and the page's own header can stick to the top of the
	 * data area without having to know how tall the nav happens to be — which
	 * it does not, since the links wrap onto a second row when the viewport is
	 * narrow.
	 */
	.shell {
		height: 100dvh;
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	.topbar {
		z-index: var(--z-topbar);
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 16px;
		padding: 7px 16px;
		/* Opaque on purpose. A translucent bar needs backdrop-filter, and that
		   is a full-width blur of everything behind it, recomputed on every
		   scrolled frame — paid for continuously, to read the panel edges
		   sliding underneath. */
		background: var(--bg-primary);
		border-bottom: 1px solid var(--separator);
		flex: none;
	}

	.brand {
		font-weight: 650;
		color: var(--label-primary);
		letter-spacing: -0.01em;
	}

	nav {
		display: flex;
		flex-wrap: wrap;
		gap: 2px;
	}

	nav a {
		padding: 4px 10px;
		border-radius: 7px;
		color: var(--label-secondary);
		font-size: 13.5px;
		/* Nav labels wrap onto a second row on narrow viewports rather than
		   being clipped or forcing the page to scroll sideways. */
		white-space: normal;
	}

	nav a:hover {
		background: var(--fill-secondary);
		text-decoration: none;
	}

	nav a[aria-current='page'] {
		background: var(--fill-primary);
		color: var(--label-primary);
		font-weight: 600;
	}

	.right {
		margin-left: auto;
		gap: 10px;
	}

	.account {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		flex-wrap: wrap;
	}

	.problem {
		color: var(--intent-warn);
		font-weight: 600;
	}

	.theme {
		padding: 3px 6px;
		font-size: 12px;
	}

	main {
		flex: 1;
		width: 100%;
		max-width: 1600px;
		margin: 0 auto;
		padding: 0 16px 16px;
		min-width: 0;
		min-height: 0;
		/* The data area is what scrolls. Sideways it does not: a wide table
		   scrolls inside its own container, as it always has. */
		overflow-y: auto;
		overflow-x: hidden;
		/* No rubber-banding past the ends of the list. */
		overscroll-behavior-y: contain;
	}

	@media (max-width: 640px) {
		.topbar {
			padding: 7px 10px;
		}
		main {
			padding: 0 10px 10px;
		}
	}
</style>
