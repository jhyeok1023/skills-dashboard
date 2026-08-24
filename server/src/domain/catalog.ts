// 이 대시보드가 아는 CloudWatch 메트릭 전부. internal/domain/catalog.go 의
// 이식이다.

import type { Intent, Unit } from '../contract.ts';

/**
 * MetricSpec 은 메트릭 하나에 대한 모든 것을 한자리에 못박는다. 어느 네임스페이스와
 * 통계로 읽는지, 어떤 단위를 지니는지, 어떤 색인지.
 *
 * 특히 통계가 호출 지점이 아니라 여기 있어야 한다. 같은 카운터를 한 화면에서는
 * Sum 으로, 다른 화면에서는 Average 로 읽으면 두 화면은 영원히 어긋나고, 어느
 * 쪽에도 그 이유를 짐작할 단서가 없다.
 */
export interface MetricSpec {
	key: string;
	label: string;
	namespace: string;
	metricName: string;
	stat: string;
	unit: Unit;
	color: string;
	intent?: Intent;
	/** SEARCH 식이 묶는 차원 이름. 마지막 것이 개별 시리즈를 가른다. */
	dimensions: string[];
}

// CloudWatch 네임스페이스.
export const nsApplicationELB = 'AWS/ApplicationELB';
export const nsContainer = 'ContainerInsights';
export const nsRDS = 'AWS/RDS';
export const nsWAFV2 = 'AWS/WAFV2';

// 통계. 카운터는 더하고, 사용률은 평균하고, 지연은 백분위로 읽는다.
export const statSum = 'Sum';
export const statAvg = 'Average';
export const statMax = 'Maximum';
export const statMin = 'Minimum';
export const statP50 = 'p50';
export const statP90 = 'p90';
export const statP99 = 'p99';

// 애플 시스템 팔레트를 이름으로 참조한다. 실제 oklch 값을 담은 CSS 변수는
// 프론트에 있다. 백엔드는 어떤 의미의 색을 쓸지만 정하므로, 메트릭의 뜻과 색이
// 패널 사이에서 어긋날 수 없다.
export const colorBlue = 'systemBlue';
export const colorGreen = 'systemGreen';
export const colorIndigo = 'systemIndigo';
export const colorOrange = 'systemOrange';
export const colorPink = 'systemPink';
export const colorPurple = 'systemPurple';
export const colorRed = 'systemRed';
export const colorTeal = 'systemTeal';
export const colorYellow = 'systemYellow';
export const colorMint = 'systemMint';
export const colorGray = 'systemGray';

/**
 * subjectPalette 는 스펙 하나가 여러 시리즈로 퍼지는 패널에서 주체 하나 — 팟,
 * 노드 — 에 색을 준다.
 *
 * 스펙 자신의 색은 그 선이 무엇을 재는지를 말하고, 차트가 메트릭당 선 하나를
 * 들 때는 그게 옳은 답이다. SEARCH 가 퍼질 때는 틀린 답이 된다 — CPU 패널의 모든
 * 팟이 colorIndigo 를 가져가서 팟 스무 개가 똑같은 선 스무 개를 그렸고, 범례
 * 글자만이 그것을 가르는 유일한 수단이었다. 거기서는 색이 주체를 실어야 하고,
 * 메트릭은 선 무늬로 옮겨 간다 — variantDash 참고.
 *
 * 순서는 상수 블록의 순서가 아니다. 이웃한 항목이 적록·청황 색각 이상에서도
 * 갈라지도록 배치했다. 팟 셋이 올라간 차트가 어떤 독자에게는 하나로 보이는 색
 * 셋을 내주지 않게 하려는 것이다. colorGray 는 뺐다 — sumSeries 가 이미 합계에
 * 그 색을 쓴다.
 */
export const subjectPalette = [
	colorBlue,
	colorOrange,
	colorGreen,
	colorPink,
	colorPurple,
	colorYellow,
	colorTeal,
	colorRed,
	colorIndigo,
	colorMint
];

/**
 * subjectColor 는 i 번째 주체의 팔레트 항목을 고르고, 팔레트보다 주체가 많으면
 * 돌려 쓴다. 그 뒤로는 선 무늬와 범례가 두 선을 가른다. 호출자는 주체를 결정적인
 * 순서로 늘어놓아 새로고침 때마다 색이 옮겨 다니지 않게 해야 한다.
 */
export function subjectColor(i: number): string {
	const n = i < 0 ? 0 : i;
	return subjectPalette[n % subjectPalette.length] as string;
}

