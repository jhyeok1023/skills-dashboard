<script lang="ts">
	import { api, ApiFailure } from '$lib/api';
	import type { Config, DiscoveryResponse, Identity, LogFormatPreview, Resource } from '$lib/types';
	import { appName } from '$lib/resources';
	import CopyValue from '$lib/components/CopyValue.svelte';
	import ResourcePicker from '$lib/components/ResourcePicker.svelte';

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
	/**
	 * What loading the stored config had to discard. A dropped value leaves a
	 * panel empty, and an empty panel says nothing about why — so it is said
	 * here, beside the fields that have to be filled in again.
	 */
	let configNotices = $state<string[]>([]);
	let saving = $state(false);
	let saved = $state(false);
	let saveError = $state('');

	/**
	 * What each 자동 조회 produced, keyed by kind.
	 *
	 * A kind that is absent has never been looked up; a kind whose `resources`
	 * is empty was looked up and found nothing. Those are different things and
	 * the page says so differently, so the two are never collapsed into one.
	 */
	let discovered = $state<Record<string, DiscoveryResponse>>({});
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
		api
			.meta()
			.then((m) => (configNotices = m.notices ?? []))
			.catch(() => {});
	});

	async function discover(kind: string, prefix = '') {
		discovering = { ...discovering, [kind]: true };
		discoveryError = { ...discoveryError, [kind]: '' };
		try {
			const res = await api.discover(kind, prefix);
			discovered = { ...discovered, [kind]: res };
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

	/**
	 * What a lookup that found nothing says.
	 *
	 * Saying it at all is the point: an empty result used to render as empty
	 * space, which is also what a lookup nobody ran looks like, and what a
	 * lookup that failed looked like on the fields with no error line.
	 */
	function emptyFor(noun: string): string {
		return `조회했지만 이 리전에 ${noun}이(가) 없습니다. 리전과 IAM 권한을 확인하세요.`;
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
				{#if identity.wafRegion}
					<span class="muted tiny">WAF 리전</span>
					<CopyValue value={identity.wafRegion} mono label="WAF 리전" />
				{/if}
			</div>
			<p class="tiny muted" data-value>
				액세스 키는 바이너리를 실행한 디렉터리의 .env 파일에서 읽습니다. 이 화면에서는 수정하지
				않습니다.
			</p>
			<p class="tiny muted" data-value>
				리전은 .env 의 AWS_REGION, WAF 리전은 ~/.skills-dashboard/config.json 의 wafRegion 이
				정합니다. AWS 클라이언트는 시작할 때 한 번 만들어지므로 변경하려면 재시작해야 합니다.
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

			{#if configNotices.length}
				<div class="notices stack" data-testid="config-notices">
					<p class="warning tiny" data-value>
						설정 파일에서 쓸 수 없는 값을 발견해 지운 뒤 시작했습니다. 아래에서 다시 선택하세요.
					</p>
					<ul class="tiny">
						{#each configNotices as note (note)}
							<li data-value>{note}</li>
						{/each}
					</ul>
				</div>
			{/if}

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
				{#if discovered.clusters?.resources.length}
					<ul class="chips">
						{#each discovered.clusters.resources as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => selectCluster(r)}>
									<span data-value>{r.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{:else if discovered.clusters}
					<p class="tiny muted" data-value>{emptyFor('EKS 클러스터')}</p>
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
				{#if discoveryError.loggroups}
					<p class="warning tiny" data-value>{discoveryError.loggroups}</p>
				{/if}
				{#if discovered.loggroups?.resources.length}
					<ul class="chips">
						{#each discovered.loggroups.resources as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => (config!.podLogGroup = r.id)}>
									<span data-value class="mono">{r.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{:else if discovered.loggroups}
					<p class="tiny muted" data-value>
						{emptyFor('/aws/containerinsights/ 로 시작하는 로그 그룹')}
					</p>
				{/if}
			</div>

			<div class="field">
				<label for="waflog">WAF 로그 그룹</label>
				<div class="row">
					<input
						id="waflog"
						class="control grow mono"
						placeholder="aws-waf-logs-..."
						bind:value={config.wafLogGroup}
					/>
					<button
						type="button"
						class="control"
						onclick={() => discover('waf-loggroups', 'aws-waf-logs-')}
					>
						{discovering['waf-loggroups'] ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#if discoveryError['waf-loggroups']}
					<p class="warning tiny" data-value>{discoveryError['waf-loggroups']}</p>
				{/if}
				{#if discovered['waf-loggroups']?.resources.length}
					<ul class="chips">
						{#each discovered['waf-loggroups'].resources as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => (config!.wafLogGroup = r.id)}>
									<span data-value class="mono">{r.name}</span>
								</button>
							</li>
						{/each}
					</ul>
				{:else if discovered['waf-loggroups']}
					<p class="tiny muted" data-value>
						{identity?.wafRegion ?? config.wafRegion} 에 aws-waf-logs- 로 시작하는 로그 그룹이 없습니다.
						WAF 로깅이 켜져 있는지 확인하세요.
					</p>
				{/if}
				<p class="tiny muted" data-value>
					CLOUDFRONT 스코프 WAF는 {identity?.wafRegion ?? config.wafRegion} 에만 로그를 남깁니다. 이 목록도
					그 리전에서 조회합니다.
				</p>
			</div>

			<div class="field">
				<label for="lb">로드 밸런서 (CloudWatch 차원)</label>
				<div class="row">
					<input
						id="lb"
						class="control grow mono"
						placeholder="app/my-alb/50dc6c495c0c9188"
						bind:value={config.loadBalancer}
					/>
					<button type="button" class="control" onclick={() => discover('loadbalancers')}>
						{discovering.loadbalancers ? '조회 중…' : '자동 조회'}
					</button>
				</div>
				{#if discoveryError.loadbalancers}
					<p class="warning tiny" data-value>{discoveryError.loadbalancers}</p>
				{/if}
				{#if discovered.loadbalancers?.resources.length}
					<ul class="chips">
						{#each discovered.loadbalancers.resources as r (r.id)}
							<li>
								<button type="button" class="chip" onclick={() => (config!.loadBalancer = r.id)}>
									<span data-value>{r.name}</span>
									<span class="tiny muted mono" data-value>{r.id}</span>
								</button>
							</li>
						{/each}
					</ul>
				{:else if discovered.loadbalancers}
					<p class="tiny muted" data-value>{emptyFor('로드 밸런서')}</p>
				{/if}
				<p class="tiny muted" data-value>
					ARN이 아니라 ARN의 뒤쪽 경로입니다. 전체 ARN을 붙여넣으면 저장할 때 변환합니다.
				</p>
				<p class="tiny muted" data-value>
					타겟 그룹별 지표는 이 값과 무관하게 조회됩니다. 이 값은 타겟 그룹을 하나도 고르지 않았을
					때 로드 밸런서 전체 지표를 보여주는 데만 쓰입니다.
				</p>
			</div>

			<ResourcePicker
				label="타겟 그룹"
				noun="타겟 그룹"
				grouped
				resources={discovered.targetgroups?.resources}
				truncated={discovered.targetgroups?.truncated}
				partial={discovered.targetgroups?.partial}
				selected={config.targetGroups}
				loading={discovering.targetgroups ?? false}
				error={discoveryError.targetgroups ?? ''}
				nameOf={appName}
				detailOf={(r) => r.id}
				idleHint="자동 조회를 눌러 이 리전의 타겟 그룹을 로드 밸런서별로 가져옵니다."
				onDiscover={() => discover('targetgroups')}
				onToggle={(r) => {
					config!.targetGroups = toggle(config!.targetGroups, r.id);
					if (r.extra?.loadBalancer && !config!.loadBalancer) {
						config!.loadBalancer = r.extra.loadBalancer;
					}
				}}
				onRemove={(id) => (config!.targetGroups = toggle(config!.targetGroups, id))}
			/>

			<ResourcePicker
				label="RDS Proxy"
				noun="RDS Proxy"
				resources={discovered.rdsproxies?.resources}
				truncated={discovered.rdsproxies?.truncated}
				partial={discovered.rdsproxies?.partial}
				selected={config.rdsProxies}
				loading={discovering.rdsproxies ?? false}
				error={discoveryError.rdsproxies ?? ''}
				detailOf={(r) => r.extra?.engine ?? ''}
				onDiscover={() => discover('rdsproxies')}
				onToggle={(r) => (config!.rdsProxies = toggle(config!.rdsProxies, r.id))}
				onRemove={(id) => (config!.rdsProxies = toggle(config!.rdsProxies, id))}
			/>

			<ResourcePicker
				label="Web ACL"
				noun="Web ACL"
				resources={discovered.webacls?.resources}
				truncated={discovered.webacls?.truncated}
				partial={discovered.webacls?.partial}
				selected={config.webAcls}
				loading={discovering.webacls ?? false}
				error={discoveryError.webacls ?? ''}
				detailOf={(r) => r.extra?.scope ?? ''}
				onDiscover={() => discover('webacls')}
				onToggle={(r) => (config!.webAcls = toggle(config!.webAcls, r.id))}
				onRemove={(id) => (config!.webAcls = toggle(config!.webAcls, id))}
			/>
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
				<label for="excludePaths">제외할 경로</label>
				<input
					id="excludePaths"
					class="control mono"
					value={config.logFormat.excludePaths.join(', ')}
					onchange={(e) => {
						config!.logFormat.excludePaths = (e.currentTarget as HTMLInputElement).value
							.split(',')
							.map((s) => s.trim())
							.filter(Boolean);
					}}
				/>
				<p class="tiny muted" data-value>
					헬스체크처럼 항상 들어오는 요청을 팟 로그 집계에서 뺍니다. 쿼리 단계에서 걸러지므로 응답
					시간 백분위와 요청 수, 비정상 응답 건수 모두에서 제외됩니다. 정확히 일치하는 경로만
					제외되며, 비워 두면 아무것도 제외하지 않습니다.
				</p>
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
					<div class="row">
						<p class="badge" data-intent={preview.matched ? 'good' : 'warn'} data-value>
							{preview.matched ? '인식됨' : '인식되지 않음'}
						</p>
						{#if preview.excluded}
							<p class="badge" data-intent="warn" data-value>제외 경로</p>
						{/if}
					</div>
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

	label {
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

	/* One line per discarded value, indented under the sentence that explains
	   why they were discarded. */
	.notices ul {
		margin: 0;
		padding-left: 18px;
		color: var(--label-secondary);
		display: grid;
		gap: 3px;
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
		/* A chip may carry a name and the dimension value under it; the gap is
		   what keeps the two from reading as one string. */
		display: inline-flex;
		align-items: baseline;
		gap: 6px;
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
