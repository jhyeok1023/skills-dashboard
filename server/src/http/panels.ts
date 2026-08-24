// 패널과 페이지 엔드포인트. internal/api/server.go 의 handlePanel·handlePage
// 이식이다.

import type { Context } from 'hono';

import type { Config } from '../contract.ts';
import { newPayload, Panel, validatePayload } from '../domain/series.ts';
import { resolveWindow } from '../domain/window.ts';
import { logger } from '../log.ts';
import type { Service } from '../service.ts';
import { requireAWS } from '../service.ts';
import {
	buildPodErrorPanel,
	buildPodLatencyPanel,
	buildPodStatusBreakdownPanel,
	buildPodStatusCodePanel,
	buildWAFBlockedPanel,
	buildWAFBreakdownPanel,
	buildWAFTrafficPanel
} from './panels-logs.ts';
import {
	buildCountsPanel,
	buildPodStatusPanel,
	buildRDSProxyPanel,
	buildTargetGroupPanel,
	buildWAFMetricsPanel,
	nodeResourcePanel,
	podResourcePanel,
	type PanelBuilder,
	type RequestCtx
} from './panels-metrics.ts';
import { badRequest, fail, json, upstream } from './respond.ts';

/**
 * panelBuilders 는 패널 id 를 그것을 조립하는 함수로 잇는다. 패널 엔드포인트와
 * 페이지 엔드포인트가 둘 다 이름으로 여기서 찾으므로, 그것이 둘이 같은 것을
 * 낸다는 보장이다.
 */
export function panelBuilders(service: Service): Map<string, PanelBuilder> {
	return new Map<string, PanelBuilder>([
		['targetgroup', (rc) => buildTargetGroupPanel(service, rc)],
		['pod-cpu', podResourcePanel(service, 'pod-cpu', '팟 CPU 사용률', 'pod.cpu')],
		['pod-mem', podResourcePanel(service, 'pod-mem', '팟 메모리 사용률', 'pod.mem')],
		['node-cpu', nodeResourcePanel(service, 'node-cpu', '노드 CPU 사용률', 'node.cpu')],
		['node-mem', nodeResourcePanel(service, 'node-mem', '노드 메모리 사용률', 'node.mem')],
		['node-disk', nodeResourcePanel(service, 'node-disk', '노드 디스크 사용률', 'node.fs')],
		['counts', (rc) => buildCountsPanel(service, rc)],
		['pod-status', (rc) => buildPodStatusPanel(service, rc)],
		['rds-proxy', (rc) => buildRDSProxyPanel(service, rc)],
		['waf-metrics', (rc) => buildWAFMetricsPanel(service, rc)],
		['pod-latency', (rc) => buildPodLatencyPanel(service, rc)],
		['pod-status-codes', (rc) => buildPodStatusCodePanel(service, rc)],
		['pod-status-breakdown', (rc) => buildPodStatusBreakdownPanel(service, rc)],
		['pod-errors', (rc) => buildPodErrorPanel(service, rc)],
		['waf-traffic', (rc) => buildWAFTrafficPanel(service, rc)],
		['waf-blocked', (rc) => buildWAFBlockedPanel(service, rc)],
		['waf-breakdown', (rc) => buildWAFBreakdownPanel(service, rc)]
	]);
}

/**
 * pageBudget 은 페이지 요청 하나를 묶는다(ms). 오래 걸리는 페이지가 끊어진 연결이
 * 아니라 경고로 낮아지게 한다.
 */
const pageBudgetMs = 90_000;

/** 화면마다 어떤 패널을 보여 주는지. */
export const pages = new Map<string, string[]>([
	[
		'overview',
		['pod-latency', 'pod-status-codes', 'targetgroup', 'counts', 'pod-status', 'waf-traffic']
	],
	['pod-logs', ['pod-latency', 'pod-status-codes', 'pod-status-breakdown', 'pod-errors']],
	['waf', ['waf-traffic', 'waf-blocked', 'waf-breakdown', 'waf-metrics']],
	['targetgroup', ['targetgroup']],
	[
		'kubernetes',
		['pod-cpu', 'pod-mem', 'node-cpu', 'node-mem', 'node-disk', 'counts', 'pod-status']
	],
	['database', ['rds-proxy']]
]);