/** 요구사항 3: 대상 응답 시간, 5xx, 4xx. */
export function targetGroupMetrics(): MetricSpec[] {
	const dimensions = ['LoadBalancer', 'TargetGroup'];
	return [
		{ key: 'tg.p50', label: '응답 시간 p50', namespace: nsApplicationELB, metricName: 'TargetResponseTime', stat: statP50, unit: 's', color: colorTeal, dimensions },
		{ key: 'tg.p90', label: '응답 시간 p90', namespace: nsApplicationELB, metricName: 'TargetResponseTime', stat: statP90, unit: 's', color: colorBlue, dimensions },
		{ key: 'tg.p99', label: '응답 시간 p99', namespace: nsApplicationELB, metricName: 'TargetResponseTime', stat: statP99, unit: 's', color: colorIndigo, dimensions },
		{ key: 'tg.4xx', label: '대상 4xx', namespace: nsApplicationELB, metricName: 'HTTPCode_Target_4XX_Count', stat: statSum, unit: 'count', color: colorOrange, intent: 'warn', dimensions },
		{ key: 'tg.5xx', label: '대상 5xx', namespace: nsApplicationELB, metricName: 'HTTPCode_Target_5XX_Count', stat: statSum, unit: 'count', color: colorRed, intent: 'bad', dimensions },
		{ key: 'tg.requests', label: '요청 수', namespace: nsApplicationELB, metricName: 'RequestCount', stat: statSum, unit: 'count', color: colorGray, dimensions },
		{ key: 'tg.healthy', label: '정상 대상', namespace: nsApplicationELB, metricName: 'HealthyHostCount', stat: statAvg, unit: 'count', color: colorGreen, intent: 'good', dimensions }
	];
}

/** 요구사항 4. */
export function podResourceMetrics(): MetricSpec[] {
	const dimensions = ['ClusterName', 'Namespace', 'PodName'];
	return [
		{ key: 'pod.cpu', label: 'CPU 사용률', namespace: nsContainer, metricName: 'pod_cpu_utilization', stat: statAvg, unit: '%', color: colorIndigo, dimensions },
		{ key: 'pod.mem', label: '메모리 사용률', namespace: nsContainer, metricName: 'pod_memory_utilization', stat: statAvg, unit: '%', color: colorTeal, dimensions },
		{ key: 'pod.cpu.limit', label: 'CPU 사용률 (limit 대비)', namespace: nsContainer, metricName: 'pod_cpu_utilization_over_pod_limit', stat: statAvg, unit: '%', color: colorPurple, dimensions },
		{ key: 'pod.mem.limit', label: '메모리 사용률 (limit 대비)', namespace: nsContainer, metricName: 'pod_memory_utilization_over_pod_limit', stat: statAvg, unit: '%', color: colorPink, intent: 'warn', dimensions }
	];
}

/**
 * 요구사항 5.
 *
 * 아무것도 고정하지 않는데도 InstanceId 가 스키마에 있어야 한다. Container
 * Insights 는 노드 메트릭을 {NodeName, ClusterName, InstanceId} 와 {ClusterName}
 * 아래에만 publish 한다. SEARCH 스키마는 집합으로 맞춰지므로 {ClusterName,NodeName}
 * 은 어떤 메트릭에도 맞지 않았고 패널은 비어서 떴다 — Container Insights 가 꺼진
 * 클러스터와 구분되지 않는 모습으로.
 */
export function nodeResourceMetrics(): MetricSpec[] {
	const dimensions = ['ClusterName', 'InstanceId', 'NodeName'];
	return [
		{ key: 'node.cpu', label: 'CPU 사용률', namespace: nsContainer, metricName: 'node_cpu_utilization', stat: statAvg, unit: '%', color: colorIndigo, dimensions },
		{ key: 'node.mem', label: '메모리 사용률', namespace: nsContainer, metricName: 'node_memory_utilization', stat: statAvg, unit: '%', color: colorTeal, dimensions },
		{ key: 'node.fs', label: '디스크 사용률', namespace: nsContainer, metricName: 'node_filesystem_utilization', stat: statAvg, unit: '%', color: colorYellow, dimensions }
	];
}

