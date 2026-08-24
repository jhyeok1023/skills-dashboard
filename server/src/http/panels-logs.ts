// 로그에서 만드는 패널. internal/api/panels_logs.go 의 이식이다.

import { createHash } from 'node:crypto';

import type { InsightsRunner, QueryResult } from '../aws/insights.ts';
import { insightsMaxRows, NoLogGroupError, totalBytesScanned } from '../aws/insights.ts';
import type { Column, LogFormat, Row } from '../contract.ts';
import {
	colorBlue,
	colorGray,
	colorGreen,
	colorIndigo,
	colorOrange,
	colorPink,
	colorPurple,
	colorRed,
	colorTeal,
	colorYellow
} from '../domain/catalog.ts';
import { podLogGroupOrDefault } from '../config/config.ts';
import * as q from '../domain/query.ts';
import { newTable, Panel, Series } from '../domain/series.ts';
import { buckets, indexOf, periodSeconds } from '../domain/window.ts';
import type { Service } from '../service.ts';
import { serviceRegion, serviceWAFRegion } from '../service.ts';
import { maxOf, reduceAcross, seriesMax, type RequestCtx } from './panels-metrics.ts';

/**
 * Logs Insights 의 bin() 이나 @timestamp 가 돌아오는 모양. 시간대 표시가 없는
 * UTC 다.
 */
const insightsTimeRe =
	/^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2}(?:\.\d+)?)(Z|[+-]\d{2}:?\d{2})?$/;

export function parseInsightsTime(s: string): Date | null {
	const m = insightsTimeRe.exec(s.trim());
	if (m === null) return null;
	const d = new Date(`${m[1]}T${m[2]}${m[3] ?? 'Z'}`);
	return Number.isNaN(d.getTime()) ? null : d;
}

const decimalRe = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;

export function rowFloat(row: Record<string, string>, key: string): number | null {
	const v = row[key];
	if (v === undefined || v === '') return null;
	const t = v.trim();
	if (!decimalRe.test(t)) return null;
	const f = Number(t);
	return Number.isFinite(f) ? f : null;
}

/**
 * logSource 는 한 패널의 로그 쿼리가 가는 곳이다: 어느 러너로, 어느 리전에서,
 * 어느 그룹에 대해.
 *
 * 셋이 함께 다니는 이유는 캐시 키에 셋 다 필요하기 때문이다. 그룹 이름만으로
 * 키를 잡으면, 서로 다른 두 리전에 같은 이름으로 있는 그룹에 대한 팟 쿼리와 WAF
 * 쿼리가 서로의 행을 내주게 된다.
 */
export interface LogSource {
	runner: InsightsRunner | null;
	region: string;
	group: string;
	/**
	 * 그룹이 비었을 때 운영자에게 할 말. 팟 로그와 WAF 로그는 서로 다른 곳에서
	 * 설정하고, 엉뚱한 쪽을 짚는 것이 운영자가 이미 맞는 설정을 들여다보게 되는
	 * 경위다.
	 */
	missing: string;
}

export function podLogs(service: Service, rc: RequestCtx): LogSource {
	return {
		runner: service.insights,
		region: serviceRegion(service),
		group: podLogGroupOrDefault(rc.cfg),
		missing: '팟 로그 그룹이 설정되지 않았습니다. 설정에서 클러스터 또는 로그 그룹을 지정하세요.'
	};
}

/**
 * wafLogs 는 WAF 패널을 WAF 리전으로 향하게 한다. 전역 러너가 준비되지 않았으면
 * 주 러너로 물러난다.
 */
export function wafLogs(service: Service, rc: RequestCtx): LogSource {
	const region = serviceWAFRegion(service);
	return {
		runner: service.insightsGlobal ?? service.insights,
		region,
		group: rc.cfg.wafLogGroup,
		missing: `WAF 로그 그룹이 설정되지 않았습니다. CLOUDFRONT 스코프 WAF는 ${region}에만 로그를 남기므로, 설정에서 해당 리전의 aws-waf-logs-* 그룹을 지정하세요.`
	};
}

export interface LogRun {
	results: Map<string, QueryResult>;
	errors: Map<string, Error>;
}

/**
 * 한 파도에서 아무것도 돌아오지 않았음을 표시한다. 캐시가 그것을 긴 TTL 대신
 * 짧은 오류 TTL 로 넣게 하려는 것이다. 패널까지 가지 않는다 — runLogQueries 가
 * 쿼리별 오류를 대신 넘긴다.
 */
class AllQueriesFailedError extends Error {
	readonly run: LogRun;
	constructor(run: LogRun) {
		super('every log query failed');
		this.run = run;
	}
}

/** runLogQueries 는 쿼리 묶음을 캐시 뒤에서 돌린다. */
export async function runLogQueries(
	service: Service,
	rc: RequestCtx,
	src: LogSource,
	name: string,
	queries: q.Query[]
): Promise<LogRun> {
	if (src.group === '' || src.runner === null) {
		const errors = new Map<string, Error>();
		for (const query of queries) errors.set(query.id, new NoLogGroupError());
		return { results: new Map(), errors };
	}
	const runner = src.runner;

	const ids = queries
		.map((query) => {
			const sum = createHash('sha256').update(query.text).digest('hex');
			return `${query.id}:${query.limit}:${sum}`;
		})
		.sort();
	const key =
		`logs|${name}|${src.region}|${src.group}|${Math.trunc(rc.w.start / 1000)}|` +
		`${Math.trunc(rc.w.end / 1000)}|${periodSeconds(rc.w.period)}|${ids.join(',')}`;

	try {
		return await service.cache.do<LogRun>(key, rc.signal, async () => {
			const run = await runner.runAll(src.group, rc.w, queries, rc.signal);
			// 아무것도 돌아오지 않은 파도는 실패로 보고한다. 그래야 캐시가 짧은
			// 오류 TTL 아래 넣는다. 여기서 그냥 성공을 돌려주면 — 예전에는 조건
			// 없이 그랬다 — 전멸이 멀쩡한 결과로 기록돼, 만료된 자격증명이나
			// 스로틀링당한 계정이 이미 풀린 뒤에도 TTL 내내 화면에 남았다.
			//
			// 일부 실패는 여전히 성공이다. 답한 쿼리는 지킬 값이 있고, 패널이
			// 나머지를 이미 경고로 보고한다.
			if (queries.length > 0 && run.errors.size === queries.length) {
				throw new AllQueriesFailedError(run);
			}
			return run;
		});
	} catch (err) {
		// 값이 함께 온 실패는 이 함수가 캐시에 보낸 제 신호다. 호출자에게는 그것이
		// 감싸고 있는 쿼리별 오류가 더 쓸모 있다.
		if (err instanceof AllQueriesFailedError) return err.run;

		// 값이 아예 없다는 것은 조회 자체가 실패했다는 뜻이다 — 보통은 취소된
		// 요청 — 그래서 쿼리별로 보고할 것이 없다.
		const errors = new Map<string, Error>();
		const wrapped = err instanceof Error ? err : new Error(String(err));
		for (const query of queries) errors.set(query.id, wrapped);
		return { results: new Map(), errors };
	}
}

