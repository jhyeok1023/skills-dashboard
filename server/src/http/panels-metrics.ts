// 메트릭에서 만드는 패널. internal/api/panels_metrics.go 의 이식이다.

import type { Clients } from '../aws/client.ts';
import { clusterNodeScaling, type NodeScaling } from '../aws/discovery.ts';
import {
	MetricFetcher,
	resultKey,
	sortSeries,
	toSeries as projectSeries,
	type MetricRequest,
	type MetricSeries
} from '../aws/metrics.ts';
import type { Config, Point, Stat, Unit } from '../contract.ts';
import {
	countMetrics,
	nodeResourceMetrics,
	podResourceMetrics,
	podStatusMetrics,
	rdsProxyMetrics,
	specsWithPrefix,
	subjectColor,
	targetGroupMetrics,
	wafMetrics,
	type MetricSpec
} from '../domain/catalog.ts';
import { friendlyTargetGroupName } from '../domain/dimensions.ts';
import { dashSolid, Panel, Series, variantDash } from '../domain/series.ts';
import { buckets, periodSeconds, type Window } from '../domain/window.ts';
import type { AWSConn } from '../connect.ts';
import type { Service } from '../service.ts';

/**
 * 패널 빌더가 읽는 요청 단위 상태. 창은 가장자리에서 한 번 풀려 아래로 넘어오므로,
 * 어느 빌더도 다른 시계에 스스로를 묶을 수 없다.
 */
export interface RequestCtx {
	signal: AbortSignal;
	w: Window;
	cfg: Config;
	/**
	 * 이 요청이 쓰는 AWS 연결. 창과 같은 이유로 여기 실려 내려온다 — 설정
	 * 화면에서 저장한 키가 한 페이지의 두 패널 사이에 끼어들어 둘이 서로 다른
	 * 계정을 설명하게 두지 않는다.
	 */
	aws: AWSConn;
}

export type PanelBuilder = (rc: RequestCtx) => Promise<Panel>;

/** filterSet 은 이름 붙은 차원 값 조합 하나다. */
export interface FilterSet {
	id: string;
	label: string;
	filters: Record<string, string>;
}

/**
 * metricCacheKey 는 창과 모든 요청을 안정된 문자열로 접는다. 창이 키의 일부이므로,
 * 캐시된 값이 그것을 가져온 구간과 다른 구간에 나갈 수 없다.
 */
export function metricCacheKey(name: string, w: Window, reqs: MetricRequest[]): string {
	const head = `metrics|${name}|${Math.trunc(w.start / 1000)}|${Math.trunc(w.end / 1000)}|${periodSeconds(w.period)}|`;
	const parts = reqs.map((r) => {
		let f = '';
		for (const k of Object.keys(r.filters).sort()) f += `${k}=${r.filters[k]};`;
		return `${resultKey(r)}@${r.spec.stat}{${f}}`;
	});
	parts.sort();
	return head + parts.join('|');
}

/** fetchMetrics 는 캐시 뒤에서 메트릭을 가져온다. */
export async function fetchMetrics(
	service: Service,
	rc: RequestCtx,
	api: Clients['cw'],
	name: string,
	reqs: MetricRequest[]
): Promise<Map<string, MetricSeries[]>> {
	if (reqs.length === 0) return new Map();
	const key = metricCacheKey(name, rc.w, reqs);
	return service.cache.do(key, rc.signal, () =>
		new MetricFetcher(api).fetch(rc.w, reqs, rc.signal)
	);
}

/**
 * toSeriesList 는 가져온 결과를 창에 투영하고, 범례에서 가장 바쁜 시리즈가 먼저
 * 오도록 정렬한다.
 *
 * 아무것도 버리지 않는다. 이전 구현의 차트는 상위 여섯 시리즈만 조용히 그린 뒤
 * 그 부분집합에서 축을 다시 계산했다. 축이 패널이 보여 준다고 주장하는 것보다
 * 적은 데이터를 설명하고 있었다.
 *
 * styleFor 는 스펙의 색을 덮어쓰고 선에 무늬를 준다. 스펙 자신의 색이 답인
 * 곳에서는 넘기지 않는다 — WAF 패널의 차단 선이 systemPink 인 것은 차단이라는
 * 뜻이기 때문이고, 시리즈별 팔레트는 그것을 부순다. 스펙 하나가 팟이나 노드마다
 * 선 하나로 퍼져 색이 대신 주체를 가리켜야 하는 패널에서만 넘긴다.
 */
