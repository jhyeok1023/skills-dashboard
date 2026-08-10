<script lang="ts">
	import { api, ApiFailure } from '$lib/api';
	import type { Config, Identity, LogFormatPreview, Resource } from '$lib/types';
	import CopyValue from '$lib/components/CopyValue.svelte';

	/**
	 * Settings: what the dashboard watches, and how it reads a log line.
	 *
	 * Credentials are not edited here. They come from the .env file the binary
	 * was started with, so this page reports what that key resolved to and
	 * nothing more — there is no field that would put a secret into a browser.
	 */

	let config = $state<Config | null>(null);
	let identity = $state<Identity | null>(null);
	let credentialProblem = $state('');
	let saving = $state(false);
	let saved = $state(false);
	let saveError = $state('');

	let discovered = $state<Record<string, Resource[]>>({});
	let discovering = $state<Record<string, boolean>>({});
	let discoveryError = $state<Record<string, string>>({});

	let sample = $state('');
	let preview = $state<LogFormatPreview | null>(null);
	let previewError = $state('');

	$effect(() => {
		api
			.config()
			.then((c) => (config = c))
			.catch((e) => (saveError = String(e?.message ?? e)));
		api
			.identity()
			.then((id) => (identity = id))
			.catch((e) => (credentialProblem = e?.hint || e?.message || String(e)));
	});

	async function discover(kind: string, prefix = '') {
		discovering = { ...discovering, [kind]: true };
		discoveryError = { ...discoveryError, [kind]: '' };
		try {
			const res = await api.discover(kind, prefix);
			discovered = { ...discovered, [kind]: res.resources };
		} catch (e) {
			const msg = e instanceof ApiFailure ? e.message : String(e);
			discoveryError = { ...discoveryError, [kind]: msg };
		} finally {
			discovering = { ...discovering, [kind]: false };
		}
	}

	function toggle(list: string[], id: string): string[] {
		return list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
	}

	async function save() {
		if (!config) return;
		saving = true;
		saved = false;
		saveError = '';
		try {
			config = await api.saveConfig(config);
			saved = true;
			setTimeout(() => (saved = false), 2500);
		} catch (e) {
			saveError = e instanceof ApiFailure ? e.message : String(e);
		} finally {
			saving = false;
		}
	}

	async function runPreview() {
		if (!config || !sample.trim()) return;
		previewError = '';
		try {
			preview = await api.previewLogFormat(sample, config.logFormat);
		} catch (e) {
			preview = null;
			previewError = e instanceof ApiFailure ? e.message : String(e);
		}
	}

	function selectCluster(r: Resource) {
		if (!config) return;
		config.clusterName = r.id;
		if (!config.podLogGroup && r.extra?.logGroup) config.podLogGroup = r.extra.logGroup;
	}
</script>