/**
 * excludedPaths 는 모든 팟 로그 stat 의 basis 뒤에 붙는 프로브 제외 문구를 만든다.
 *
 * 트래픽의 한 조각을 조용히 버리는 숫자는 그저 틀린 숫자보다 나쁘다. 그런 일이
 * 있었다는 것을 화면의 무엇도 암시하지 않기 때문이다. 값 옆에 제외된 경로를
 * 적는 것이, 헬스 체크가 세어지지 않게 된 뒤에도 "요청 수" 를 정직하게 유지한다.
 */
export function excludedPaths(f: LogFormat): string {
	if (f.excludePaths.length === 0) return '';
	return `, ${f.excludePaths.join(' · ')} 제외`;
}

/**
 * noteQueryCost 는 Insights 쿼리 묶음이 얼마나 스캔했는지 기록한다. Insights 는
 * 바이트로 과금하므로, 새로고침의 값을 청구서에 맡기지 않고 보여 준다.
 */
export function noteQueryCost(panel: Panel, results: Map<string, QueryResult>): void {
	const bytes = totalBytesScanned(results);
	if (bytes <= 0) return;
	panel.stats.push({
		key: 'insights.bytesScanned',
		label: '스캔량',
		value: bytes,
		unit: 'bytes',
		basis: `Logs Insights 쿼리 ${results.size}건`
	});
}

/**
 * noteQueryErrors 는 쿼리별 실패를 패널 경고로 바꾼다. 쿼리 하나가 실패해도
 * 페이지가 계속 그려지게 하려는 것이다.
 */
export function noteQueryErrors(panel: Panel, src: LogSource, errors: Map<string, Error>): void {
	for (const id of [...errors.keys()].sort()) {
		const err = errors.get(id) as Error;
		if (err instanceof NoLogGroupError) {
			panel.warn(src.missing);
			return;
		}
		// 리전을 함께 적는다. 여기서 가장 흔한 실패가, 그룹은 있는데 쿼리가 찾아간
		// 곳에 없는 경우이기 때문이다.
		panel.warn(`${id} 쿼리 실패 (${src.region}): ${err.message}`);
	}
}

/**
 * warnIfTruncated 는 Logs Insights 가 집계를 결과 상한에서 잘랐을 때 그렇다고
 * 말한다.
 *
 * 상한에 걸린 집계는 더 작은 답이 아니라 틀린 답이다. Insights 가 남긴 행은
 * 쿼리가 마침 먼저 정렬한 것들이므로, 거기서 끌어낸 모든 총계가 알 수 없는 만큼
 * 모자란다. 예전에는 아무도 이 플래그를 읽지 않았고, 그래서 잘린 stats 가 대표
 * 숫자로 더해지고 차트로 그려지는 동안 세기를 멈췄다는 말이 화면 어디에도 없었다.
 */
export function warnIfTruncated(
	panel: Panel,
	id: string,
	results: Map<string, QueryResult>
): void {
	const res = results.get(id);
	if (res !== undefined && res.truncated) {
		panel.warn(
			`${id} 집계가 Logs Insights 결과 상한(${insightsMaxRows}행)에 걸려 잘렸습니다. ` +
				'값이 실제보다 작습니다 — 조회 구간을 좁히거나 네임스페이스·제외 경로를 설정하세요.'
		);
	}
}

