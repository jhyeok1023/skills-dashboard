// GetMetricData 를 돌린다. internal/awsx/metrics.go 의 이식이다.

import {
	GetMetricDataCommand,
	type CloudWatchClient,
	type MetricDataQuery
} from '@aws-sdk/client-cloudwatch';

import type { Unit } from '../contract.ts';
import { queryID, searchExpression, type MetricSpec } from '../domain/catalog.ts';
import { Series } from '../domain/series.ts';
import { buckets, periodSeconds, timestamps, type Window } from '../domain/window.ts';
import { sendOptions } from './client.ts';

/**
 * 한 번의 GetMetricData 호출에 넣을 수 있는 MetricDataQueries 의 CloudWatch 상한.
 *
 * 이전 구현은 이 숫자를 주석에 적어 두고 한 번도 강제하지 않았다. 쿼리 목록을
 * ListMetrics 로 만들었는데 — 그것은 최대 2주 전에 지워진 팟의 차원까지 계속
 * 돌려준다 — 목록이 단조 증가해 천장을 넘었고, 그 뒤로 모든 호출이 검증에서
 * 실패해 메트릭 패널이 영영 비어 있었다. 여기서는 일을 쪼개 상한을 강제한다.
 */
export const maxQueriesPerCall = 500;

/** NextToken 순회의 상한. 병적인 응답이 요청을 무한히 붙들지 못하게 한다. */
const maxPages = 20;

/**
 * 시리즈 색인의 구분자. 키에도 라벨에도 나올 수 없는 문자여야 한다 — Go 도
 * 같은 이유로 NUL 을 썼다.
 */
const sep = String.fromCharCode(0);

/** MetricRequest 는 카탈로그 항목과 그것을 거를 차원 값을 짝짓는다. */
export interface MetricRequest {
	/**
	 * 이 요청의 결과를 가르는 키. 기본은 스펙의 키이고, 스펙 하나를 서로 다른
	 * 필터로 여러 번 낼 때 — 이를테면 타겟 그룹마다 — 결과가 갈리도록 덮어쓴다.
	 */
	key?: string;
	spec: MetricSpec;
	filters: Record<string, string>;
}

/** 이 요청의 시리즈가 묶이는 키. */
export function resultKey(r: MetricRequest): string {
	return r.key !== undefined && r.key !== '' ? r.key : r.spec.key;
}

/**
 * MetricSeries 는 한 요청이 돌려받은 시계열 하나다. SEARCH 식은 한 번에 여러
 * 시리즈에 맞으므로 — 팟마다, 노드마다, 타겟 그룹마다 — 요청 하나가 보통 이것을
 * 여럿 낸다.
 */
export interface MetricSeries {
	/** 요청한 스펙의 키. */
	key: string;
	/** CloudWatch 가 붙인 라벨. 이를테면 팟 이름. */
	label: string;
	points: Map<number, number>;
}

export interface MetricFetcherOptions {
	/** 테스트에서 maxQueriesPerCall 을 덮어쓴다. */
	maxQueries?: number;
}

export class MetricFetcher {
	private readonly api: CloudWatchClient;
	private readonly chunkSize: number;

	constructor(api: CloudWatchClient, options: MetricFetcherOptions = {}) {
		this.api = api;
		this.chunkSize =
			options.maxQueries !== undefined && options.maxQueries > 0
				? options.maxQueries
				: maxQueriesPerCall;
	}

	/** fetch 는 창 위의 모든 요청을 풀어 스펙 키별로 묶은 시리즈를 돌려준다. */
	async fetch(
		w: Window,
		reqs: MetricRequest[],
		signal?: AbortSignal
	): Promise<Map<string, MetricSeries[]>> {
		const out = new Map<string, MetricSeries[]>();
		if (reqs.length === 0) return out;

		const queries: MetricDataQuery[] = [];
		// byID 는 쿼리 식별자를 그것을 요청한 쪽으로 되돌린다. 결과를 위치가
		// 아니라 이 맵으로 맞춘다 — 이전 구현은 id 에서 Sscanf 로 배열 인덱스를
		// 복원하고 파싱 오류를 무시한 뒤 그 값으로 슬라이스를 인덱싱했다.
		// 그래서 예상 못 한 id 는 엉뚱한 시리즈에 쓰였고, 범위 밖은 핸들러를
		// 패닉시켰다.
		const byID = new Map<string, MetricRequest>();

		for (const req of reqs) {
			let expr: string;
			try {
				expr = searchExpression(req.spec, req.filters, periodSeconds(w.period));
			} catch (err) {
				throw new Error(
					`build search for ${resultKey(req)}: ${err instanceof Error ? err.message : String(err)}`
				);
			}
			const id = queryID(resultKey(req));
			if (byID.has(id)) {
				throw new Error(`duplicate query id "${id}" for metric ${resultKey(req)}`);
			}
			byID.set(id, req);
			queries.push({
				Id: id,
				Expression: expr,
				ReturnData: true,
				Period: periodSeconds(w.period)
			});
		}

		for (let start = 0; start < queries.length; start += this.chunkSize) {
			await this.fetchChunk(w, queries.slice(start, start + this.chunkSize), byID, out, signal);
		}
		return out;
	}

