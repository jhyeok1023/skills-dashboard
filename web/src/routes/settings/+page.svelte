<script lang="ts">
	import { api, ApiFailure } from '$lib/api';
	import type {
		Config,
		CredentialsState,
		DiscoveryResponse,
		Identity,
		LogFormatPreview,
		Resource
	} from '$lib/types';
	import { appName, emptyFor } from '$lib/resources';
	import CopyValue from '$lib/components/CopyValue.svelte';
	import ResourcePicker from '$lib/components/ResourcePicker.svelte';

	/**
	 * Settings: the key, what the dashboard watches, and how it reads a log line.
	 *
	 * The key is edited here now. It used to be read-only on the grounds that a
	 * field holding a secret is one more way for it to leak, and that is still
	 * true — what changed is the other side of it: rotating a key meant editing
	 * a file most people never find and restarting the process. Saving verifies
	 * against AWS first and takes effect without a restart.
	 */

	let config = $state<Config | null>(null);
	let identity = $state<Identity | null>(null);
	let credentialProblem = $state('');

	/** The key as the server reports it, and the form that edits it. */
	let credentials = $state<CredentialsState | null>(null);
	let secretShown = $state(false);
	let credentialsSaving = $state(false);
	let credentialsSaved = $state(false);
	let credentialsError = $state('');
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
	 * Why there is no config to show.
	 *
	 * Separate from saveError because the save row lives inside `{#if config}`:
	 * a failed load used to write its reason into a field that, by definition,
	 * was not on the page. The screen went blank below the credentials card and
	 * said nothing at all.
	 */
	let loadError = $state('');

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

	function loadConfig() {
		loadError = '';
		api
			.config()
			.then((c) => (config = c))
			.catch((e) => (loadError = e instanceof ApiFailure ? e.message : String(e?.message ?? e)));
	}

	function loadIdentity() {
		identity = null;
		credentialProblem = '';
		return api
			.identity()
			.then((id) => (identity = id))
			.catch((e) => (credentialProblem = e?.hint || e?.message || String(e)));
	}

	$effect(() => {
		loadConfig();
		loadIdentity();
		api
			.credentials()
			.then((c) => (credentials = c))
			.catch((e) => (credentialsError = e instanceof ApiFailure ? e.message : String(e)));
		api
			.meta()
			.then((m) => (configNotices = m.notices ?? []))
			.catch(() => {});
	});

	async function saveCredentials() {
		if (!credentials) return;
		credentialsSaving = true;
		credentialsSaved = false;
		credentialsError = '';
		try {
			credentials = await api.saveCredentials({
				accessKeyId: credentials.accessKeyId.trim(),
				secretAccessKey: credentials.secretAccessKey.trim(),
				sessionToken: credentials.sessionToken?.trim() ?? '',
				region: credentials.region.trim()
			});
			credentialsSaved = true;
			setTimeout(() => (credentialsSaved = false), 2500);
			// The whole page was assembled by whichever key was in force. The
			// identity card is the part that is visibly wrong until it is refetched.
			await loadIdentity();
		} catch (e) {
			credentialsError = e instanceof ApiFailure ? e.message : String(e);
		} finally {
			credentialsSaving = false;
		}
	}

	async function clearCredentials() {
		credentialsSaving = true;
		credentialsSaved = false;
		credentialsError = '';
		try {
			credentials = await api.clearCredentials();
			await loadIdentity();
		} catch (e) {
			credentialsError = e instanceof ApiFailure ? e.message : String(e);
		} finally {
			credentialsSaving = false;
		}
	}

	/** Which key the dashboard is running on, in words. */
	function sourceLabel(state: CredentialsState): string {
		if (state.source === 'saved') return '저장된 키를 쓰고 있습니다.';
		if (state.source === 'env') {
			return state.envFile
				? `${state.envFile} 의 키를 쓰고 있습니다.`
				: '환경변수의 키를 쓰고 있습니다.';
		}
		return '사용 중인 키가 없습니다.';
	}

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

<!--
	Everything a lookup can report other than its results.

	The four single-value fields below each used to spell this out for
	themselves, and each remembered a different subset: two of the four never
	said a lookup had failed, none of them ever said the list had been cut short.
	ResourcePicker says all of it for the multi-select fields; this says the same
	things, in the same order, for the rest.