/** 요구사항 6: 팟과 노드가 몇 개인가. */
export function countMetrics(): MetricSpec[] {
	return [
		{ key: 'count.pods', label: '실행 중 팟', namespace: nsContainer, metricName: 'service_number_of_running_pods', stat: statAvg, unit: 'count', color: colorBlue, dimensions: ['ClusterName', 'Namespace', 'Service'] },
		{ key: 'count.nodes', label: '노드', namespace: nsContainer, metricName: 'cluster_node_count', stat: statAvg, unit: 'count', color: colorGreen, dimensions: ['ClusterName'] },
		{ key: 'count.nodes.failed', label: '실패 노드', namespace: nsContainer, metricName: 'cluster_failed_node_count', stat: statMax, unit: 'count', color: colorRed, intent: 'bad', dimensions: ['ClusterName'] }
	];
}

/**
 * 요구사항 7.
 *
 * PodName 이 스키마에 있는 이유는 이 메트릭 중 어느 것도 {Namespace, ClusterName}
 * 에 publish 되지 않기 때문이다 — 팟 단위 집합은 {PodName, Namespace, ClusterName}
 * 이다. 패널이 퍼진 것을 다시 더해 내리므로 숫자의 뜻은 원래 의도한 그대로다.
 * PodName 이 없으면 아무 뜻도 없었다. SEARCH 가 어떤 시리즈에도 맞지 않았으니까.
 *
 * pod_status_* 는 향상된 관측이 켜진 Container Insights 만 publish 한다.
 * pod_number_of_container_restarts 는 양쪽 다 내므로, 옛 에이전트를 도는
 * 클러스터에서도 이 패널이 무언가는 말할 수 있다.
 */
export function podStatusMetrics(): MetricSpec[] {
	const dimensions = ['ClusterName', 'Namespace', 'PodName'];
	return [
		{ key: 'pod.running', label: 'Running', namespace: nsContainer, metricName: 'pod_status_running', stat: statAvg, unit: 'count', color: colorGreen, intent: 'good', dimensions },
		{ key: 'pod.pending', label: 'Pending', namespace: nsContainer, metricName: 'pod_status_pending', stat: statAvg, unit: 'count', color: colorOrange, intent: 'warn', dimensions },
		{ key: 'pod.failed', label: 'Failed', namespace: nsContainer, metricName: 'pod_status_failed', stat: statAvg, unit: 'count', color: colorRed, intent: 'bad', dimensions },
		{ key: 'pod.restarts', label: '컨테이너 재시작', namespace: nsContainer, metricName: 'pod_number_of_container_restarts', stat: statSum, unit: 'count', color: colorPink, intent: 'bad', dimensions }
	];
}

/** 요구사항 8. */
export function rdsProxyMetrics(): MetricSpec[] {
	const dimensions = ['ProxyName'];
	return [
		{ key: 'proxy.db', label: 'DB 커넥션', namespace: nsRDS, metricName: 'DatabaseConnections', stat: statAvg, unit: 'conn', color: colorPurple, dimensions },
		{ key: 'proxy.client', label: '클라이언트 커넥션', namespace: nsRDS, metricName: 'ClientConnections', stat: statAvg, unit: 'conn', color: colorBlue, dimensions },
		{ key: 'proxy.pinned', label: '세션 고정 커넥션', namespace: nsRDS, metricName: 'DatabaseConnectionsCurrentlySessionPinned', stat: statAvg, unit: 'conn', color: colorOrange, intent: 'warn', dimensions },
		{ key: 'proxy.borrow', label: '커넥션 대기', namespace: nsRDS, metricName: 'DatabaseConnectionsBorrowLatency', stat: statAvg, unit: 'ms', color: colorYellow, dimensions },
		{ key: 'proxy.max', label: '최대 허용 커넥션', namespace: nsRDS, metricName: 'MaxDatabaseConnectionsAllowed', stat: statMax, unit: 'conn', color: colorGray, dimensions }
	];
}

/** 같은 페이지의 로그 기반 통계를 보완하는, WAF 의 메트릭 쪽 시각. */
export function wafMetrics(): MetricSpec[] {
	const dimensions = ['WebACL', 'Rule', 'Region'];
	return [
		// 둘 다 intent 가 없다. 요청을 차단하는 WAF 는 정상 운영 상태이므로,
		// 그것에 표시를 달면 경보가 영구히 켜져 있고 실제로 벗어난 경우가
		// 묻힌다. 색은 여전히 둘을 가른다.
		{ key: 'waf.allowed', label: '허용', namespace: nsWAFV2, metricName: 'AllowedRequests', stat: statSum, unit: 'count', color: colorGreen, dimensions },
		{ key: 'waf.blocked', label: '차단', namespace: nsWAFV2, metricName: 'BlockedRequests', stat: statSum, unit: 'count', color: colorPink, dimensions },
		{ key: 'waf.counted', label: '카운트', namespace: nsWAFV2, metricName: 'CountedRequests', stat: statSum, unit: 'count', color: colorGray, dimensions }
	];
}