	private async fetchChunk(
		w: Window,
		queries: MetricDataQuery[],
		byID: Map<string, MetricRequest>,
		out: Map<string, MetricSeries[]>,
		signal?: AbortSignal
	): Promise<void> {
		// 쌓여 가는 시리즈에 색인을 둔다. 반복되는 페이지와 여러 청크가 같은
		// 시리즈에 덧붙게 하려는 것이다 — 안 그러면 중복이 생긴다.
		const index = new Map<string, MetricSeries>();
		for (const [key, list] of out) {
			for (const s of list) index.set(key + sep + s.label, s);
		}

		let nextToken: string | undefined;
		for (let page = 0; ; page++) {
			if (page >= maxPages) {
				throw new Error(
					`GetMetricData returned more than ${maxPages} pages; refusing to keep walking`
				);
			}

			let resp;
			try {
				resp = await this.api.send(
					new GetMetricDataCommand({
						MetricDataQueries: queries,
						StartTime: new Date(w.start),
						EndTime: new Date(w.end),
						ScanBy: 'TimestampAscending',
						...(nextToken !== undefined ? { NextToken: nextToken } : {})
					}),
					sendOptions(signal)
				);
			} catch (err) {
				throw new Error(`GetMetricData: ${err instanceof Error ? err.message : String(err)}`);
			}

			for (const r of resp.MetricDataResults ?? []) {
				const id = r.Id ?? '';
				const req = byID.get(id);
				if (req === undefined) {
					// 아무도 묻지 않은 id 는 데이터가 아니라 버그다. 아무 시리즈에나
					// 써 넣는 대신 그렇다고 말한다.
					throw new Error(`GetMetricData returned unknown query id "${id}"`);
				}
				const stamps = r.Timestamps ?? [];
				const values = r.Values ?? [];
				if (stamps.length !== values.length) {
					throw new Error(
						`query "${id}" returned ${stamps.length} timestamps for ${values.length} values`
					);
				}

				const key = resultKey(req);
				const label = r.Label !== undefined && r.Label !== '' ? r.Label : req.spec.label;
				const k = key + sep + label;
				let series = index.get(k);
				if (series === undefined) {
					series = { key, label, points: new Map<number, number>() };
					const list = out.get(key);
					if (list === undefined) out.set(key, [series]);
					else list.push(series);
					index.set(k, series);
				}
				for (let i = 0; i < stamps.length; i++) {
					series.points.set(alignToBucket(stamps[i] as Date, w), values[i] as number);
				}
			}

			if (resp.NextToken === undefined || resp.NextToken === '') return;
			nextToken = resp.NextToken;
		}
	}
}

/**
 * alignToBucket 은 CloudWatch 타임스탬프를 창의 격자에 맞춘다. CloudWatch 는 이미
 * 주기에 맞춘 시각을 주므로 다시 맞추는 값은 공짜이고, 주기가 어긋난 경우가
 * 조용히 그려지지 않는 시리즈 대신 덮어쓰인 점으로 드러난다.
 */
export function alignToBucket(ts: Date, w: Window): number {
	return Math.trunc((Math.floor(ts.getTime() / w.period) * w.period) / 1000);
}

/**
 * toSeries 는 MetricSeries 를 창의 축에 투영해, 프론트가 그리는 구멍 보존
 * 표현으로 만든다.
 */
export function toSeries(
	m: MetricSeries,
	w: Window,
	label: string,
	unit: Unit,
	color: string
): Series {
	const s = new Series(label, unit, color, buckets(w));
	const axis = timestamps(w);
	for (let i = 0; i < axis.length; i++) {
		const v = m.points.get(axis[i] as number);
		if (v !== undefined) s.set(i, v);
	}
	return s;
}

/** sortSeries 는 라벨로 정렬한다. 새로고침 때마다 범례가 뒤섞이지 않게. */
export function sortSeries(list: MetricSeries[]): void {
	list.sort((a, b) => (a.label < b.label ? -1 : a.label > b.label ? 1 : 0));
}