export async function buildPodLatencyPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('pod-latency', '팟 응답 시간');
	const lq = new q.LogQueries(rc.cfg.logFormat);

	const traffic = q.podTraffic(lq, rc.w);
	const src = podLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'pod-latency', [traffic]);
	noteQueryErrors(panel, src, errors);
	const res = results.get(traffic.id);
	if (res === undefined) return panel;

	// 시리즈는 앱마다 만든다. 앱 하나에 백분위 선 한 벌씩이다.
	interface AppSeries {
		p50: Series;
		p90: Series;
		p99: Series;
		requests: Series;
	}
	const apps = new Map<string, AppSeries>();
	const order: string[] = [];
	let totalRequests = 0;
	let totalLatencySamples = 0;

	for (const row of res.rows) {
		const ts = parseInsightsTime(row['t'] ?? '');
		if (ts === null) continue;
		const idx = indexOf(rc.w, ts);
		if (idx === null) continue;

		const app = row['app'] === undefined || row['app'] === '' ? '(unknown)' : row['app'];
		let a = apps.get(app);
		if (a === undefined) {
			const n = buckets(rc.w);
			a = {
				p50: new Series(`${app} · p50`, 'ms', colorTeal, n),
				p90: new Series(`${app} · p90`, 'ms', colorBlue, n),
				p99: new Series(`${app} · p99`, 'ms', colorIndigo, n),
				requests: new Series(`${app} · 요청 수`, 'count', colorGray, n)
			};
			apps.set(app, a);
			order.push(app);
		}

		const p50 = rowFloat(row, 'p50');
		if (p50 !== null) a.p50.set(idx, p50);
		const p90 = rowFloat(row, 'p90');
		if (p90 !== null) a.p90.set(idx, p90);
		const p99 = rowFloat(row, 'p99');
		if (p99 !== null) a.p99.set(idx, p99);

		const requests = rowFloat(row, 'requests');
		if (requests !== null) {
			a.requests.add(idx, requests);
			totalRequests += requests;
		}
		const samples = rowFloat(row, 'latencySamples');
		if (samples !== null) totalLatencySamples += samples;
	}

	order.sort();
	const p99s: Series[] = [];
	for (const app of order) {
		const a = apps.get(app) as AppSeries;
		panel.series.push(a.p50, a.p90, a.p99, a.requests);
		p99s.push(a.p99);
	}

	// 수치 둘, 이름 둘, 밝힌 모집단 둘.
	//
	// 이전 구현은 상태를 실은 행에서 요청 총계를, 지연을 실은 행에서 "요청 수"
	// 컬럼을 끌어내고 둘을 같은 이름으로 붙여 나란히 보여 줬다. 여기서는 둘이
	// 한 쿼리에서 나오고, 서로 다른 이름을 지키고, 각자 무엇을 셌는지 말한다.
	const excluded = excludedPaths(rc.cfg.logFormat);
	panel.stats.push(
		{
			key: 'pod.p99.max',
			label: '최대 p99',
			value: reduceAcross(p99s, seriesMax, maxOf),
			unit: 'ms',
			basis: `${rc.cfg.logFormat.latencyField} 가 있는 요청, 구간 전체${excluded}`
		},
		{
			key: 'pod.requests.total',
			label: '요청 수',
			value: totalRequests,
			unit: 'count',
			basis: `${rc.cfg.logFormat.statusField} 가 있는 로그 라인${excluded}`
		},
		{
			key: 'pod.latencySamples.total',
			label: '응답 시간 표본 수',
			value: totalLatencySamples,
			unit: 'count',
			basis: `${rc.cfg.logFormat.latencyField} 가 있는 로그 라인${excluded}`
		}
	);
	noteQueryCost(panel, results);
	return panel;
}

export async function buildPodStatusCodePanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('pod-status-codes', '비정상 응답 코드');
	const lq = new q.LogQueries(rc.cfg.logFormat);
	const limit = rc.cfg.limits.logRows;

	const series = q.podBadStatusSeries(lq, rc.w);
	const list = q.podBadStatusList(lq, limit);

	const src = podLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'pod-status-codes', [
		series,
		list
	]);
	noteQueryErrors(panel, src, errors);
	warnIfTruncated(panel, series.id, results);

	// 집계는 상한이 없으므로 그것을 더하면 비정상 응답의 진짜 개수가 나온다.
	// 옆의 목록은 상한이 있다. 목록을 세면 대표 숫자가 상한에서 멈추고 차트와
	// 어긋난다 — 이전 구현이 정확히 그렇게 했다.
	let total = 0;
	const byStatus = new Map<string, Series>();
	const fromSeries = results.get(series.id);
	if (fromSeries !== undefined) {
		for (const row of fromSeries.rows) {
			const ts = parseInsightsTime(row['t'] ?? '');
			if (ts === null) continue;
			const idx = indexOf(rc.w, ts);
			if (idx === null) continue;
			const n = rowFloat(row, 'n');
			if (n === null) continue;

			const code = row['status'] === undefined || row['status'] === '' ? '(none)' : row['status'];
			let s = byStatus.get(code);
			if (s === undefined) {
				s = new Series(code, 'count', statusColor(code), buckets(rc.w));
				byStatus.set(code, s);
			}
			s.add(idx, n);
			total += n;
		}
	}

	for (const code of [...byStatus.keys()].sort()) {
		panel.series.push(byStatus.get(code) as Series);
	}

	const rows: Row[] = [];
	const fromList = results.get(list.id);
	if (fromList !== undefined) {
		for (const r of fromList.rows) {
			rows.push({
				timestamp: r['@timestamp'] ?? '',
				app: r['app'] ?? '',
				pod: r['pod'] ?? '',
				container: r['container'] ?? '',
				namespace: r['namespace'] ?? '',
				method: r['method'] ?? '',
				path: r['path'] ?? '',
				target: r['requestTarget'] ?? '',
				status: r['status'] ?? '',
				latencyMs: r['latencyMs'] ?? '',
				clientIp: r['clientIp'] ?? '',
				userAgent: r['userAgent'] ?? ''
			});
		}
	}

	const cols: Column[] = [
		{ key: 'timestamp', label: '시각', mono: true },
		{ key: 'status', label: '코드', numeric: true, copyable: true },
		{ key: 'method', label: '메소드' },
		{ key: 'target', label: '요청 대상', mono: true, copyable: true },
		{ key: 'latencyMs', label: '응답 시간', unit: 'ms', numeric: true },
		{ key: 'app', label: '앱', copyable: true },
		{ key: 'pod', label: '팟', mono: true, copyable: true },
		{ key: 'clientIp', label: '클라이언트 IP', mono: true, copyable: true },
		// 조건 없이 붙인다. 이 둘은 애플리케이션의 로그 줄이 아니라 쿠버네티스
		// 봉투에서 오므로, 운영자가 무엇을 설정했든 존재한다 — "—" 라고 적힌
		// 상세를 열 수가 없다. 이것을 선언하는 것이 이 패널에 행 상세를 주는
		// 유일한 방법이기도 하다. 프론트는 detail 컬럼이 있는 곳에 펼치기를
		// 내주고, 펼친 화면은 모든 컬럼을 보여 준다. 404 하나를 보는 운영자가
		// 잘리지 않은 요청 전체를 한자리에서 얻는다.
		{ key: 'container', label: '컨테이너', detail: true, mono: true, copyable: true },
		{ key: 'namespace', label: '네임스페이스', detail: true, mono: true, copyable: true }
	];
	// 쿼리가 실제로 골랐을 때만 선언한다. 이쪽은 운영자가 설정하는 것이라,
	// 항상 있는 컬럼은 그 필드를 이름 짓지 않은 모든 클러스터의 상세에 "—" 를
	// 하나씩 넣게 된다.
	if (rc.cfg.logFormat.userAgentField !== '') {
		cols.push({ key: 'userAgent', label: 'User-Agent', detail: true, mono: true, copyable: true });
	}
	panel.table = newTable(cols, rows, honestTotal(total, rows.length), limit);

	panel.stats.push({
		key: 'pod.badStatus.total',
		label: '비정상 응답',
		value: total,
		unit: 'count',
		basis:
			`상태 코드가 ${rc.cfg.logFormat.okStatuses.join(', ')} 가 아닌 요청 (집계 전체)` +
			excludedPaths(rc.cfg.logFormat),
		intent: 'bad'
	});
	noteQueryCost(panel, results);
	return panel;
}