/**
 * specsWithPrefix 는 키가 prefix 로 시작하는 스펙을 고른다. CPU 패널이 메모리와
 * 차트를 나눠 쓰지 않게 하려는 것이다. 키가 이미 중첩돼 있으므로 — pod.cpu 가
 * pod.cpu.limit 을 데려간다 — 접두사 하나가 리소스 하나를 가리킨다.
 */
export function specsWithPrefix(specs: MetricSpec[], prefix: string): MetricSpec[] {
	return specs.filter((s) => s.key.startsWith(prefix));
}

/** 대시보드가 아는 모든 스펙. 키가 유일한지 확인할 때 쓴다. */
export function allMetrics(): MetricSpec[] {
	return [
		...targetGroupMetrics(),
		...podResourceMetrics(),
		...nodeResourceMetrics(),
		...countMetrics(),
		...podStatusMetrics(),
		...rdsProxyMetrics(),
		...wafMetrics()
	];
}

/**
 * searchValueRe 는 차원 값을 SEARCH 식의 큰따옴표 안에 넣어도 안전한 문자로
 * 묶는다.
 */
const searchValueRe = /^[A-Za-z0-9 ._:/+=@-]+$/;

/**
 * searchExpression 은 스펙에 대한 CloudWatch 메트릭 수식 SEARCH 를, 주어진 차원
 * 값으로 걸러서 만든다.
 *
 * SEARCH 를 쓰면 ListMetrics 호출이 통째로 사라지고, 그와 함께 이전 구현의 메트릭
 * 패널을 끝내 죽인 실패도 사라진다. ListMetrics 는 이미 없어진 팟의 차원까지 계속
 * 돌려주므로 생성되는 쿼리 목록이 단조 증가했고, GetMetricData 의 500쿼리 천장을
 * 넘은 순간부터 이후 모든 호출이 검증에서 실패했다. SEARCH 는 몇 개의 시리즈에
 * 맞든 쿼리 하나다.
 */
export function searchExpression(
	spec: MetricSpec,
	filters: Record<string, string>,
	periodSecs: number
): string {
	if (spec.namespace === '' || spec.metricName === '') {
		throw new Error(`metric "${spec.key}" is missing a namespace or name`);
	}
	const schema = [spec.namespace, ...spec.dimensions];

	const terms = [`MetricName=${JSON.stringify(spec.metricName)}`];
	// 키를 정렬한다. 출력이 결정적이어야 쿼리가 캐시되고 테스트가 비교된다.
	for (const k of Object.keys(filters).sort()) {
		const v = filters[k];
		if (v === undefined || v === '') continue;
		if (!searchValueRe.test(k) || !searchValueRe.test(v)) {
			throw new Error(
				`dimension ${k}="${v}" contains characters that cannot be embedded in a SEARCH expression`
			);
		}
		terms.push(`${k}=${JSON.stringify(v)}`);
	}

	return `SEARCH('{${schema.join(',')}} ${terms.join(' ')}', '${spec.stat}', ${periodSecs})`;
}

/** queryIDRe 는 GetMetricData 가 쿼리 식별자로 받아 주는 모양이다. */
const queryIDRe = /^[a-z][a-zA-Z0-9_]*$/;

/**
 * queryID 는 메트릭 키를 쓸 수 있는 GetMetricData id 로 바꾼다.
 *
 * 결과를 스펙에 되맞출 때 위치가 아니라 이 식별자를 쓴다. 이전 구현은 id 를
 * Sscanf 로 훑어 배열 인덱스를 복원하고 오류를 버린 뒤 그 값으로 슬라이스를
 * 인덱싱했다 — 그래서 예상 못 한 id 는 엉뚱한 시리즈에 데이터를 썼고, 범위를
 * 벗어난 id 는 핸들러를 패닉시켰다.
 */
export function queryID(key: string): string {
	let id = 'q';
	for (const ch of key) {
		id += /[A-Za-z0-9]/.test(ch) ? ch : '_';
	}
	// 인쇄 가능한 문자로 된 키에서는 닿지 않는 자리다. 다만 잘못된 id 는
	// 조용히 어긋나는 대신 CloudWatch 에서 거절당하므로, 유효한 것으로 물러난다.
	return queryIDRe.test(id) ? id : 'q';
}