const namespaceRe = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

/**
 * requestConfig 는 저장된 설정에 이 요청만의 네임스페이스를 얹는다.
 *
 * `*` 는 네임스페이스 필터를 끈다. 그 밖의 값은 쿠버네티스 이름 규칙을 지켜야
 * 한다 — 이 값이 쿼리 문자열에 끼워 넣어지기 때문이다.
 */
export function requestConfig(service: Service, url: URL): Config {
	const cfg = service.store.get();
	cfg.logFormat.namespace = cfg.namespace;

	if (!url.searchParams.has('namespace')) return cfg;
	const namespace = (url.searchParams.get('namespace') ?? '').trim();
	if (namespace === '*') {
		cfg.logFormat.namespace = '';
		return cfg;
	}
	if (namespace.length > 63 || !namespaceRe.test(namespace)) {
		throw new Error(`namespace "${namespace}"는 올바른 Kubernetes namespace 이름이 아닙니다`);
	}
	cfg.logFormat.namespace = namespace;
	return cfg;
}

/**
 * warnPanel 은 만들지 못한 패널의 자리를 대신한다. 나머지 페이지가 그대로 그려지고,
 * 그 빈자리가 왜 거기 있는지 말하게 하려는 것이다.
 */
function warnPanel(id: string, err: unknown): Panel {
	const panel = new Panel(id, id);
	panel.warn(err instanceof Error ? err.message : String(err));
	return panel;
}

/**
 * 요청이 중단됐는지 보는 신호. Go 는 r.Context() 로 알았고, Hono 의 요청은 표준
 * Request 이므로 같은 것이 signal 에 있다.
 */
function abortSignalOf(c: Context): AbortSignal {
	return c.req.raw.signal;
}

function windowAndConfig(service: Service, url: URL, signal: AbortSignal): RequestCtx {
	return {
		signal,
		w: resolveWindow(
			service.now(),
			url.searchParams.get('range') ?? '',
			url.searchParams.get('period') ?? ''
		),
		cfg: requestConfig(service, url)
	};
}

export async function handlePanel(service: Service, c: Context): Promise<Response> {
	const denied = requireAWS(service);
	if (denied !== null) return fail(503, denied.error, denied.hint);

	const id = c.req.param('id') ?? '';
	const build = panelBuilders(service).get(id);
	if (build === undefined) return badRequest(new Error(`unknown panel "${id}"`));

	let rc: RequestCtx;
	try {
		rc = windowAndConfig(service, new URL(c.req.url), abortSignalOf(c));
	} catch (err) {
		return badRequest(err);
	}

	const payload = newPayload(rc.w);
	try {
		payload.add(await build(rc));
	} catch (err) {
		// 브라우저가 떠난 것은 실패가 아니다. 그릴 상대가 없으므로 아무것도
		// 쓰지 않는다.
		if (rc.signal.aborted) return new Response(null, { status: 499 });
		return upstream(err);
	}
	return finish(payload);
}

export async function handlePage(service: Service, c: Context): Promise<Response> {
	const denied = requireAWS(service);
	if (denied !== null) return fail(503, denied.error, denied.hint);

	const id = c.req.param('id') ?? '';
	const ids = pages.get(id);
	if (ids === undefined) return badRequest(new Error(`unknown page "${id}"`));

	const clientSignal = abortSignalOf(c);

	// 페이지는 Logs Insights 쿼리 한 파도 위에 앉아 있고, 서버 자신의 응답 상한을
	// 넘길 수 있다. 이 시한이 먼저 떨어지고 핸들러 *안에서* 떨어진다. 아직 도는
	// 쿼리는 취소되고, noteQueryErrors 가 그것을 패널별 경고로 바꾸며, 끝난 패널은
	// 그대로 그려진다. 느린 쿼리를 이름으로 말하는 페이지가 아무 말도 없는
	// 페이지보다 낫다.
	//
	// 예산은 패널마다가 아니라 파도 하나를 덮으므로, 합이 아니라 가장 느린 패널을
	// 묶는다.
	const budget = AbortSignal.any([clientSignal, AbortSignal.timeout(pageBudgetMs)]);

	let rc: RequestCtx;
	try {
		rc = windowAndConfig(service, new URL(c.req.url), budget);
	} catch (err) {
		return badRequest(err);
	}

	const panels = await buildPanels(service, rc, id, ids);

	// 예산이 만료된 것과 브라우저가 떠난 것은 다르다. 앞의 것에는 그릴 만한
	// 부분 패널이 남아 있고, 뒤의 것에는 그려 줄 상대가 없다.
	if (clientSignal.aborted) return new Response(null, { status: 499 });

	const payload = newPayload(rc.w);
	for (const panel of panels) {
		if (panel !== null) payload.add(panel);
	}
	return finish(payload);
}

