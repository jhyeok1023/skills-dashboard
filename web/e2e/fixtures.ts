import type { Page } from '@playwright/test';

/**
 * Fixtures for the layout tests.
 *
 * The values here are deliberately awkward: a pod name long enough to overflow
 * its column, an ARN, a query string, a basis sentence in Korean. Layout bugs
 * show up on the longest string in the set, not the average one.
 */

const START = 1786320000; // 2026-08-10T06:40:00Z
const PERIOD = 300;
const BUCKETS = 12;

export const timestamps = Array.from({ length: BUCKETS }, (_, i) => START + i * PERIOD);

function series(label: string, unit: string, color: string, seed: number) {
	return {
		label,
		unit,
		color,
		// A gap every fifth bucket, so the null-handling is exercised.
		values: timestamps.map((_, i) => (i % 5 === 4 ? null : seed + (i % 7) * 3))
	};
}

export const LONG_POD = 'product-api-deployment-7d9f8c6b5a-x2m4q-with-a-very-long-suffix';
export const LONG_ARN =
	'arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/k8s-default-product-d6d507c878/73e2d6bc24d8a067';
export const LONG_PATH = '/v1/organizations/12345/members?include=profile,settings&sort=-createdAt';

const window = {
	start: START,
	end: START + BUCKETS * PERIOD,
	period: PERIOD,
	range: '1h',
	timestamps
};

const latencyPanel = {
	id: 'pod-latency',
	title: '팟 응답 시간',
	series: [
		series(`${LONG_POD} · p50`, 'ms', 'systemTeal', 10),
		series(`${LONG_POD} · p99`, 'ms', 'systemIndigo', 120)
	],
	stats: [
		{
			key: 'pod.p99.max',
			label: '최대 p99',
			value: 182.4,
			unit: 'ms',
			basis: 'latency_ms 가 있는 요청, 구간 전체, /health · /healthcheck 제외'
		},
		{
			key: 'pod.requests.total',
			label: '요청 수',
			value: 12840,
			unit: 'count',
			basis: 'status 가 있는 로그 라인, /health · /healthcheck 제외'
		},
		{
			key: 'pod.latencySamples.total',
			label: '응답 시간 표본 수',
			value: 12010,
			unit: 'count',
			basis: 'latency_ms 가 있는 로그 라인, /health · /healthcheck 제외'
		}
	]
};

const statusPanel = {
	id: 'pod-status-codes',
	title: '비정상 응답 코드',
	series: [series('503', 'count', 'systemRed', 40), series('404', 'count', 'systemOrange', 12)],
	stats: [
		{
			key: 'pod.badStatus.total',
			label: '비정상 응답',
			value: 1284,
			unit: 'count',
			intent: 'bad',
			basis: '상태 코드가 200, 201 가 아닌 요청 (집계 전체)'
		}
	],
	table: {
		columns: [
			{ key: 'timestamp', label: '시각', mono: true },
			{ key: 'status', label: '코드', numeric: true, copyable: true },
			{ key: 'path', label: '경로', mono: true, copyable: true },
			{ key: 'pod', label: '팟', mono: true, copyable: true },
			{ key: 'clientIp', label: '클라이언트 IP', mono: true, copyable: true }
		],
		rows: Array.from({ length: 300 }, () => ({
			timestamp: '2026-08-10 07:12:04.000',
			status: '503',
			path: LONG_PATH,
			pod: LONG_POD,
			clientIp: '10.0.3.123'
		})),
		// Counted independently of the 300 rows carried.
		total: 1284,
		truncated: true,
		limit: 300
	}
};

const targetGroupPanel = {
	id: 'targetgroup',
	title: '타겟 그룹',
	series: [
		series('응답 시간 p99', 's', 'systemIndigo', 0.12),
		series('대상 5xx', 'count', 'systemRed', 3)
	],
	stats: [
		{
			key: 'tg.p99.max',
			label: '최대 응답 시간 p99',
			value: 0.184,
			unit: 's',
			basis: 'TargetResponseTime p99, 선택 구간 전체'
		},
		{
			key: 'tg.5xx.total',
			label: '대상 5xx',
			value: 91,
			unit: 'count',
			intent: 'bad',
			basis: 'HTTPCode_Target_5XX_Count Sum'
		}
	]
};