export function statusColor(code: string): string {
	if (code.startsWith('5')) return colorRed;
	if (code.startsWith('4')) return colorOrange;
	if (code.startsWith('3')) return colorYellow;
	return colorGray;
}

/**
 * honestTotal 은 표의 총계를 그것이 싣고 있는 행 수 이상으로 유지한다. 행 수보다
 * 작은 총계는 말이 되지 않고 payload 검증기가 거절하는데, 반올림 차이가 그것을
 * 만들어 낼 수 있는 유일한 자리가 여기다.
 */
export function honestTotal(total: number, rows: number): number {
	const t = Math.trunc(total);
	return t < rows ? rows : t;
}

/** 상태 코드 하나와 그것을 만든 경로들. */
interface StatusBucket {
	code: string;
	total: number;
	lastTs: string;
	paths: { path: string; n: number }[];
}

/**
 * pivotStatusPaths 는 (상태, 경로) 행을 상태 코드마다 한 묶음으로 접는다. 바쁜
 * 것이 먼저이고, 각 묶음 안의 경로도 바쁜 것이 먼저다.
 *
 * pivotWAFActions 와 같은 모양이고 이유도 같다. 쿼리가 한 번의 스캔으로 두 키를
 * 묶었고, 그것을 "코드마다 한 행, 그 아래 경로들" 로 바꾸는 접기는 같은 바이트에
 * 대한 두 번째 쿼리보다 여기서 하는 쪽이 싸다.
 */
export function pivotStatusPaths(rows: Record<string, string>[]): StatusBucket[] {
	const index = new Map<string, number>();
	const out: StatusBucket[] = [];

	for (const r of rows) {
		const code = r['status'] === undefined || r['status'] === '' ? '(none)' : r['status'];
		const n = rowFloat(r, 'n');
		if (n === null) continue;

		let i = index.get(code);
		if (i === undefined) {
			i = out.length;
			index.set(code, i);
			out.push({ code, total: 0, lastTs: '', paths: [] });
		}
		const b = out[i] as StatusBucket;
		b.total += n;
		// 경로가 없는 행도 코드의 총계에는 셈된다 — 버리면 분해가 차트와 어긋난다
		// — 다만 나열할 이름이 없으므로 경로로 제시하지는 않는다.
		const p = r['path'];
		if (p !== undefined && p !== '') b.paths.push({ path: p, n });
		// @timestamp 는 고정 폭이므로 사전순이 곧 시간순이다.
		const ts = r['lastTs'];
		if (ts !== undefined && ts > b.lastTs) b.lastTs = ts;
	}

	for (const b of out) {
		b.paths.sort((x, y) => (x.n !== y.n ? y.n - x.n : x.path < y.path ? -1 : x.path > y.path ? 1 : 0));
	}
	out.sort((x, y) => (x.total !== y.total ? y.total - x.total : x.code < y.code ? -1 : x.code > y.code ? 1 : 0));
	return out;
}

/**
 * topPathsNote 는 한 묶음의 경로를 행의 펼친 상세에 넣을 모양으로 만들고, 몇 개를
 * 빼놓았는지 말한다.
 *
 * 중첩된 표가 아니라 문자열 하나인 이유: 상세 화면은 이 행 자신의 값을 늘어놓는
 * 정의 목록이고, 코드의 경로들은 정확히 그런 것이다 — 마침 목록인 값 하나. topN
 * 에서 말없이 자르면 "이것이 그 경로들이다" 로 읽히는데, 404 홍수에서 그것은 사실의
 * 정반대다.
 */
export function topPathsNote(b: StatusBucket, topN: number): string {
	if (b.paths.length === 0) return '경로가 기록되지 않았습니다';
	const shown = topN > 0 && b.paths.length > topN ? b.paths.slice(0, topN) : b.paths;
	const parts = shown.map((p) => `${p.path} (${formatCount(p.n)}건)`);
	const rest = b.paths.length - shown.length;
	if (rest > 0) parts.push(`외 ${rest}개`);
	return parts.join(' · ');
}

/**
 * Go 의 strconv.FormatFloat(v, 'f', -1, 64) 와 같은 모양으로 낸다. JS 의 기본
 * 문자열화는 큰 수와 아주 작은 수에서 지수 표기로 넘어간다.
 */
function formatCount(v: number): string {
	if (Number.isInteger(v)) return v.toFixed(0);
	const s = String(v);
	return s.includes('e') || s.includes('E') ? v.toFixed(20).replace(/0+$/, '') : s;
}

/**
 * buildPodStatusBreakdownPanel 은 "404 를 만든 경로는 무엇이고 403 을 만든 경로는
 * 무엇인가" 에 답한다 — 상태 코드마다 한 행, 그 경로는 행 상세에.
 *
 * pod-status-codes 의 두 번째 표가 아니라 제 패널인 이유는, 패널이 표 하나를
 * 싣기 때문이고 둘이 다른 종류이기 때문이다. 저쪽은 개별 요청을 나열하고 이쪽은
 * 그것을 집계한다. 오버뷰 페이지에 두지 않은 것도 의도다 — 코드별 경로 분해는
 * 운영자가 찾아가는 것이고, 거기 두면 가장 자주 뜨는 화면에 Insights 스캔이 하나
 * 더 붙는다.
 */
