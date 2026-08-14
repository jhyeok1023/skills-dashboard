<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, ApiFailure } from '$lib/api';
	import { formatNumber } from '$lib/format';
	import type { CheckResult, Config } from '$lib/types';
	import { visibleInterval } from '$lib/visibility';

	/**
	 * The traffic check.
	 *
	 * Every other screen here reads CloudWatch, which lags by minutes and, when
	 * a panel comes back empty, cannot say whether nothing happened or nothing
	 * was published. This one asks the service directly, and it only asks when
	 * told to: nothing on this page fires a request the operator did not.
	 *
	 * The history is deliberately in memory only. Persisting it would make this
	 * a monitoring system with a retention policy, an on-disk format and a
	 * question about what happens while the dashboard is closed. It is a button.
	 */

	const REPEAT_CHOICES = [
		{ label: '수동', seconds: 0 },
		{ label: '10초', seconds: 10 },
		{ label: '30초', seconds: 30 },
		{ label: '1분', seconds: 60 }
	];

	/** Kept short on purpose: this is a recent trend, not a record. */
	const HISTORY = 20;

	let config = $state<Config | null>(null);
	let configError = $state('');
	let results = $state<CheckResult[]>([]);
	let error = $state<ApiFailure | null>(null);
	let running = $state(false);
	let repeatSeconds = $state(0);

	const target = $derived(config?.check?.url ?? '');
	const latest = $derived(results[0] ?? null);

	$effect(() => {
		api
			.config()
			.then((c) => {
				config = c;
				configError = '';
			})
			.catch((e) => (configError = e?.message ?? '설정을 읽지 못했습니다.'));
	});

	async function run() {
		if (!target || running) return;
		running = true;
		error = null;
		try {
			const res = await api.check();
			results = [res, ...results].slice(0, HISTORY);
		} catch (e) {
			error = e instanceof ApiFailure ? e : new ApiFailure(0, String(e));
		} finally {
			running = false;
		}
	}

	// Quiet while the tab is hidden: this one hits the production service.
	$effect(() => {
		const seconds = repeatSeconds;
		if (seconds <= 0 || !target) return;
		return visibleInterval(() => void run(), seconds * 1000);
	});

	function verdict(r: CheckResult): { text: string; intent: string; icon: string } {
		if (r.error) return { text: '응답 없음', intent: 'bad', icon: '✕' };
		if (r.ok) return { text: '정상', intent: 'good', icon: '✓' };
		return { text: '비정상 응답', intent: 'bad', icon: '✕' };
	}

	function clock(iso: string): string {
		const d = new Date(iso);
		return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
	}
</script>