const breakdownPanel = {
	id: 'waf-breakdown',
	title: 'WAF 통계',
	stats: [
		{
			key: 'waf.requests.total',
			label: '요청 수',
			value: 17000,
			unit: 'count',
			basis: 'WAF 로그 메소드별 집계 합계 (전체)'
		}
	],
	bars: { keyColumn: 'key', valueColumn: 'count', groupColumn: 'dimension' },
	table: {
		columns: [
			{ key: 'dimension', label: '구분' },
			{ key: 'key', label: '값', mono: true, copyable: true },
			{ key: 'count', label: '건수', numeric: true }
		],
		rows: [
			{ dimension: 'method', key: 'GET', count: '15000' },
			{ dimension: 'method', key: 'POST', count: '2000' },
			{ dimension: 'path', key: LONG_PATH, count: '820' },
			{
				dimension: 'header:User-Agent',
				key: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)',
				count: '1100'
			}
		],
		total: 4,
		truncated: false,
		limit: 20
	}
};

const countsPanel = {
	id: 'counts',
	title: '팟 · 노드 개수',
	series: [
		series('실행 중 팟', 'count', 'systemBlue', 8),
		series('노드', 'count', 'systemGreen', 3)
	],
	stats: [
		{
			key: 'pods.current',
			label: '팟 (현재)',
			value: 9,
			unit: 'count',
			basis: 'service_number_of_running_pods 합계'
		},
		{
			key: 'pods.min',
			label: '팟 (최소)',
			value: 6,
			unit: 'count',
			basis: '관측값 (구간 내 최소/최대)'
		},
		{
			key: 'pods.max',
			label: '팟 (최대)',
			value: 14,
			unit: 'count',
			basis: '관측값 (구간 내 최소/최대)'
		},
		{
			key: 'nodes.current',
			label: '노드 (현재)',
			value: 4,
			unit: 'count',
			basis: 'cluster_node_count'
		},
		{
			key: 'nodes.min',
			label: '노드 (최소)',
			value: 2,
			unit: 'count',
			basis: 'EKS 노드그룹 scalingConfig 합계 (general, spot)'
		},
		{
			key: 'nodes.max',
			label: '노드 (최대)',
			value: 9,
			unit: 'count',
			basis: 'EKS 노드그룹 scalingConfig 합계 (general, spot)'
		}
	]
};

const podStatusPanel = {
	id: 'pod-status',
	title: '팟 상태',
	series: [
		series('Running', 'count', 'systemGreen', 9),
		series('Pending', 'count', 'systemOrange', 1),
		series('Failed', 'count', 'systemRed', 0),
		series('컨테이너 재시작', 'count', 'systemPink', 2)
	],
	stats: [
		{
			key: 'pod.running',
			label: 'Running',
			value: 9,
			unit: 'count',
			intent: 'good',
			basis: 'pod_status_running'
		},
		{
			key: 'pod.pending',
			label: 'Pending',
			value: 1,
			unit: 'count',
			intent: 'warn',
			basis: 'pod_status_pending'
		},
		{
			key: 'pod.restarts',
			label: '컨테이너 재시작',
			value: 12,
			unit: 'count',
			intent: 'bad',
			basis: 'pod_number_of_container_restarts 합계'
		}
	],
	warnings: [
		'Container Insights에는 OOMKilled 전용 지표가 없습니다. CrashLoop은 재시작 증가로, OOM은 팟 로그의 OOMKilled 패턴으로 확인하세요.'
	]
};