export async function buildPodStatusBreakdownPanel(
	service: Service,
	rc: RequestCtx
): Promise<Panel> {
	const panel = new Panel('pod-status-breakdown', '응답 코드별 경로');
	const lq = new q.LogQueries(rc.cfg.logFormat);
	const topN = rc.cfg.limits.topN;

	// 여기서 경로 필드는 다른 곳처럼 선택적이지 않다 — 이 패널이 곧 *경로별*
	// 분해이므로, 그것이 없으면 할 말이 없다. 오류가 아니라 경고로 말한다.
	// 다른 모든 팟 패널이 이 설정에서 실패하지 않고 낮은 수준으로 동작하고,
	// 여기서 오류를 내면 팟 로그 페이지 전체가 실패 카드가 되며 이 패널 자신의
	// 엔드포인트는 502 가 된다. 쿼리를 건너뛰면 스캔도 아낀다.
	if (rc.cfg.logFormat.pathField === '') {
		panel.warn('경로 필드(pathField)가 설정되지 않아 코드별 경로를 집계할 수 없습니다.');
		return panel;
	}

	const byPath = q.podBadStatusByPath(lq);
	const src = podLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'pod-status-breakdown', [
		byPath
	]);
	noteQueryErrors(panel, src, errors);
	warnIfTruncated(panel, byPath.id, results);

	const rows: Row[] = [];
	let total = 0;
	const res = results.get(byPath.id);
	if (res !== undefined) {
		for (const b of pivotStatusPaths(res.rows)) {
			total += b.total;
			rows.push({
				status: b.code,
				count: b.total,
				paths: b.paths.length,
				// 제 이름이 아니라 `timestamp` 다. 프론트는 이 키로 셀을 로그
				// 시각 포매터에 태우므로, 다른 이름이면 지역 시간을 보여 주는
				// 컬럼 옆에 날것의 UTC 문자열이 뜬다.
				timestamp: b.lastTs,
				topPaths: topPathsNote(b, topN)
			});
		}
	}

	// 총계는 요청 수가 아니라 행 수다. 이 표는 상태 코드를 나열하고, 쿼리가 본
	// 모든 코드가 거기 있다. 이 수준에서 잘려 나가는 것은 없다 — 행 안쪽의
	// 상한, 즉 경로 목록의 상한은 topPathsNote 가 말하고, 쿼리 위의 상한은
	// warnIfTruncated 가 말한다.
	panel.table = newTable(
		[
			{ key: 'status', label: '코드', mono: true, copyable: true },
			{ key: 'count', label: '건수', numeric: true },
			{ key: 'paths', label: '경로 종류', numeric: true },
			{ key: 'timestamp', label: '마지막 발생', mono: true },
			{ key: 'topPaths', label: '상위 경로', detail: true, mono: true, copyable: true }
		],
		rows,
		rows.length,
		topN
	);

	// 같은 행의 두 가지 표현이므로, 막대가 표에 없는 것을 셀 수 없다.
	panel.bars = { keyColumn: 'status', valueColumn: 'count' };

	// 쿼리가 행 상한에 걸린 뒤로는 둘 다 수치가 아니라 하한이다. Insights 가
	// 버린 행은 건수가 가장 적은 (상태, 경로) 쌍이므로, 거기에만 나타나던 코드는
	// 집계에서 빠지고 그 요청도 합계에서 빠진다. basis 에 "이상" 이라고 적는 것이
	// 여기서 할 수 있는 전부다 — 두 번째 스캔 없이 그 숫자를 되찾을 수는 없다.
	let codesBasis = '구간 내 관측된 비정상 응답 코드';
	let totalBasis = '코드 · 경로별 집계 합계 (전체)';
	if (res !== undefined && res.truncated) {
		codesBasis += ' (결과 상한에 걸려 실제보다 적을 수 있음)';
		totalBasis = '코드 · 경로별 집계 합계 (결과 상한에 걸려 실제 이상)';
	}

	const excluded = excludedPaths(rc.cfg.logFormat);
	panel.stats.push(
		{
			key: 'pod.badStatus.codes',
			label: '코드 종류',
			value: rows.length,
			unit: 'count',
			basis: codesBasis + excluded
		},
		{
			key: 'pod.badStatus.byPath.total',
			label: '비정상 응답',
			value: total,
			unit: 'count',
			basis: totalBasis + excluded
			// intent 없음: 같은 숫자가 pod-status-codes 에서 이미 bad 로 표시돼
			// 있고, 사실 하나에 경보 타일이 둘이면 카드 두 장이 붉어진다.
		}
	);
	noteQueryCost(panel, results);
	return panel;
}