-->
{#snippet outcome(res: DiscoveryResponse | undefined, err: string, noun: string)}
	{#if err}
		<p class="warning tiny" data-value>{err}</p>
	{/if}
	{#each res?.partial ?? [] as note (note)}
		<p class="warning tiny" data-value>{note}</p>
	{/each}
	{#if res?.truncated}
		<p class="warning tiny" data-value>
			목록이 너무 길어 중간에서 끊었습니다. 찾는 {noun}이(가) 없다면 아래 목록이 전부가 아닙니다.
		</p>
	{/if}
	{#if res && !res.resources.length}
		<p class="tiny muted" data-value>{emptyFor(noun)}</p>
	{/if}
{/snippet}

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
		{:else}
			<p class="warning" data-value>{credentialProblem || '자격증명을 확인하는 중입니다…'}</p>
		{/if}

		{#if credentials}
			<div class="fields">
				<div class="field">
					<label for="accessKeyId">액세스 키 ID</label>
					<input
						id="accessKeyId"
						class="control mono"
						autocomplete="off"
						spellcheck="false"
						bind:value={credentials.accessKeyId}
					/>
				</div>

				<div class="field">
					<label for="secretAccessKey">시크릿 액세스 키</label>
					<div class="row">
						<input
							id="secretAccessKey"
							class="control mono grow"
							type={secretShown ? 'text' : 'password'}
							autocomplete="off"
							spellcheck="false"
							bind:value={credentials.secretAccessKey}
						/>
						<!-- The field is masked by default and revealed on request: the
						     value has to be checkable when a save is refused, and unreadable
						     the rest of the time. -->
						<button
							type="button"
							class="control"
							aria-pressed={secretShown}
							onclick={() => (secretShown = !secretShown)}
						>
							{secretShown ? '가리기' : '보기'}
						</button>
					</div>
				</div>

				<div class="field">
					<label for="sessionToken">세션 토큰</label>
					<input
						id="sessionToken"
						class="control mono"
						autocomplete="off"
						spellcheck="false"
						placeholder="임시 자격증명일 때만"
						bind:value={credentials.sessionToken}
					/>
				</div>

				<div class="field">
					<label for="credRegion">리전</label>
					<input
						id="credRegion"
						class="control mono"
						autocomplete="off"
						spellcheck="false"
						bind:value={credentials.region}
					/>
				</div>
			</div>

			<div class="row">
				<button
					type="button"
					class="control primary"
					disabled={credentialsSaving}
					onclick={saveCredentials}
				>
					{credentialsSaving ? '확인 중…' : '저장'}
				</button>
				{#if credentials.saved}
					<button
						type="button"
						class="control"
						disabled={credentialsSaving}
						onclick={clearCredentials}
					>
						저장된 키 지우기
					</button>
				{/if}
				{#if credentialsSaved}
					<span class="badge" data-intent="good" data-value>저장되었습니다</span>
				{/if}
			</div>

			{#if credentialsError}
				<p class="warning" data-value>{credentialsError}</p>
			{/if}

			<p class="tiny muted" data-value>{sourceLabel(credentials)}</p>
			<p class="tiny muted" data-value>
				저장하면 AWS 에 한 번 물어본 뒤 통과할 때만 ~/.skills-dashboard/credentials.json 에
				기록하고, 재시작 없이 바로 적용합니다. 저장된 키가 .env 보다 우선합니다.
			</p>
			<p class="tiny muted" data-value>
				WAF 리전은 ~/.skills-dashboard/config.json 의 wafRegion 이 정합니다.
			</p>
		{/if}
	</section>

	{#if loadError}
		<section class="card stack">
			<p class="warning" data-value>설정을 불러오지 못했습니다: {loadError}</p>
			<div class="row">
				<button type="button" class="control" onclick={loadConfig}>다시 시도</button>
			</div>
		</section>
	{/if}

	<!--
		Everything below is one value away from nothing at all.

		A single bad field used to take the whole form with it: the resource
		lists arrive as JSON, and one that came back `null` where the contract
		promised an array threw while mounting, so the monitoring targets, the
		log rules and the save button never reached the DOM. What was left
		looked like a settings page that simply had no settings — a shape that
		reads as a missing feature rather than as a fault, which is how it went
		unreported. This is the same bargain the server already makes in
		recoverPanics: a failure gets to be one message, not the whole screen.
	-->
	<svelte:boundary>
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
					{@render outcome(discovered.clusters, discoveryError.clusters, 'EKS 클러스터')}
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
					{@render outcome(
						discovered.loggroups,
						discoveryError.loggroups,
						'/aws/containerinsights/ 로 시작하는 로그 그룹'
					)}
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
					{@render outcome(
						discovered['waf-loggroups'],
						discoveryError['waf-loggroups'],
						'aws-waf-logs- 로 시작하는 로그 그룹'
					)}
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
					{/if}
					<p class="tiny muted" data-value>
						CLOUDFRONT 스코프 WAF는 {identity?.wafRegion ?? config.wafRegion} 에만 로그를 남깁니다. 이
						목록도 그 리전에서 조회하며, 비어 있다면 WAF 로깅이 켜져 있는지 확인하세요.
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
					{@render outcome(discovered.loadbalancers, discoveryError.loadbalancers, '로드 밸런서')}
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

			<!-- Traffic check -->
			<section class="card stack">
				<h2 data-value>트래픽 점검</h2>
				<p class="tiny muted" data-value>
					대시보드가 <strong>직접 GET 요청을 보내는 유일한 주소</strong>입니다. 나머지 화면은 전부
					CloudWatch만 읽습니다. 비워 두면 트래픽 점검 화면은 아무 요청도 보내지 않습니다.
				</p>

				<div class="field">
					<label for="check-url">서비스 주소</label>
					<input
						id="check-url"
						class="control"
						type="url"
						placeholder="https://api.example.com/health"
						bind:value={config.check.url}
					/>
					<p class="tiny muted" data-value>http 또는 https 만 쓸 수 있습니다.</p>
				</div>

				<div class="field">
					<label for="check-status">정상으로 볼 상태 코드</label>
					<input
						id="check-status"
						class="control"
						type="number"
						min="0"
						max="599"
						placeholder="비우면 2xx 전체"
						value={config.check.expectStatus || ''}
						oninput={(e) =>
							(config!.check.expectStatus =
								Number((e.currentTarget as HTMLInputElement).value) || 0)}
					/>
					<p class="tiny muted" data-value>
						비워 두면 2xx 응답을 정상으로 봅니다. 인증이 걸린 엔드포인트라 401이 정상인 경우처럼,
						특정 코드 하나만 정상으로 보려면 그 코드를 적으세요.
					</p>
				</div>
			</section>

			<!-- Log format -->
			<section class="card stack">
				<h2 data-value>로그 파싱 규칙</h2>
				<p class="tiny muted" data-value>
					자동 인식은 JSON access log와 Gin 기본 로그를 함께 읽습니다. 아래에 실제 로그 한 줄을
					붙여넣어 결과를 확인한 뒤 저장하세요.
				</p>

				<div class="field preset-field">
					<label for="logPreset">로그 형식 preset</label>
					<select id="logPreset" class="control" bind:value={config.logFormat.preset}>
						<option value="auto">자동 인식 · JSON + Gin</option>
						<option value="gin">Gin 기본 로그</option>
						<option value="json">JSON access log</option>
					</select>
					<p class="tiny muted" data-value>
						자동 인식에서는 JSON 필드를 먼저 쓰고, 없으면 Gin access log를 파싱합니다.
					</p>
				</div>

				<div class="fields">
					{#each [['appField', '앱'], ['latencyField', '응답 시간'], ['statusField', '상태 코드'], ['methodField', '메소드'], ['pathField', '경로'], ['levelField', '레벨'], ['clientIpField', '클라이언트 IP'], ['userAgentField', 'User-Agent']] as [key, label] (key)}
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
						<p class="tiny muted" data-value>
							이 코드가 아닌 응답이 비정상 응답 패널에 표시됩니다.
						</p>
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
					<input
						id="levelPattern"
						class="control mono"
						bind:value={config.logFormat.levelPattern}
					/>
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
							<span class="muted tiny">메소드 · 요청 대상</span><span data-value class="mono"
								>{preview.parsed.method || '—'}
								{preview.parsed.requestTarget || preview.parsed.path || ''}</span
							>
							<span class="muted tiny">상태</span><span data-value>
								{preview.parsed.status || '—'}
								{#if preview.badStatus}<span class="badge" data-intent="bad">비정상</span>{/if}
							</span>
							<span class="muted tiny">응답 시간</span><span data-value>
								{preview.parsed.latencyMs === null ? '—' : `${preview.parsed.latencyMs} ms`}
							</span>
							<span class="muted tiny">레벨</span><span data-value
								>{preview.parsed.level || '—'}</span
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

		{#snippet failed(error: unknown, reset: () => void)}
			<section class="card stack">
				<p class="warning" data-value>
					설정 화면을 그리지 못했습니다. 저장된 설정에 이 화면이 다룰 수 없는 값이 들어 있을 수
					있습니다.
				</p>
				<p class="tiny muted mono" data-value>{String(error)}</p>
				<div class="row">
					<button type="button" class="control" onclick={reset}>다시 시도</button>
				</div>
			</section>
		{/snippet}
	</svelte:boundary>
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