const wafTrafficPanel = {
	id: 'waf-traffic',
	title: 'WAF 트래픽',
	series: [
		series('ALLOW', 'count', 'systemGreen', 900),
		series('BLOCK', 'count', 'systemPink', 60)
	],
	stats: [
		{
			key: 'waf.log.allow',
			label: 'ALLOW',
			value: 16200,
			unit: 'count',
			intent: 'good',
			basis: 'WAF 로그 action 집계 (전체)'
		},
		{
			key: 'waf.log.block',
			label: 'BLOCK',
			value: 800,
			unit: 'count',
			intent: 'bad',
			basis: 'WAF 로그 action 집계 (전체)'
		}
	]
};

const errorPanel = {
	id: 'pod-errors',
	title: 'ERROR · WARN 로그',
	series: [series('error', 'count', 'systemRed', 30), series('warn', 'count', 'systemOrange', 8)],
	stats: [
		{
			key: 'pod.error.total',
			label: 'ERROR',
			value: 412,
			unit: 'count',
			intent: 'bad',
			basis: 'level 또는 메시지 패턴이 error 계열 (집계 전체)'
		},
		{
			key: 'pod.warn.total',
			label: 'WARN',
			value: 77,
			unit: 'count',
			intent: 'warn',
			basis: 'level 또는 메시지 패턴이 warn 계열 (집계 전체)'
		}
	],
	table: {
		columns: [
			{ key: 'timestamp', label: '시각', mono: true },
			{ key: 'pod', label: '팟', mono: true, copyable: true },
			{ key: 'message', label: '메시지', mono: true, copyable: true }
		],
		rows: Array.from({ length: 300 }, () => ({
			timestamp: '2026-08-10 07:31:00.000',
			pod: LONG_POD,
			message:
				'ERROR failed to acquire connection from pool after 30s: context deadline exceeded (pool size 20, in use 20, waiters 41)'
		})),
		total: 489,
		truncated: true,
		limit: 300
	}
};

const rdsPanel = {
	id: 'rds-proxy',
	title: 'RDS Proxy 커넥션',
	series: [
		series('DB 커넥션', 'conn', 'systemPurple', 18),
		series('클라이언트 커넥션', 'conn', 'systemBlue', 40)
	],
	stats: [
		{
			key: 'proxy.db.current',
			label: 'DB 커넥션 (현재)',
			value: 21,
			unit: 'conn',
			basis: 'DatabaseConnections Average'
		},
		{
			key: 'proxy.max.allowed',
			label: '최대 허용 커넥션',
			value: 90,
			unit: 'conn',
			basis: 'MaxDatabaseConnectionsAllowed'
		}
	]
};

const pages: Record<string, unknown[]> = {
	overview: [
		latencyPanel,
		statusPanel,
		targetGroupPanel,
		countsPanel,
		podStatusPanel,
		wafTrafficPanel
	],
	'pod-logs': [latencyPanel, statusPanel, errorPanel],
	waf: [wafTrafficPanel, breakdownPanel],
	targetgroup: [targetGroupPanel],
	kubernetes: [countsPanel, podStatusPanel],
	database: [rdsPanel]
};