/**
 * buildPanels 는 페이지의 모든 패널을 한꺼번에 만들고 요청된 순서로 돌려준다.
 *
 * 예전에는 하나씩 차례로 만들었고, 패널마다 제 Logs Insights 파도 위에 앉으므로
 * — 겹치는 것은 패널 *안의* 쿼리뿐이었다 — 네 패널짜리 페이지가 가장 긴 파도
 * 하나면 될 것을 네 파도의 합만큼 썼다. WAF 페이지가 가장 심했다. 그 메트릭
 * 패널은 1초도 안 걸리는 GetMetricData 하나인데, 누가 그것을 보기까지 Insights
 * 파도 셋을 기다렸다.
 *
 * 빌더가 건드리는 것 중 공유되는 가변 상태는 없다. RequestCtx 는 신호와 창과
 * 설정 스냅샷을 나르고 전부 읽기 전용이다. 캐시는 single-flight 라 같은 질문을
 * 하는 두 패널이 경쟁하지 않고 호출 하나로 접힌다. InsightsRunner 는 제 세마포어를
 * 들고 있으므로 여기서 동시 실행 상한을 넘기지 않고 줄을 선다.
 *
 * 결과는 끝나는 대로 덧붙이는 대신 고정된 자리에 떨어진다. 프론트는
 * payload.panels 를 배열 순서대로 그리고 넓은 것을 그 주위에 배치하므로, 어느
 * 패널이 경주에서 이겼든 응답은 pages 가 선언한 순서를 지켜야 한다.
 */
async function buildPanels(
	service: Service,
	rc: RequestCtx,
	page: string,
	ids: string[]
): Promise<(Panel | null)[]> {
	const pageStarted = Date.now();

	// 브라우저가 이미 놓아 버린 요청이 유료 Insights 스캔의 파도를 열어서는 안
	// 된다. 차례로 만들 때는 패널 사이에서 이것을 확인했다. 한꺼번에 만든다는
	// 것은 파도 앞에서 한 번, 그리고 각 작업 안에서 다시 확인한다는 뜻이다 —
	// 취소는 파도가 시작된 뒤에도 떨어질 수 있다.
	if (rc.signal.aborted) return [];

	const builders = panelBuilders(service);
	const results = await Promise.all(
		ids.map(async (pid): Promise<Panel | null> => {
			const build = builders.get(pid);
			if (build === undefined) return null;
			if (rc.signal.aborted) return null;

			const started = Date.now();
			try {
				const panel = await build(rc);
				logger.debug('panel built', { page, panel: pid, ms: Date.now() - started, failed: false });
				return panel;
			} catch (err) {
				logger.debug('panel built', { page, panel: pid, ms: Date.now() - started, failed: true });
				logger.warn('panel failed', {
					panel: pid,
					error: err instanceof Error ? err : String(err)
				});
				return warnPanel(pid, err);
			}
		})
	);

	logger.debug('page built', { page, panels: ids.length, ms: Date.now() - pageStarted });
	return results;
}

/**
 * finish 는 나가기 전에 payload 를 검증한다. 시간축과 어긋난 시리즈는, 그렇지
 * 않았다면 그럴듯해 보이지만 밀린 데이터로 그려졌을 버그다.
 */
function finish(payload: ReturnType<typeof newPayload>): Response {
	try {
		validatePayload(payload);
	} catch (err) {
		logger.error('payload failed validation', { error: err instanceof Error ? err : String(err) });
		return fail(500, err);
	}
	return json(200, payload);
}