export async function buildPodErrorPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('pod-errors', 'ERROR · WARN 로그');
	const lq = new q.LogQueries(rc.cfg.logFormat);
	const limit = rc.cfg.limits.logRows;

	const series = q.podErrorSeries(lq, rc.w);
	const list = q.podErrorList(lq, limit);

	const src = podLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'pod-errors', [series, list]);
	noteQueryErrors(panel, src, errors);

	const n = buckets(rc.w);
	const errSeries = new Series('error', 'count', colorRed, n);
	const warnSeries = new Series('warn', 'count', colorOrange, n);
	let errTotal = 0;
	let warnTotal = 0;

	const fromSeries = results.get(series.id);
	if (fromSeries !== undefined) {
		for (const row of fromSeries.rows) {
			const ts = parseInsightsTime(row['t'] ?? '');
			if (ts === null) continue;
			const idx = indexOf(rc.w, ts);
			if (idx === null) continue;
			const v = rowFloat(row, 'n');
			if (v === null) continue;

			if ((row['level'] ?? '').toLowerCase() === 'warn') {
				warnSeries.add(idx, v);
				warnTotal += v;
			} else {
				errSeries.add(idx, v);
				errTotal += v;
			}
		}
	}
	panel.series.push(errSeries, warnSeries);

	const rows: Row[] = [];
	const fromList = results.get(list.id);
	if (fromList !== undefined) {
		for (const r of fromList.rows) {
			const msg = r['dashboardMessage'] !== undefined && r['dashboardMessage'] !== ''
				? r['dashboardMessage']
				: (r['@message'] ?? '');
			rows.push({
				timestamp: r['@timestamp'] ?? '',
				pod: r['pod'] ?? '',
				container: r['container'] ?? '',
				message: msg
			});
		}
	}

	// 총계는 맞는 모든 줄을 세고, 목록은 상한에서 멈춘다. 이전 구현은 잘린 배열의
	// 길이를 총계로 보여 줬고, 그래서 대표 숫자가 300에서 조용히 얼어붙었다.
	panel.table = newTable(
		[
			{ key: 'timestamp', label: '시각', mono: true },
			{ key: 'container', label: '컨테이너', copyable: true },
			{ key: 'pod', label: '팟', mono: true, copyable: true },
			{ key: 'message', label: '메시지', mono: true, copyable: true }
		],
		rows,
		honestTotal(errTotal + warnTotal, rows.length),
		limit
	);

	const excluded = excludedPaths(rc.cfg.logFormat);
	panel.stats.push(
		{
			key: 'pod.error.total',
			label: 'ERROR',
			value: errTotal,
			unit: 'count',
			basis: `level 또는 메시지 패턴이 error 계열 (집계 전체)${excluded}`,
			intent: 'bad'
		},
		{
			key: 'pod.warn.total',
			label: 'WARN',
			value: warnTotal,
			unit: 'count',
			basis: `level 또는 메시지 패턴이 warn 계열 (집계 전체)${excluded}`,
			intent: 'warn'
		}
	);
	noteQueryCost(panel, results);
	return panel;
}

/**
 * wafResponseNote 는 요청이 어떤 응답을 받았는지 말하고, 대부분의 행에 줄 숫자가
 * 없으므로 말로 한다.
 *
 * WAF 로그는 애플리케이션의 상태 코드를 싣지 않는다. responseCodeSent 는 Block
 * 액션에 사용자 지정 응답이 설정됐을 때만 쓰이고, 평범한 차단은 403 으로 답하면서
 * 그것에 대해 아무것도 기록하지 않으며, WAF 가 허용한 것에 답한 것은 애플리케이션이라
 * 팟 로그만이 그것을 봤다. 그래서 여기에 "상태 코드" 컬럼을 두면 한 종류를 빼고는
 * 모든 행에 어떤 레코드도 뒷받침하지 않는 숫자를 찍게 된다 — 그래서 컬럼이 문장이고,
 * 그 문장은 이 행이 어느 경우인지와 나머지 답이 어디 있는지를 말한다.
 */
export function wafResponseNote(action: string, sent: string): string {
	if (sent !== '') return `${sent} (WAF 사용자 지정 응답)`;
	switch (action.trim().toUpperCase()) {
		case 'BLOCK':
			return '403 · WAF 기본 차단 응답 (로그에 코드가 기록되지 않음)';
		case 'CAPTCHA':
		case 'CHALLENGE':
			return 'WAF가 CAPTCHA · Challenge 응답을 보냈습니다 (코드는 로그에 없음)';
		default:
			return 'WAF 로그에 없음 · 애플리케이션 응답 코드는 팟 로그에서 확인하세요';
	}
}

/**
 * wafActionColor 는 WAF 액션마다 색 하나를 못박는다. 그것을 보여 주는 모든 패널이
 * 같은 색을 쓴다. 프론트는 이것을 --waf-allow, --waf-block 식으로 별칭 짓고 각각
 * 옆에 다른 글리프를 그리므로, 색이 읽히지 않는 상황에서도 액션은 읽힌다.
 *
 * CAPTCHA 와 CHALLENGE 를 기본값에 맡기지 않고 이름 붙였다. 예전에는 빈 액션과
 * 함께 기본값을 나눠 써서, 서로 다른 결과 셋이 구분되지 않는 노란색 하나로
 * 그려졌다.
 */
export function wafActionColor(action: string): string {
	switch (action.toUpperCase()) {
		case 'ALLOW':
			return colorGreen;
		case 'BLOCK':
			return colorPink;
		case 'COUNT':
		case 'EXCLUDED_AS_COUNT':
			return colorGray;
		case 'CHALLENGE':
			return colorOrange;
		case 'CAPTCHA':
			return colorPurple;
		default:
			return colorYellow;
	}
}