export function toSeriesList(
	w: Window,
	list: MetricSeries[],
	spec: MetricSpec,
	labelFor: ((m: MetricSeries) => string) | null,
	styleFor: ((m: MetricSeries) => { color: string; dash: string }) | null
): Series[] {
	const out = list.map((m) => {
		const label = labelFor === null ? spec.label : labelFor(m);
		const style = styleFor === null ? { color: spec.color, dash: dashSolid } : styleFor(m);
		const s = projectSeries(m, w, label, spec.unit, style.color);
		s.dash = style.dash;
		return s;
	});

	// 값이 큰 순서, 동점이면 라벨 순. 정의된 표본이 하나도 없는 시리즈는 뒤로.
	return out
		.map((s, i) => ({ s, i, max: s.max() }))
		.sort((x, y) => {
			if (x.max === null && y.max === null) return cmp(x.s.label, y.s.label) || x.i - y.i;
			if (x.max === null) return 1;
			if (y.max === null) return -1;
			if (x.max !== y.max) return y.max - x.max;
			return cmp(x.s.label, y.s.label) || x.i - y.i;
		})
		.map((e) => e.s);
}

function cmp(a: string, b: string): number {
	return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * reduceAcross 는 모든 시리즈의 축약에 f 를 적용해 극값을 낸다. 팟이 여럿인
 * 패널이 대표 숫자 하나를 내는 방법이다.
 */
export function reduceAcross(
	list: Series[],
	reduce: (s: Series) => Point,
	pick: (a: number, b: number) => number
): Point {
	let acc: number | null = null;
	for (const s of list) {
		const v = reduce(s);
		if (v === null) continue;
		acc = acc === null ? v : pick(acc, v);
	}
	return acc;
}

export const maxOf = (a: number, b: number): number => (a > b ? a : b);
export const minOf = (a: number, b: number): number => (a < b ? a : b);
export const addOf = (a: number, b: number): number => a + b;

export const seriesMax = (s: Series): Point => s.max();
export const seriesMin = (s: Series): Point => s.min();
export const seriesSum = (s: Series): Point => s.sum();
export const seriesLast = (s: Series): Point => s.last();

/**
 * setSeriesLabel 은 여러 필터 집합으로 그리는 패널의 시리즈 하나에 이름을 준다:
 * 메트릭 이름에, 차트에 집합이 둘 이상이면 집합 이름을 앞에, 집합 하나가 여러
 * 시리즈에 맞았으면 CloudWatch 자신의 라벨을 뒤에 붙인다.
 *
 * 뒤에 붙는 것이 중복을 가른다. 필터 집합 하나는 보통 시리즈 하나를 내지만,
 * SEARCH 스키마가 차원 하나를 고정하지 않으면 여럿을 낸다 — 로드밸런서 둘에
 * 등록된 타겟 그룹, 규칙에 걸친 WAF 메트릭 — 그러면 집합 이름이 그 모두에 똑같이
 * 붙는다. CloudWatch 의 라벨은 실제로 맞은 차원을 싣고 있다. 가릴 것이 있을 때만
 * 붙이면 흔한 범례는 짧게 남는다.
 */
export function setSeriesLabel(
	sets: FilterSet[],
	fs: FilterSet,
	spec: MetricSpec,
	n: number
): (m: MetricSeries) => string {
	const label = sets.length > 1 ? `${fs.label} · ${spec.label}` : spec.label;
	const ambiguous = n > 1;
	return (m) => (ambiguous && m.label !== '' ? `${label} (${m.label})` : label);
}

/**
 * sumSeries 는 여러 시리즈를 칸마다 더해 하나로 접는다. 개별 시리즈가 그 자체로
 * 흥미롭지 않은 곳에 쓴다 — 이를테면 서비스 전체의 팟 수.
 */
export function sumSeries(
	w: Window,
	list: Series[],
	label: string,
	unit: Unit,
	color: string
): Series {
	const out = new Series(label, unit, color, buckets(w));
	for (const s of list) {
		s.values.forEach((v, i) => {
			if (v !== null) out.add(i, v);
		});
	}
	return out;
}

/**
 * metricRequests 는 필터 집합마다 스펙 하나를 펼치고, 결과 키를 갈라 둔다.
 * 타겟 그룹이나 프록시가 여럿이어도 서로 분리된 채로 남게 하려는 것이다.
 */
export function metricRequests(specs: MetricSpec[], sets: FilterSet[]): MetricRequest[] {
	const out: MetricRequest[] = [];
	for (const spec of specs) {
		for (const fs of sets) {
			out.push({ key: `${spec.key}|${fs.id}`, spec, filters: fs.filters });
		}
	}
	return out;
}

/**
 * targetGroupFilterSets 는 선택된 타겟 그룹마다 필터 집합 하나를 만든다.
 *
 * LoadBalancer 차원을 cfg.loadBalancer 로 고정하지 않는 것은 의도다. SEARCH
 * 스키마가 이미 그것을 싣고 있고 — {AWS/ApplicationELB,LoadBalancer,TargetGroup}
 * — 매치를 ALB 자신의 메트릭이 아니라 대상별 메트릭으로 좁히는 것이 그 스키마다.
 * 값 항을 더하면 더 좁아질 뿐이다. 타겟 그룹 차원은 전역적으로 유일하므로 더
 * 좁혀서 얻는 것이 없고, 잃는 것은 실재한다. 앱마다 타겟 그룹 하나가 ALB 여럿에
 * 흩어져 있으면 cfg.loadBalancer 에 없는 그룹은 어떤 메트릭에도 맞지 않아 납작한
 * 빈 시리즈로 그려지고, 그것은 "이 앱에 트래픽이 없었다" 로 읽힌다.
 */
export function targetGroupFilterSets(cfg: Config): FilterSet[] {
	const out: FilterSet[] = cfg.targetGroups.map((tg) => ({
		id: tg,
		label: friendlyTargetGroupName(lastSegmentName(tg)),
		filters: { TargetGroup: tg }
	}));
	if (out.length === 0 && cfg.loadBalancer !== '') {
		out.push({
			id: cfg.loadBalancer,
			label: cfg.loadBalancer,
			filters: { LoadBalancer: cfg.loadBalancer }
		});
	}
	return out;
}

/**
 * lastSegmentName 은 타겟 그룹 차원에서 사람이 읽는 이름을 꺼낸다:
 * targetgroup/k8s-default-product-abc/def -> k8s-default-product-abc
 */
export function lastSegmentName(dimension: string): string {
	const parts = dimension.split('/');
	return parts.length >= 2 ? (parts[1] as string) : dimension;
}

export async function buildTargetGroupPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('targetgroup', '타겟 그룹');

	const sets = targetGroupFilterSets(rc.cfg);
	if (sets.length === 0) {
		panel.warn('타겟 그룹이 선택되지 않았습니다. 설정에서 대상을 선택하세요.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const specs = targetGroupMetrics();
	const results = await fetchMetrics(
		service,
		rc,
		clients.cw,
		'targetgroup',
		metricRequests(specs, sets)
	);

	const byKey = new Map<string, Series[]>();
	for (const spec of specs) {
		for (const fs of sets) {
			const list = results.get(`${spec.key}|${fs.id}`) ?? [];
			sortSeries(list);
			const series = toSeriesList(rc.w, list, spec, setSeriesLabel(sets, fs, spec, list.length), null);
			panel.series.push(...series);
			byKey.set(spec.key, [...(byKey.get(spec.key) ?? []), ...series]);
		}
	}

	const of = (key: string): Series[] => byKey.get(key) ?? [];
	panel.stats = [
		{
			key: 'tg.p99.max',
			label: '최대 응답 시간 p99',
			value: reduceAcross(of('tg.p99'), seriesMax, maxOf),
			unit: 's',
			basis: 'TargetResponseTime p99, 선택 구간 전체',
			intent: 'neutral'
		},
		{
			key: 'tg.4xx.total',
			label: '대상 4xx',
			value: reduceAcross(of('tg.4xx'), seriesSum, addOf),
			unit: 'count',
			basis: 'HTTPCode_Target_4XX_Count Sum',
			intent: 'warn'
		},
		{
			key: 'tg.5xx.total',
			label: '대상 5xx',
			value: reduceAcross(of('tg.5xx'), seriesSum, addOf),
			unit: 'count',
			basis: 'HTTPCode_Target_5XX_Count Sum',
			intent: 'bad'
		},
		{
			key: 'tg.requests.total',
			label: '요청 수',
			value: reduceAcross(of('tg.requests'), seriesSum, addOf),
			unit: 'count',
			basis: 'RequestCount Sum'
		}
	];
	return panel;
}

/**
 * dropSilent 는 창 안에 표본이 하나도 없는 결과를 걷어내고, 남은 것과 몇 개가
 * 나갔는지를 돌려준다.
 *
 * SEARCH 는 요청한 구간의 데이터가 아니라 CloudWatch 의 메트릭 색인에 맞는데,
 * 그 색인은 마지막 데이터포인트 뒤로도 2주쯤 메트릭을 들고 있다. 그래서 클러스터가
 * 한 번이라도 굴린 모든 노드가 돌아오고 — 다시 만든 클러스터는 매번 새 InstanceId
 * 로 답한다 — 각각이 전부 구멍인 선을 그리고, 범례 한 줄을 차지하고, 패널의
 * "노드 N개" 에 셈해졌다. 살아 있는 노드 하나가 열둘로 읽혔다.
 *
 * 퍼지는 패널만 이것을 부른다. 운영자가 고른 주체 — 타겟 그룹, RDS 프록시 — 에서는
 * 빈 시리즈가 그들의 질문에 대한 답이므로 차트에 남아야 한다.
 */
export function dropSilent(list: MetricSeries[]): { live: MetricSeries[]; gone: number } {
	const live = list.filter((m) => m.points.size > 0);
	return { live, gone: list.length - live.length };
}

/**
 * subjectIndex 는 패널이 그리는 주체에 번호를 매긴다. 색을 스펙 목록 안의 위치가
 * 아니라 팟이나 노드로 찾을 수 있게 하려는 것이다.
 *
 * 번호는 스펙을 가로지른 합집합에 대해 라벨 순으로 매긴다. 두 가지를 얻는다.
 * 한 팟의 CPU 선과 그 팟의 limit 대비 선이 같은 색이다 — 같은 팟이기 때문이다.
 * 그리고 다음 폴링이 시리즈 순서를 바꿔도 색이 옮겨 다니지 않는다 — toSeriesList
 * 가 값으로 정렬하므로 가장 바쁜 팟은 계속 자리를 바꾼다.
 */
export function subjectIndex(live: MetricSeries[][]): Map<string, number> {
	const labels: string[] = [];
	const seen = new Set<string>();
	for (const list of live) {
		for (const m of list) {
			if (!seen.has(m.label)) {
				seen.add(m.label);
				labels.push(m.label);
			}
		}
	}
	labels.sort();
	return new Map(labels.map((l, i) => [l, i]));
}

async function buildResourcePanel(
	service: Service,
	rc: RequestCtx,
	id: string,
	title: string,
	specs: MetricSpec[],
	filters: Record<string, string>,
	noun: string
): Promise<Panel> {
	const panel = new Panel(id, title);
	if (rc.cfg.clusterName === '') {
		panel.warn('클러스터가 선택되지 않았습니다. 설정에서 EKS 클러스터를 선택하세요.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const sets: FilterSet[] = [{ id: 'cluster', label: rc.cfg.clusterName, filters }];
	const results = await fetchMetrics(service, rc, clients.cw, id, metricRequests(specs, sets));

	// 가져온 결과를 두 번 훑는다. 색도 개수도 스펙 하나가 아니라 패널 전체에
	// 달려 있기 때문이다.
	let empty = true;
	const live: MetricSeries[][] = [];
	const gone: number[] = [];
	for (const spec of specs) {
		const list = results.get(`${spec.key}|cluster`) ?? [];
		// 필터보다 앞인 것은 의도다. 아무것도 publish 하지 않는 클러스터와,
		// 맞은 것이 옛 노드뿐인 클러스터는 다른 사실이고, Container Insights 에
		// 대한 것은 앞의 하나뿐이다.
		if (list.length > 0) empty = false;
		const dropped = dropSilent(list);
		sortSeries(dropped.live);
		live.push(dropped.live);
		gone.push(dropped.gone);
	}

	const subject = subjectIndex(live);
	specs.forEach((spec, i) => {
		const dash = variantDash(i);
		const series = toSeriesList(
			rc.w,
			live[i] as MetricSeries[],
			spec,
			(m) => `${m.label} · ${spec.label}`,
			(m) => ({ color: subjectColor(subject.get(m.label) ?? 0), dash })
		);
		panel.series.push(...series);

		let basis = `${spec.metricName} ${spec.stat}, ${noun} ${series.length}개`;
		const missing = gone[i] ?? 0;
		if (missing > 0) {
			// 말하되 패널 경고가 아니라 basis 로 말한다. 경고는 카드 전체를
			// 붉게 만들고, 지난주에 다시 만든 클러스터는 잘못한 것 없이 그것을
			// 영구히 켜 둔다.
			basis += ` (구간 내 데이터 없음 ${missing}개 제외)`;
		}
		panel.stats.push({
			key: `${spec.key}.max`,
			label: `최대 ${spec.label}`,
			value: reduceAcross(series, seriesMax, maxOf),
			unit: spec.unit,
			basis,
			...(spec.intent !== undefined ? { intent: spec.intent } : {})
		});
	});

	if (empty) {
		panel.warn(
			'Container Insights 지표가 없습니다. 클러스터에서 Container Insights가 활성화되어 있는지 확인하세요.'
		);
	}
	return panel;
}

/**
 * podResourcePanel 과 nodeResourcePanel 은 주체마다가 아니라 리소스마다 패널
 * 하나를 만든다. "팟 리소스" 패널 하나에 CPU·메모리와 limit 대비 비율 둘을 함께
 * 올리면 팟마다 메트릭마다 선 하나가 되고, 팟 스무 개가 220픽셀 위에 선 여든 개를
 * 그렸다. 접두사로 쪼개는 데 API 비용은 들지 않고 — 같은 스펙을 그대로 가져와
 * 무엇을 재는지로 묶을 뿐이다 — 차트마다 제 축과 제 통계 줄을 얻는다.
 */
export function podResourcePanel(
	service: Service,
	id: string,
	title: string,
	prefix: string
): PanelBuilder {
	return (rc) =>
		buildResourcePanel(
			service,
			rc,
			id,
			title,
			specsWithPrefix(podResourceMetrics(), prefix),
			{ ClusterName: rc.cfg.clusterName, Namespace: rc.cfg.namespace },
			'팟'
		);
}

export function nodeResourcePanel(
	service: Service,
	id: string,
	title: string,
	prefix: string
): PanelBuilder {
	return (rc) =>
		buildResourcePanel(
			service,
			rc,
			id,
			title,
			specsWithPrefix(nodeResourceMetrics(), prefix),
			{ ClusterName: rc.cfg.clusterName },
			'노드'
		);
}

async function nodeScaling(service: Service, rc: RequestCtx): Promise<NodeScaling> {
	const key = `nodescaling|${rc.cfg.clusterName}`;
	return service.cache.do(key, rc.signal, () =>
		clusterNodeScaling(rc.aws.clients?.eks ?? null, rc.cfg.clusterName, rc.signal)
	);
}

export async function buildCountsPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('counts', '팟 · 노드 개수');
	if (rc.cfg.clusterName === '') {
		panel.warn('클러스터가 선택되지 않았습니다.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const specs = countMetrics();
	const sets: FilterSet[] = [
		{ id: 'cluster', label: rc.cfg.clusterName, filters: { ClusterName: rc.cfg.clusterName } }
	];
	const results = await fetchMetrics(service, rc, clients.cw, 'counts', metricRequests(specs, sets));

	// 위치가 아니라 키로 찾는다. 스펙은 목록이고, 거기서 specs[0]/specs[1] 을
	// 읽는다는 것은 끝이 아닌 자리에 메트릭을 더하면 이 둘이 조용히 다른 것을
	// 가리키게 된다는 뜻이다.
	const byKey = new Map(specs.map((s) => [s.key, s]));
	const podSpec = byKey.get('count.pods') as MetricSpec;
	const nodeSpec = byKey.get('count.nodes') as MetricSpec;
	const failedSpec = byKey.get('count.nodes.failed') as MetricSpec;

	const podParts = toSeriesList(
		rc.w,
		results.get('count.pods|cluster') ?? [],
		podSpec,
		(m) => m.label,
		null
	);
	const pods = sumSeries(rc.w, podParts, '실행 중 팟', podSpec.unit, podSpec.color);
	const nodes = toSeriesList(
		rc.w,
		results.get('count.nodes|cluster') ?? [],
		nodeSpec,
		() => nodeSpec.label,
		null
	);
	const failed = toSeriesList(
		rc.w,
		results.get('count.nodes.failed|cluster') ?? [],
		failedSpec,
		() => failedSpec.label,
		null
	);

	panel.series.push(pods, ...nodes, ...failed);

	// 팟: 최소와 최대는 창 안에서 관측된 값이다. CloudWatch 는
	// HorizontalPodAutoscaler 가 설정한 경계를 publish 하지 않고 이 대시보드는
	// AWS API 만 읽으므로, basis 는 이것이 오토스케일러의 한계인 양 굴지 않고
	// "관측값" 이라고 말한다.
	const observed = '관측값 (구간 내 최소/최대)';
	panel.stats.push(
		{
			key: 'pods.current',
			label: '팟 (현재)',
			value: pods.last(),
			unit: 'count',
			basis: 'service_number_of_running_pods 합계'
		},
		{ key: 'pods.min', label: '팟 (최소)', value: pods.min(), unit: 'count', basis: observed },
		{ key: 'pods.max', label: '팟 (최대)', value: pods.max(), unit: 'count', basis: observed }
	);

	panel.stats.push({
		key: 'nodes.current',
		label: '노드 (현재)',
		value: reduceAcross(nodes, seriesLast, maxOf),
		unit: 'count',
		basis: 'cluster_node_count'
	});
	panel.stats.push({
		key: 'nodes.failed',
		label: '실패 노드',
		value: reduceAcross(failed, seriesMax, maxOf),
		unit: 'count',
		basis: 'cluster_failed_node_count Maximum, 구간 최대',
		...(failedSpec.intent !== undefined ? { intent: failedSpec.intent } : {})
	});

	// 노드 경계에는 권위 있는 출처가 있다. 노드 그룹의 스케일링 설정이다.
	// 시작할 때 한 번 붙잡는 대신 요청마다 읽으므로, 다시 스케일한 것이 즉시
	// 반영된다.
	try {
		const scaling = await nodeScaling(service, rc);
		let basis = 'EKS 노드그룹 scalingConfig 합계';
		if (scaling.groups.length > 0) basis += ` (${scaling.groups.join(', ')})`;
		panel.stats.push(
			{ key: 'nodes.min', label: '노드 (최소)', value: scaling.min, unit: 'count', basis },
			{ key: 'nodes.max', label: '노드 (최대)', value: scaling.max, unit: 'count', basis }
		);
	} catch (err) {
		panel.warn(`노드 최소/최대를 읽지 못했습니다: ${err instanceof Error ? err.message : String(err)}`);
	}
	return panel;
}

export async function buildPodStatusPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('pod-status', '팟 상태');
	if (rc.cfg.clusterName === '') {
		panel.warn('클러스터가 선택되지 않았습니다.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const specs = podStatusMetrics();
	const filters: Record<string, string> = { ClusterName: rc.cfg.clusterName };
	if (rc.cfg.namespace !== '') filters['Namespace'] = rc.cfg.namespace;
	const sets: FilterSet[] = [{ id: 'cluster', label: rc.cfg.clusterName, filters }];

	const results = await fetchMetrics(
		service,
		rc,
		clients.cw,
		'pod-status',
		metricRequests(specs, sets)
	);

	let empty = true;
	for (const spec of specs) {
		const list = results.get(`${spec.key}|cluster`) ?? [];
		if (list.length > 0) empty = false;
		const parts = toSeriesList(rc.w, list, spec, (m) => m.label, null);
		const total = sumSeries(rc.w, parts, spec.label, spec.unit, spec.color);
		panel.series.push(total);

		const restarts = spec.key === 'pod.restarts';
		panel.stats.push({
			key: spec.key,
			label: spec.label,
			value: restarts ? total.sum() : total.last(),
			unit: spec.unit,
			basis: restarts ? `${spec.metricName} 합계` : spec.metricName,
			...(spec.intent !== undefined ? { intent: spec.intent } : {})
		});
	}

	// pod_status_* 는 향상된 관측이 켜진 Container Insights 만 publish 하므로,
	// 여기가 비었다는 것에는 사뭇 다른 두 가지 원인이 있고 경고는 실제로 손댈 수
	// 있는 쪽을 짚어야 한다. 그것이 없으면 "실패한 팟이 없다" 와 "이 클러스터는
	// 이 지표를 아예 내지 않는다" 가 똑같아 보인다.
	if (empty) {
		panel.warn(
			'pod_status_* 지표가 없습니다. 이 지표는 Container Insights 확장 관찰성(amazon-cloudwatch-observability 애드온)에서만 게시됩니다.'
		);
	}

	// 이 패널은 OOMKilled 지표를 읽지 않는다. 향상된 관측이 하나 publish 하기는
	// 하지만(pod_container_status_terminated_reason_oom_killed) 사건이 난 뒤에야
	// 나타나므로, 여기 보이는 신호는 재시작 수와 로그 패널의 OOM 수다. 그렇다고
	// 말하는 편이 재시작 수를 OOM 수인 양 암시하는 것보다 낫다.
	panel.warn(
		'CrashLoop은 재시작 증가로, OOM은 팟 로그의 OOMKilled 패턴으로 확인하세요. 이 패널은 OOMKilled 지표를 읽지 않습니다.'
	);
	return panel;
}

export async function buildRDSProxyPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('rds-proxy', 'RDS Proxy 커넥션');
	if (rc.cfg.rdsProxies.length === 0) {
		panel.warn('RDS Proxy가 선택되지 않았습니다. 설정에서 대상을 선택하세요.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const specs = rdsProxyMetrics();
	const sets: FilterSet[] = rc.cfg.rdsProxies.map((p) => ({
		id: p,
		label: p,
		filters: { ProxyName: p }
	}));

	const results = await fetchMetrics(
		service,
		rc,
		clients.cw,
		'rds-proxy',
		metricRequests(specs, sets)
	);

	const byKey = new Map<string, Series[]>();
	for (const spec of specs) {
		for (const fs of sets) {
			const list = results.get(`${spec.key}|${fs.id}`) ?? [];
			sortSeries(list);
			const series = toSeriesList(rc.w, list, spec, setSeriesLabel(sets, fs, spec, list.length), null);
			panel.series.push(...series);
			byKey.set(spec.key, [...(byKey.get(spec.key) ?? []), ...series]);
		}
	}

	const of = (key: string): Series[] => byKey.get(key) ?? [];
	panel.stats = [
		{
			key: 'proxy.db.current',
			label: 'DB 커넥션 (현재)',
			value: reduceAcross(of('proxy.db'), seriesLast, addOf),
			unit: 'conn',
			basis: 'DatabaseConnections Average'
		},
		{
			key: 'proxy.db.max',
			label: 'DB 커넥션 (최대)',
			value: reduceAcross(of('proxy.db'), seriesMax, maxOf),
			unit: 'conn',
			basis: 'DatabaseConnections Average, 구간 최대'
		},
		{
			key: 'proxy.client.current',
			label: '클라이언트 커넥션 (현재)',
			value: reduceAcross(of('proxy.client'), seriesLast, addOf),
			unit: 'conn',
			basis: 'ClientConnections Average'
		},
		{
			key: 'proxy.pinned.max',
			label: '세션 고정 (최대)',
			value: reduceAcross(of('proxy.pinned'), seriesMax, maxOf),
			unit: 'conn',
			basis: 'DatabaseConnectionsCurrentlySessionPinned',
			intent: 'warn'
		},
		{
			key: 'proxy.max.allowed',
			label: '최대 허용 커넥션',
			value: reduceAcross(of('proxy.max'), seriesMax, maxOf),
			unit: 'conn',
			basis: 'MaxDatabaseConnectionsAllowed'
		}
	];
	return panel;
}

export async function buildWAFMetricsPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('waf-metrics', 'WAF 메트릭');
	if (rc.cfg.webAcls.length === 0) {
		panel.warn('Web ACL이 선택되지 않았습니다. 설정에서 대상을 선택하세요.');
		return panel;
	}

	const clients = rc.aws.clients as Clients;
	const specs = wafMetrics();
	const sets: FilterSet[] = rc.cfg.webAcls.map((acl) => ({
		id: acl,
		label: acl,
		filters: { WebACL: acl, Rule: 'ALL' }
	}));

	// CLOUDFRONT 범위 ACL 은 배포가 어디서 트래픽을 받든 us-east-1 에 publish
	// 한다. 비교하는 것은 클라이언트가 실제로 어느 리전으로 만들어졌는가이지
	// 설정이 기록한 두 값이 아니다 — 설정의 리전은 자격증명에 대한 메모일 뿐
	// 그것을 정하지 않는다.
	const api = clients.wafRegion !== clients.region ? clients.cwGlobal : clients.cw;

	const results = await fetchMetrics(service, rc, api, 'waf-metrics', metricRequests(specs, sets));

	const byKey = new Map<string, Series[]>();
	for (const spec of specs) {
		for (const fs of sets) {
			const list = results.get(`${spec.key}|${fs.id}`) ?? [];
			sortSeries(list);
			const series = toSeriesList(rc.w, list, spec, setSeriesLabel(sets, fs, spec, list.length), null);
			panel.series.push(...series);
			byKey.set(spec.key, [...(byKey.get(spec.key) ?? []), ...series]);
		}
	}

	const allowed = reduceAcross(byKey.get('waf.allowed') ?? [], seriesSum, addOf);
	const blocked = reduceAcross(byKey.get('waf.blocked') ?? [], seriesSum, addOf);
	panel.stats = [
		// 일부러 intent 를 두지 않는다 — buildWAFTrafficPanel 참고. 여기서 차단은
		// 정상 상태이지 올릴 조건이 아니다.
		{
			key: 'waf.allowed.total',
			label: '허용',
			value: allowed,
			unit: 'count',
			basis: 'AWS/WAFV2 AllowedRequests Sum'
		},
		{
			key: 'waf.blocked.total',
			label: '차단',
			value: blocked,
			unit: 'count',
			basis: 'AWS/WAFV2 BlockedRequests Sum'
		}
	];
	if (allowed !== null && blocked !== null && allowed + blocked > 0) {
		panel.stats.push({
			key: 'waf.blocked.rate',
			label: '차단 비율',
			value: (blocked / (allowed + blocked)) * 100,
			unit: '%',
			basis: 'BlockedRequests / (Allowed + Blocked), 메트릭 기준'
		});
	}

	// 같은 페이지가 WAF 로그에서 끌어낸 차단 수치를 함께 싣는다. 둘은 정확히
	// 맞지 않는다 — 로그 전달이 메트릭보다 몇 분 늦는다 — 그래서 각 stat 의 basis
	// 가 출처를 밝히고, 그 차이를 나중에 발견하도록 남겨 두지 않는다.
	panel.warn('이 값은 CloudWatch 메트릭 기준입니다. 로그 기반 통계와는 전달 지연으로 차이가 날 수 있습니다.');
	return panel;
}