<div class="page">
	<div class="head-sticky">
		<header class="head-row">
			<h1 data-value>트래픽 점검</h1>

			{#if target}
				<button type="button" class="control primary" onclick={run} disabled={running}>
					<span class="spin-slot" aria-hidden="true">
						{#if running}<span class="spinner"></span>{/if}
					</span>
					<span>지금 점검</span>
				</button>

				<label class="row gap-tight">
					<span class="tiny muted">반복</span>
					<select
						class="control"
						aria-label="반복 주기"
						value={String(repeatSeconds)}
						onchange={(e) => (repeatSeconds = Number((e.currentTarget as HTMLSelectElement).value))}
					>
						{#each REPEAT_CHOICES as c (c.seconds)}
							<option value={String(c.seconds)}>{c.label}</option>
						{/each}
					</select>
				</label>
			{/if}
		</header>
	</div>

	<p class="desc muted tiny" data-value>
		설정된 주소로 대시보드가 직접 GET 요청을 한 번 보냅니다. CloudWatch 지표는 몇 분 늦고 값이
		비었을 때 "트래픽이 없었다"와 "게시되지 않았다"를 구분해 주지 않으므로, 지금 응답하는지는 여기서
		확인합니다. 결과는 이 화면에만 남고 새로고침하면 사라집니다.
	</p>

	{#if configError}
		<div class="card error" role="alert">
			<h2 data-value>설정을 불러오지 못했습니다</h2>
			<p data-value>{configError}</p>
		</div>
	{:else if config && !target}
		<div class="card" role="note">
			<h2 data-value>점검할 주소가 없습니다</h2>
			<p class="muted tiny" data-value>
				설정 화면의 <strong>트래픽 점검</strong> 항목에 서비스 주소를 입력하면 이 화면이 동작합니다.
			</p>
			<p class="tiny"><a href={resolve('/settings')}>설정으로 이동</a></p>
		</div>
	{:else if config}
		<section class="card panel">
			<header class="row">
				<h2 data-value>대상</h2>
			</header>
			<p class="mono target" data-value>{target}</p>
			<p class="tiny muted" data-value>
				정상 판정 기준: {config.check.expectStatus > 0
					? `${config.check.expectStatus} 응답만`
					: '2xx 응답'}
				· <a href={resolve('/settings')}>설정에서 변경</a>
			</p>

			{#if error}
				<p class="warning" data-value>{error.message}</p>
				{#if error.hint}<p class="tiny muted" data-value>{error.hint}</p>{/if}
			{/if}

			{#if latest}
				{@const v = verdict(latest)}
				<div class="latest" data-intent={v.intent} data-testid="latest">
					<span class="verdict" data-value>
						<span aria-hidden="true">{v.icon}</span>
						{v.text}
					</span>
					<span class="tiny muted" data-value>
						{latest.error
							? latest.error
							: `${latest.status} · ${formatNumber(latest.elapsedMs, 0)}ms · ${clock(latest.at)}`}
					</span>
				</div>
			{:else}
				<p class="muted tiny" data-value>아직 점검하지 않았습니다.</p>
			{/if}
		</section>

		{#if results.length > 1}
			<section class="card panel">
				<header class="row">
					<h2 data-value>최근 {results.length}회</h2>
				</header>
				<div class="table-scroll">
					<table>
						<thead>
							<tr>
								<th>시각</th>
								<th>결과</th>
								<th class="numeric">상태</th>
								<th class="numeric">소요</th>
							</tr>
						</thead>
						<tbody>
							{#each results as r, i (`${r.at}-${i}`)}
								{@const v = verdict(r)}
								<tr>
									<td class="mono"><span data-value>{clock(r.at)}</span></td>
									<td>
										<span class="verdict" data-intent={v.intent} data-value>
											<span aria-hidden="true">{v.icon}</span>
											{v.text}
										</span>
									</td>
									<td class="numeric"><span data-value>{r.status ?? '—'}</span></td>
									<td class="numeric"><span data-value>{formatNumber(r.elapsedMs, 0)}ms</span></td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</section>
		{/if}
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}

	/* Matches PageView: main is the scroll container, so this sticks to the top
	   of the data area without knowing the topbar's height. */
	.head-sticky {
		position: sticky;
		top: 0;
		z-index: var(--z-sticky-head);
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 0;
		background: var(--bg-secondary);
		border-bottom: 1px solid var(--separator);
	}

	.head-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px 12px;
		min-width: 0;
	}

	.head-row h1 {
		font-size: 17px;
		margin-right: auto;
	}

	.gap-tight {
		gap: 4px;
	}

	.panel {
		display: flex;
		flex-direction: column;
		gap: 7px;
		min-width: 0;
	}

	.target {
		overflow-wrap: anywhere;
	}

	.latest {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 4px 10px;
		padding: 8px 10px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
	}

	/* The glyph carries the verdict as well as the colour does, so a greyscale
	   print and a colourblind reader get the same answer. */
	.verdict {
		display: inline-flex;
		align-items: baseline;
		gap: 5px;
		font-weight: 650;
	}

	[data-intent='good'] .verdict,
	.verdict[data-intent='good'] {
		color: var(--intent-good);
	}

	[data-intent='bad'] .verdict,
	.verdict[data-intent='bad'] {
		color: var(--intent-bad);
	}

	.control.primary {
		display: inline-flex;
		align-items: center;
		gap: 5px;
	}

	/* Reserved whether or not it is spinning, so starting a check does not
	   reflow the control row. */
	.spin-slot {
		display: inline-flex;
		width: 9px;
		height: 9px;
		flex: none;
	}

	.error h2 {
		margin-bottom: 6px;
		color: var(--intent-bad);
	}
</style>