export async function buildWAFTrafficPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('waf-traffic', 'WAF 트래픽');

	const limit = rc.cfg.limits.logRows;
	const series = q.wafActionSeries(rc.w);
	const recent = q.wafRecentList(limit);
	const src = wafLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'waf-traffic', [
		series,
		recent
	]);
	noteQueryErrors(panel, src, errors);

	const byAction = new Map<string, Series>();
	const totals = new Map<string, number>();
	const fromSeries = results.get(series.id);
	if (fromSeries !== undefined) {
		for (const row of fromSeries.rows) {
			const ts = parseInsightsTime(row['t'] ?? '');
			if (ts === null) continue;
			const idx = indexOf(rc.w, ts);
			if (idx === null) continue;
			const v = rowFloat(row, 'n');
			if (v === null) continue;

			const action = row['action'] === undefined || row['action'] === '' ? '(none)' : row['action'];
			let s = byAction.get(action);
			if (s === undefined) {
				s = new Series(action, 'count', wafActionColor(action), buckets(rc.w));
				byAction.set(action, s);
			}
			s.add(idx, v);
			totals.set(action, (totals.get(action) ?? 0) + v);
		}
	}

	let overall = 0;
	for (const action of [...byAction.keys()].sort()) {
		panel.series.push(byAction.get(action) as Series);
		const n = totals.get(action) ?? 0;
		overall += n;
		// intent 없음. 트래픽을 차단하는 WAF 는 제 일을 하는 WAF 이므로, BLOCK 을
		// "bad" 로 표시하면 대시보드가 영구 경보 상태가 되고 진짜 급증이 평범한
		// 화요일과 구분되지 않는다. 액션은 대신 색과 글리프가 싣는다.
		panel.stats.push({
			key: `waf.log.${action.toLowerCase()}`,
			label: action,
			value: n,
			unit: 'count',
			basis: 'WAF 로그 action 집계 (전체)'
		});
	}

	const rows: Row[] = [];
	const fromRecent = results.get(recent.id);
	if (fromRecent !== undefined) {
		for (const r of fromRecent.rows) {
			rows.push({
				timestamp: r['@timestamp'] ?? '',
				action: r['action'] ?? '',
				rule: r['rule'] ?? '',
				clientIp: r['clientIp'] ?? '',
				country: r['country'] ?? '',
				method: r['method'] ?? '',
				uri: r['uri'] ?? '',
				args: r['args'] ?? '',
				ruleType: r['ruleType'] ?? '',
				userAgent: r['userAgent'] ?? '',
				responseCode: wafResponseNote(r['action'] ?? '', r['responseCode'] ?? '')
			});
		}
	}

	// 총계는 액션 시계열 자신의 합이고, 창 전체에 대해 이 목록과 완전히 무관하게
	// 세어진 것이다. 목록이 행 상한에서 멈춰도 그 위의 수치는 멈추지 않는 이유다.
	//
	// bars 없음: 이것은 개별 요청이지 분포가 아니다. 여기서 막대를 그리면 모든
	// 막대 높이가 1인 차트가 화면에 뜬다.
	panel.table = newTable(
		[
			{ key: 'timestamp', label: '시각', mono: true },
			{ key: 'action', label: '처리' },
			{ key: 'method', label: '메소드' },
			{ key: 'uri', label: '경로', mono: true, copyable: true },
			{ key: 'args', label: '쿼리', mono: true, copyable: true },
			{ key: 'clientIp', label: '클라이언트', mono: true, copyable: true },
			{ key: 'country', label: '국가' },
			{ key: 'rule', label: '룰', copyable: true },
			// 상세 전용. 룰 종류는 운영자가 한 행에 대해 한 번 궁금해하는 낱말이고,
			// User-Agent 는 다른 컬럼을 전부 쥐어짜지 않고는 컬럼을 줄 수 없을
			// 만큼 길며, 응답 메모는 문장이다.
			{ key: 'ruleType', label: '룰 종류', detail: true },
			{ key: 'userAgent', label: 'User-Agent', detail: true, mono: true, copyable: true },
			{ key: 'responseCode', label: '응답 코드', detail: true }
		],
		rows,
		honestTotal(overall, rows.length),
		limit
	);

	noteQueryCost(panel, results);
	return panel;
}

export async function buildWAFBlockedPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('waf-blocked', '차단된 요청');

	const agg = q.wafBlocked(rc.cfg.limits.topN);
	const src = wafLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'waf-blocked', [agg]);
	noteQueryErrors(panel, src, errors);

	const rows: Row[] = [];
	let listTotal = 0;
	const res = results.get(agg.id);
	if (res !== undefined) {
		for (const r of res.rows) {
			listTotal += rowFloat(r, 'n') ?? 0;
			rows.push({
				rule: r['rule'] ?? '',
				clientIp: r['clientIp'] ?? '',
				country: r['country'] ?? '',
				count: r['n'] ?? ''
			});
		}
	}
	panel.table = newTable(
		[
			{ key: 'rule', label: '규칙', mono: true, copyable: true },
			{ key: 'clientIp', label: '클라이언트 IP', mono: true, copyable: true },
			{ key: 'country', label: '국가' },
			{ key: 'count', label: '건수', numeric: true }
		],
		rows,
		honestTotal(listTotal, rows.length),
		rc.cfg.limits.topN
	);
	panel.bars = { keyColumn: 'rule', valueColumn: 'count' };

	// 예전에 여기서 가져왔다가 버리던 요청별 목록은 이제 waf-traffic 에 산다.
	// 거기서는 실제로 그려지고, 차단만이 아니라 모든 액션을 덮는다. 이 패널의
	// 스캔 수는 하나 줄었고 페이지 전체의 스캔 수는 그대로다.
	noteQueryCost(panel, results);
	return panel;
}

/**
 * actionFanout 은 분해 쿼리에, 표가 보여 줄 키 개수 위로 얼마나 여유를 주는지다.
 *
 * 이제 모든 분해가 제 키와 함께 액션으로도 묶으므로, 경로 하나가 그것이 받아 본
 * 액션마다 한 행씩 차지한다. 여유가 없으면 쿼리 자신의 limit 이 어느 키가
 * 살아남을지 정하고 — 그것도 (키, 액션) 의 양으로 정한다 — 그래서 ALLOW 행이
 * 크고 BLOCK 행이 작은 경로가 차단이 조용히 빠진 채 도착할 수 있다. 그 차단이
 * 바로 이 분해가 존재하는 이유인 숫자다.
 */
const actionFanout = 4;

/** 분해 키 하나와, 액션별로 갈렸던 것을 다시 접은 결과. */
interface WAFBucket {
	key: string;
	allow: number;
	block: number;
	other: number;
	total: number;
	lastAction: string;
	lastTs: string;
}

/**
 * pivotWAFActions 는 (키, 액션) 행을 키마다 한 묶음으로 접는다. 바쁜 것이 먼저다.
 *
 * 액션별 수치를 쿼리가 아니라 여기서 더하는 이유는 Logs Insights 에 조건부 집계가
 * 없기 때문이다. "action=BLOCK 인 것의 count" 를 "action=ALLOW 인 것의 count" 와
 * 한 stats 명령에 나란히 요구하는 것은 표현할 수 없고, 두 명령으로 요구하는 것은
 * 같은 바이트를 두 번 스캔하는 것이다.
 */