const meta = {
	maxRangeSeconds: 14400,
	defaultRange: '1h',
	ranges: [
		{ range: '15m', seconds: 900, periods: ['1m'], defaultPeriod: '1m' },
		{ range: '30m', seconds: 1800, periods: ['1m', '5m'], defaultPeriod: '1m' },
		{ range: '1h', seconds: 3600, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
		{ range: '2h', seconds: 7200, periods: ['1m', '5m', '10m'], defaultPeriod: '1m' },
		{ range: '4h', seconds: 14400, periods: ['1m', '5m', '10m', '1h'], defaultPeriod: '5m' }
	],
	limits: {
		logRows: 300,
		topN: 20,
		insightsConcurrency: 6,
		queryTimeoutSeconds: 45,
		cacheTtlSeconds: 30
	},
	// The dashboard starts on a repaired config rather than refusing to boot,
	// so the settings page has to account for the value that went missing.
	notices: ['loadBalancer "my-alb"는 CloudWatch 차원 값이 아닙니다 → 이 값을 비웠습니다.']
};

const identity = {
	account: '123456789012',
	arn: 'arn:aws:iam::123456789012:user/dashboard-readonly',
	userId: 'AIDAEXAMPLEEXAMPLE',
	region: 'ap-northeast-2',
	wafRegion: 'us-east-1'
};

/**
 * Discovery answers, per kind. They differ by kind on purpose: the WAF log
 * group listing comes from us-east-1 and the working-region listing does not
 * carry it, and a load balancer is offered as its CloudWatch dimension rather
 * than its ARN.
 */
const discoveries: Record<string, unknown[]> = {
	clusters: [
		{ id: 'prod', name: 'prod', extra: { logGroup: '/aws/containerinsights/prod/application' } }
	],
	loggroups: [
		{
			id: '/aws/containerinsights/prod/application',
			name: '/aws/containerinsights/prod/application'
		}
	],
	'waf-loggroups': [{ id: 'aws-waf-logs-demo', name: 'aws-waf-logs-demo' }],
	loadbalancers: [
		{
			id: 'app/my-alb/50dc6c495c0c9188',
			name: 'my-alb',
			arn: 'arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188',
			extra: { type: 'application', scheme: 'internet-facing' }
		}
	]
};

const config = {
	region: 'ap-northeast-2',
	wafRegion: 'us-east-1',
	clusterName: 'prod',
	namespace: 'default',
	podLogGroup: '/aws/containerinsights/prod/application',
	wafLogGroup: 'aws-waf-logs-demo',
	loadBalancer: 'app/my-alb/50dc6c495c0c9188',
	targetGroups: [LONG_ARN],
	rdsProxies: ['app-proxy'],
	webAcls: ['skills-waf'],
	wafHeaders: ['Host', 'User-Agent'],
	logFormat: {
		timeField: 'time',
		messageField: 'log',
		processedField: 'log_processed',
		streamField: 'stream',
		appField: 'app',
		latencyField: 'latency_ms',
		latencyUnit: 'ms',
		statusField: 'status',
		methodField: 'method',
		pathField: 'path',
		levelField: 'level',
		clientIpField: 'client_ip',
		textPattern: '',
		levelPattern: '(?i)\\b(error|err|fatal|panic|warn|warning|oomkilled)\\b',
		namespace: 'default',
		okStatuses: [200, 201],
		excludePaths: ['/health', '/healthcheck']
	},
	limits: meta.limits
};

/** Serves the fixtures above for every /api call the page makes. */
export async function mockApi(page: Page) {
	await page.route('**/api/**', async (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname;
		const json = (body: unknown) =>
			route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

		if (path === '/api/meta') return json(meta);
		if (path === '/api/identity') return json(identity);
		if (path === '/api/health') return json({ ok: true, credentials: true });
		if (path === '/api/config') return json(config);

		const pageMatch = path.match(/^\/api\/page\/(.+)$/);
		if (pageMatch) {
			const panels = pages[pageMatch[1]] ?? [];
			return json({ window, panels });
		}

		if (path.startsWith('/api/discovery/')) {
			const kind = path.split('/').pop() ?? '';
			return json({ kind, resources: discoveries[kind] ?? discoveries.clusters });
		}

		if (path === '/api/logfmt/preview') {
			return json({
				parsed: {
					timestamp: '2026-08-10T07:12:04Z',
					app: 'stress',
					pod: LONG_POD,
					namespace: 'default',
					container: 'stress',
					stream: 'stdout',
					method: 'GET',
					path: LONG_PATH,
					clientIp: '10.0.3.123',
					status: 503,
					latencyMs: 12.5,
					level: '',
					message: '{"app":"stress"}',
					hasAccess: true
				},
				matched: true,
				badStatus: true,
				excluded: false
			});
		}

		return json({});
	});
}

export const PAGES = [
	{ path: '/', name: 'overview' },
	{ path: '/logs/pod', name: 'pod-logs' },
	{ path: '/logs/waf', name: 'waf' },
	{ path: '/infra/targetgroup', name: 'targetgroup' },
	{ path: '/infra/kubernetes', name: 'kubernetes' },
	{ path: '/infra/database', name: 'database' },
	{ path: '/settings', name: 'settings' }
];