<div class="page stack">
	<header class="stack head">
		<h1 data-value>설정</h1>
		<p class="muted tiny" data-value>
			모니터링 대상과 로그 파싱 규칙을 정합니다. 저장하면 캐시가 비워지고 다음 조회부터 반영됩니다.
		</p>
	</header>

	<!-- Credentials -->
	<section class="card stack">
		<h2 data-value>자격증명</h2>
		{#if identity}
			<div class="kv">
				<span class="muted tiny">계정</span>
				<CopyValue value={identity.account} mono label="계정 ID" />
				<span class="muted tiny">ARN</span>
				<CopyValue value={identity.arn} mono label="ARN" />
				<span class="muted tiny">리전</span>
				<CopyValue value={identity.region} mono label="리전" />
			</div>
			<p class="tiny muted" data-value>
				액세스 키는 바이너리를 실행한 디렉터리의 .env 파일에서 읽습니다. 이 화면에서는 수정하지
				않습니다.
			</p>
		{:else}
			<p class="warning" data-value>{credentialProblem || '자격증명을 확인하는 중입니다…'}</p>
			<pre class="env" data-value>AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
AWS_REGION=ap-northeast-2</pre>
		{/if}
	</section>

	{#if config}
		<!-- Resources -->
		<section class="card stack">
			<h2 data-value>모니터링 대상</h2>

			<div class="field">
				<label for="cluster">EKS 클러스터</label>
				<div class="row">
					<input id="cluster" class="control grow" bind:value={config.clusterName} />
					<button type="button" class="control" onclick={() => discover('clusters')}>
						{discovering.clusters ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#if discoveryError.clusters}
					<p class="warning tiny" data-value>{discoveryError.clusters}</p>
				{/if}
				{#if discovered.clusters?.length}
					<ul class="chips">
						{#each discovered.clusters as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => selectCluster(r)}>
									<span data-value>{r.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{/if}
			</div>

			<div class="field">
				<label for="namespace">네임스페이스</label>
				<input id="namespace" class="control" bind:value={config.namespace} />
				<p class="tiny muted" data-value>비워 두면 모든 네임스페이스를 조회합니다.</p>
			</div>

			<div class="field">
				<label for="podlog">팟 로그 그룹</label>
				<div class="row">
					<input id="podlog" class="control grow mono" bind:value={config.podLogGroup} />
					<button
						type="button"
						class="control"
						onclick={() => discover('loggroups', '/aws/containerinsights/')}
					>
						{discovering.loggroups ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#if discovered.loggroups?.length}
					<ul class="chips">
						{#each discovered.loggroups as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => (config!.podLogGroup = r.id)}>
									<span data-value class="mono">{r.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{/if}
			</div>

			<div class="field">
				<label for="waflog">WAF 로그 그룹</label>
				<div class="row">
					<input id="waflog" class="control grow mono" bind:value={config.wafLogGroup} />
					<button
						type="button"
						class="control"
						onclick={() => discover('loggroups', 'aws-waf-logs-')}
					>
						자동 조회
					</button>
				</div>
			</div>

			<div class="field">
				<label for="lb">로드 밸런서 (CloudWatch 차원)</label>
				<input id="lb" class="control mono" bind:value={config.loadBalancer} />
			</div>

			<div class="field">
				<div class="row">
					<span class="label-text">타겟 그룹</span>
					<button type="button" class="control" onclick={() => discover('targetgroups')}>
						{discovering.targetgroups ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#if discoveryError.targetgroups}
					<p class="warning tiny" data-value>{discoveryError.targetgroups}</p>
				{/if}
				{#each discovered.targetgroups ?? [] as r (r.id)}
					<label class="check">
						<input
							type="checkbox"
							checked={config.targetGroups.includes(r.id)}
							onchange={() => {
								config!.targetGroups = toggle(config!.targetGroups, r.id);
								if (r.extra?.loadBalancer && !config!.loadBalancer) {
									config!.loadBalancer = r.extra.loadBalancer;
								}
							}}
						/>
						<span data-value>{r.extra?.friendlyName ?? r.name}</span>
						<span class="tiny muted mono" data-value>{r.id}</span>
					</label>
				{/each}
				{#each config.targetGroups as tg (tg)}
					{#if !(discovered.targetgroups ?? []).some((r) => r.id === tg)}
						<div class="row selected">
							<CopyValue value={tg} mono label="타겟 그룹" />
							<button
								type="button"
								class="control tiny"
								onclick={() => (config!.targetGroups = toggle(config!.targetGroups, tg))}
							>
								제거
							</button>
						</div>
					{/if}
				{/each}
			</div>

			<div class="field">
				<div class="row">
					<span class="label-text">RDS Proxy</span>
					<button type="button" class="control" onclick={() => discover('rdsproxies')}>
						{discovering.rdsproxies ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#each discovered.rdsproxies ?? [] as r (r.id)}
					<label class="check">
						<input
							type="checkbox"
							checked={config.rdsProxies.includes(r.id)}
							onchange={() => (config!.rdsProxies = toggle(config!.rdsProxies, r.id))}
						/>
						<span data-value>{r.name}</span>
						<span class="tiny muted" data-value>{r.extra?.engine ?? ''}</span>
					</label>
				{/each}
			</div>

			<div class="field">
				<div class="row">
					<span class="label-text">Web ACL</span>
					<button type="button" class="control" onclick={() => discover('webacls')}>
						{discovering.webacls ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#each discovered.webacls ?? [] as r (r.id + (r.extra?.scope ?? ''))}
					<label class="check">
						<input
							type="checkbox"
							checked={config.webAcls.includes(r.id)}
							onchange={() => (config!.webAcls = toggle(config!.webAcls, r.id))}
						/>
						<span data-value>{r.name}</span>
						<span class="tiny muted" data-value>{r.extra?.scope ?? ''}</span>
					</label>
				{/each}
			</div>
		</section>

		<!-- Log format -->
		<section class="card stack">
			<h2 data-value>로그 파싱 규칙</h2>
			<p class="tiny muted" data-value>
				fluent-bit이 파싱해 둔 log_processed 필드를 우선 읽고, 없으면 평문에 정규식을 적용합니다.
				아래에 실제 로그 한 줄을 붙여넣어 규칙이 맞는지 확인한 뒤 저장하세요.
			</p>

			<div class="fields">
				{#each [['appField', '앱'], ['latencyField', '응답 시간'], ['statusField', '상태 코드'], ['methodField', '메소드'], ['pathField', '경로'], ['levelField', '레벨'], ['clientIpField', '클라이언트 IP']] as [key, label] (key)}
					<div class="field">
						<label for={key}>{label} 필드</label>
						<input
							id={key}
							class="control mono"
							value={config.logFormat[key as keyof typeof config.logFormat] as string}
							oninput={(e) => {
								// eslint-disable-next-line @typescript-eslint/no-explicit-any
								(config!.logFormat as any)[key] = (e.currentTarget as HTMLInputElement).value;
							}}
						/>
					</div>
				{/each}

				<div class="field">
					<label for="latencyUnit">응답 시간 단위</label>
					<select id="latencyUnit" class="control" bind:value={config.logFormat.latencyUnit}>
						<option value="ms">ms</option>
						<option value="s">s</option>
					</select>
				</div>

				<div class="field">
					<label for="okStatuses">정상 상태 코드</label>
					<input
						id="okStatuses"
						class="control mono"
						value={config.logFormat.okStatuses.join(', ')}
						onchange={(e) => {
							const raw = (e.currentTarget as HTMLInputElement).value;
							config!.logFormat.okStatuses = raw
								.split(',')
								.map((s) => Number(s.trim()))
								.filter((n) => Number.isFinite(n) && n > 0);
						}}
					/>
					<p class="tiny muted" data-value>이 코드가 아닌 응답이 비정상 응답 패널에 표시됩니다.</p>
				</div>
			</div>

			<div class="field">
				<label for="levelPattern">레벨 감지 정규식</label>
				<input id="levelPattern" class="control mono" bind:value={config.logFormat.levelPattern} />
			</div>

			<div class="field">
				<label for="textPattern">평문 파싱 정규식 (선택)</label>
				<input id="textPattern" class="control mono" bind:value={config.logFormat.textPattern} />
				<p class="tiny muted" data-value>
					이름 있는 캡처 그룹을 사용합니다. 예:
					(?P&lt;method&gt;[A-Z]+)\s+(?P&lt;path&gt;\S+)\s+(?P&lt;status&gt;\d{3})
				</p>
			</div>

			<div class="field">
				<label for="sample">샘플 로그 라인</label>
				<textarea
					id="sample"
					class="control sample mono"
					rows="4"
					bind:value={sample}
					placeholder="CloudWatch에서 로그 한 줄을 복사해 붙여넣으세요"></textarea>
				<button type="button" class="control" onclick={runPreview} disabled={!sample.trim()}>
					파싱 미리보기
				</button>
			</div>

			{#if previewError}
				<p class="warning" data-value>{previewError}</p>
			{/if}

			{#if preview}
				<div class="preview" data-testid="logfmt-preview">
					<p class="badge" data-intent={preview.matched ? 'good' : 'warn'} data-value>
						{preview.matched ? '인식됨' : '인식되지 않음'}
					</p>
					{#if preview.suggestion}
						<p class="warning tiny" data-value>{preview.suggestion}</p>
					{/if}
					<div class="kv">
						<span class="muted tiny">앱</span><span data-value>{preview.parsed.app || '—'}</span>
						<span class="muted tiny">팟</span><span data-value class="mono"
							>{preview.parsed.pod || '—'}</span
						>
						<span class="muted tiny">네임스페이스</span><span data-value
							>{preview.parsed.namespace || '—'}</span
						>
						<span class="muted tiny">메소드 · 경로</span><span data-value class="mono"
							>{preview.parsed.method || '—'}
							{preview.parsed.path || ''}</span
						>
						<span class="muted tiny">상태</span><span data-value>
							{preview.parsed.status || '—'}
							{#if preview.badStatus}<span class="badge" data-intent="bad">비정상</span>{/if}
						</span>
						<span class="muted tiny">응답 시간</span><span data-value>
							{preview.parsed.latencyMs === null ? '—' : `${preview.parsed.latencyMs} ms`}
						</span>
						<span class="muted tiny">레벨</span><span data-value>{preview.parsed.level || '—'}</span
						>
						<span class="muted tiny">클라이언트 IP</span><span data-value class="mono"
							>{preview.parsed.clientIp || '—'}</span
						>
					</div>
				</div>
			{/if}
		</section>

		<div class="row actions">
			<button type="button" class="control primary" onclick={save} disabled={saving}>
				{saving ? '저장 중…' : '저장'}
			</button>
			{#if saved}<span class="badge" data-intent="good" data-value>저장되었습니다</span>{/if}
			{#if saveError}<span class="warning" data-value>{saveError}</span>{/if}
		</div>
	{/if}
</div>

<style>
	.page {
		gap: 18px;
		max-width: 68rem;
	}

	.head {
		gap: 6px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 5px;
		min-width: 0;
	}

	.fields {
		display: grid;
		gap: 12px;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 14rem), 1fr));
	}

	label,
	.label-text {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--label-secondary);
	}

	.grow {
		flex: 1 1 18rem;
		min-width: 0;
	}

	.control.sample {
		resize: vertical;
		font-size: 12px;
		/* A pasted log line wraps; it is never cut off. */
		white-space: pre-wrap;
		overflow-wrap: anywhere;
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
	}

	.chips {
		list-style: none;
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		margin: 0;
		padding: 0;
	}

	.chip {
		border: 1px solid var(--separator);
		background: var(--fill-secondary);
		border-radius: 999px;
		padding: 3px 10px;
		font-size: 12px;
		/* Log group names are long; they wrap inside the chip. */
		white-space: normal;
		overflow-wrap: anywhere;
		text-align: left;
		max-width: 100%;
	}

	.kv {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 4px 12px;
		align-items: baseline;
		min-width: 0;
	}

	.env {
		margin: 0;
		padding: 10px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
		font-family: var(--font-mono);
		font-size: 12px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.preview {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
	}

	.selected {
		gap: 8px;
	}

	.actions {
		gap: 10px;
		position: sticky;
		bottom: 0;
		padding: 10px 0;
		background: linear-gradient(to top, var(--bg-secondary) 70%, transparent);
	}

	.control.primary {
		background: var(--accent);
		border-color: transparent;
		color: white;
		font-weight: 600;
		padding: 7px 18px;
	}
</style>