export function pivotWAFActions(
	rows: Record<string, string>[],
	keyOf: (r: Record<string, string>) => string
): WAFBucket[] {
	const index = new Map<string, number>();
	const out: WAFBucket[] = [];

	for (const r of rows) {
		const key = keyOf(r);
		if (key === '') continue;
		const n = rowFloat(r, 'n');
		if (n === null) continue;

		let i = index.get(key);
		if (i === undefined) {
			i = out.length;
			index.set(key, i);
			out.push({ key, allow: 0, block: 0, other: 0, total: 0, lastAction: '', lastTs: '' });
		}
		const b = out[i] as WAFBucket;

		const action = (r['action'] ?? '').trim().toUpperCase();
		if (action === 'ALLOW') b.allow += n;
		else if (action === 'BLOCK') b.block += n;
		// COUNT, CHALLENGE, CAPTCHA 와 앞으로 WAF 가 더할 무엇이든. 버리지 않고
		// 한데 모은다. 컬럼이 합계와 맞아야 하고, 안 그러면 표가 독자에게 그
		// 차이를 계산해 보라고 부추긴 뒤 아무 뜻도 없음을 알게 한다.
		else b.other += n;
		b.total += n;

		// Insights 는 @timestamp 를 고정 폭 "YYYY-MM-DD hh:mm:ss.SSS" 로 내므로
		// 사전순이 곧 시간순이고 파싱이 필요 없다. 빈 액션도 정말로 가장 최근이면
		// 비교에서 이긴다 — 조용히 건너뛰지 않고 기록된 대로 보고한다.
		const ts = r['lastTs'] ?? '';
		if (ts > b.lastTs) {
			b.lastTs = ts;
			b.lastAction = action;
		}
	}

	out.sort((x, y) => (x.total !== y.total ? y.total - x.total : x.key < y.key ? -1 : x.key > y.key ? 1 : 0));
	return out;
}

export async function buildWAFBreakdownPanel(service: Service, rc: RequestCtx): Promise<Panel> {
	const panel = new Panel('waf-breakdown', 'WAF 통계');
	const topN = rc.cfg.limits.topN;
	const fetch = topN * actionFanout;

	const queries: q.Query[] = [q.wafByMethod(), q.wafByPath(fetch)];
	const headerQueries = new Map<string, string>();
	for (const h of rc.cfg.wafHeaders) {
		try {
			const hq = q.wafByHeader(h, fetch);
			queries.push(hq);
			headerQueries.set(hq.id, h);
		} catch (err) {
			panel.warn(
				`헤더 "${h}" 통계를 만들 수 없습니다: ${err instanceof Error ? err.message : String(err)}`
			);
		}
	}

	const src = wafLogs(service, rc);
	const { results, errors } = await runLogQueries(service, rc, src, 'waf-breakdown', queries);
	noteQueryErrors(panel, src, errors);

	// 모든 분해가 한 표의 행이 되고, 어느 분해에서 왔는지 꼬리표를 단다. 그래야
	// 값마다 제 복사 버튼을 지킨다.
	const rows: Row[] = [];
	let total = 0;
	// keysFound 는 차원별 상한을 걸기 전에 분해가 실제로 가른 것을 센다. 표의
	// 총계는 화면에 뜬 행 수가 아니라 그것이다 — 잘린 목록이 그 옆에 찍히는
	// 수치를 줄여서는 안 된다.
	let keysFound = 0;

	const add = (dimension: string, list: WAFBucket[]): void => {
		keysFound += list.length;
		for (const b of list.length > topN ? list.slice(0, topN) : list) {
			rows.push({
				dimension,
				key: b.key,
				allow: b.allow,
				block: b.block,
				other: b.other,
				count: b.total,
				lastAction: b.lastAction
			});
		}
	};

	const byMethod = results.get('waf.byMethod');
	if (byMethod !== undefined) {
		const list = pivotWAFActions(byMethod.rows, (r) => r['method'] ?? '');
		for (const b of list) total += b.total;
		add('method', list);
	}
	const byPath = results.get('waf.byPath');
	if (byPath !== undefined) {
		add(
			'path',
			pivotWAFActions(byPath.rows, (r) => {
				const uri = r['uri'] ?? '';
				const args = r['args'] ?? '';
				return args !== '' ? `${uri}?${args}` : uri;
			})
		);
	}
	// 정렬한다. 맵을 그대로 도는 것은 그 밖에는 똑같은 두 응답 사이에서 헤더
	// 분해의 순서를 바꿔 놓는다.
	for (const id of [...headerQueries.keys()].sort()) {
		const res = results.get(id);
		if (res === undefined) continue;
		add(`header:${headerQueries.get(id) as string}`, pivotWAFActions(res.rows, (r) => r['value'] ?? ''));
	}

	panel.table = newTable(
		[
			{ key: 'dimension', label: '구분' },
			{ key: 'key', label: '값', mono: true, copyable: true },
			{ key: 'allow', label: '허용', numeric: true },
			{ key: 'block', label: '차단', numeric: true },
			{ key: 'other', label: '기타', numeric: true },
			{ key: 'count', label: '합계', numeric: true },
			{ key: 'lastAction', label: '마지막 처리' }
		],
		rows,
		honestTotal(keysFound, rows.length),
		topN
	);

	// 막대와 표는 같은 행의 두 표현이므로, 차트가 그 아래 목록과 다른 것을 셀 수
	// 없다.
	panel.bars = { keyColumn: 'key', valueColumn: 'count', groupColumn: 'dimension' };

	panel.stats.push({
		key: 'waf.requests.total',
		label: '요청 수',
		value: total,
		unit: 'count',
		basis: 'WAF 로그 메소드별 집계 합계 (전체)'
	});
	noteQueryCost(panel, results);
	return panel;
}
