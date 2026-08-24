// Logs Insights 쿼리를 돌린다. internal/awsx/insights.go 의 이식이다.
//
// 이전 구현에 없던 난간을 단다: 동시에 나는 쿼리 수의 상한, 쿼리마다의 시한,
// 그리고 나가는 길의 StopQuery.

import {
	GetQueryResultsCommand,
	StartQueryCommand,
	StopQueryCommand,
	type CloudWatchLogsClient
} from '@aws-sdk/client-cloudwatch-logs';

import type { Query } from '../domain/query.ts';
import type { Window } from '../domain/window.ts';
import { sendOptions } from './client.ts';

/**
 * CloudWatch 가 Logs Insights 결과 집합에 씌우는 천장. 정확히 이만큼의 행을
 * 돌려준 쿼리는 잘렸을 가능성이 크고, payload 는 부분적인 답을 완전한 것처럼
 * 내놓는 대신 그렇다고 말한다.
 */
export const insightsMaxRows = 10_000;

/** 완료된 Insights 쿼리 하나. */
export interface QueryResult {
	id: string;
	rows: Record<string, string>[];

	/**
	 * 이 쿼리가 든 비용. Insights 는 스캔한 바이트로 과금하므로 UI 에 드러낸다.
	 * 운영자가 새로고침 한 번의 값을 청구서에서 발견하는 대신 화면에서 본다.
	 */
	bytesScanned: number;
	recordsMatched: number;
	truncated: boolean;
	elapsedMs: number;
}

/**
 * 설정되지 않은 로그 그룹에 쿼리를 요청했다는 뜻. 호출자가 오류 대신 설명하는
 * 패널을 그릴 수 있도록 따로 구분한다.
 */
export class NoLogGroupError extends Error {
	constructor() {
		super('no log group configured');
		this.name = 'NoLogGroupError';
	}
}

/**
 * 동시 실행 수를 묶는 세마포어.
 *
 * Go 는 버퍼 채널로 같은 일을 했다. 기다리는 동안 호출자가 사라지면 자리를
 * 기다리던 것도 함께 접어야 한다 — 안 그러면 아무도 읽지 않을 답을 위해 유료
 * 스캔이 시작된다.
 */
class Semaphore {
	private free: number;
	private readonly waiting: (() => void)[] = [];

	constructor(n: number) {
		this.free = n;
	}

	async acquire(signal?: AbortSignal): Promise<void> {
		if (signal?.aborted === true) throw signal.reason ?? new Error('aborted');
		if (this.free > 0) {
			this.free--;
			return;
		}
		await new Promise<void>((resolve, reject) => {
			const onAbort = () => {
				const i = this.waiting.indexOf(grant);
				if (i >= 0) this.waiting.splice(i, 1);
				reject(signal?.reason ?? new Error('aborted'));
			};
			const grant = () => {
				signal?.removeEventListener('abort', onAbort);
				resolve();
			};
			this.waiting.push(grant);
			signal?.addEventListener('abort', onAbort, { once: true });
		});
		this.free--;
	}

	release(): void {
		this.free++;
		const next = this.waiting.shift();
		if (next !== undefined) next();
	}
}

export interface InsightsRunnerOptions {
	/**
	 * 동시에 나는 쿼리 수의 상한. CloudWatch 는 계정당 서른 개쯤을 허용하고
	 * 여기서 한 페이지는 대여섯 개를 낸다. 넉넉히 아래에 두면 같은 키를 쓰는
	 * 다른 것에도 여유가 남는다.
	 */
	concurrency?: number;
	/** 쿼리 하나의 시한(ms). */
	timeoutMs?: number;
	/** 쿼리가 도는 동안 GetQueryResults 를 부르는 간격(ms). */
	pollIntervalMs?: number;
}

export class InsightsRunner {
	readonly api: CloudWatchLogsClient;
	private readonly sem: Semaphore;
	private readonly timeoutMs: number;
	private readonly pollIntervalMs: number;

	constructor(api: CloudWatchLogsClient, options: InsightsRunnerOptions = {}) {
		this.api = api;
		const n = options.concurrency !== undefined && options.concurrency > 0 ? options.concurrency : 6;
		this.sem = new Semaphore(n);
		this.timeoutMs =
			options.timeoutMs !== undefined && options.timeoutMs > 0 ? options.timeoutMs : 45_000;
		this.pollIntervalMs =
			options.pollIntervalMs !== undefined && options.pollIntervalMs > 0
				? options.pollIntervalMs
				: 250;
	}

	/** run 은 창 위에서 쿼리 하나를 돌리고 끝날 때까지 기다린다. */
	async run(logGroup: string, w: Window, q: Query, signal?: AbortSignal): Promise<QueryResult> {
		if (logGroup === '') throw new NoLogGroupError();

		await this.sem.acquire(signal);
		try {
			return await this.runHeld(logGroup, w, q, signal);
		} finally {
			this.sem.release();
		}
	}

	private async runHeld(
		logGroup: string,
		w: Window,
		q: Query,
		signal?: AbortSignal
	): Promise<QueryResult> {
		const deadline = AbortSignal.timeout(this.timeoutMs);
		const bounded = signal === undefined ? deadline : AbortSignal.any([signal, deadline]);

		const started = Date.now();
		let queryId = '';
		try {
			const out = await this.api.send(
				new StartQueryCommand({
					logGroupNames: [logGroup],
					startTime: Math.trunc(w.start / 1000),
					endTime: Math.trunc(w.end / 1000),
					queryString: q.text,
					...(q.limit > 0 ? { limit: q.limit } : {})
				}),
				sendOptions(bounded)
			);
			queryId = out.queryId ?? '';
		} catch (err) {
			throw new Error(`StartQuery ${q.id}: ${message(err)}`);
		}
		if (queryId === '') throw new Error(`StartQuery ${q.id} returned no query id`);

		try {
			const res = await this.poll(queryId, q, bounded);
			res.elapsedMs = Date.now() - started;
			return res;
		} catch (err) {
			// 쿼리는 CloudWatch 쪽에서 아직 돌고 있고 동시 실행 자리를 붙들고
			// 있다. 그것을 놓아주려면 살아 있는 신호가 필요하므로, 지금 풀려
			// 나가는 중인 것 대신 새 시한을 쓴다.
			this.stop(queryId);
			throw err;
		}
	}

	private async poll(queryId: string, q: Query, signal: AbortSignal): Promise<QueryResult> {
		for (;;) {
			let out;
			try {
				out = await this.api.send(new GetQueryResultsCommand({ queryId }), sendOptions(signal));
			} catch (err) {
				throw new Error(`GetQueryResults ${q.id}: ${message(err)}`);
			}

			switch (out.status) {
				case 'Complete':
					return buildResult(q, out.results ?? [], out.statistics);
				case 'Failed':
					throw new Error(`query ${q.id} failed`);
				case 'Cancelled':
					throw new Error(`query ${q.id} was cancelled`);
				case 'Timeout':
					throw new Error(`query ${q.id} timed out in CloudWatch`);
				default:
					break;
			}

			await wait(this.pollIntervalMs, signal, q.id);
		}
	}

	/**
	 * stop 은 CloudWatch 쪽 쿼리 자리를 놓아준다. 짧은 제 시한으로 돌고 오류는
	 * 무시한다 — 쿼리가 스스로 끝났을 수도 있고, 이미 사라진 것을 취소하지 못한
	 * 데 대해 할 수 있는 유용한 일이 없다.
	 */
	private stop(queryId: string): void {
		void this.api
			.send(new StopQueryCommand({ queryId }), sendOptions(AbortSignal.timeout(5_000)))
			.catch(() => {});
	}

	/**
	 * runAll 은 러너의 상한 안에서 모든 쿼리를 동시에 돌리고 결과를 쿼리 id 로
	 * 묶어 돌려준다.
	 *
	 * 쿼리 하나가 실패해도 나머지가 가라앉지 않는다. 오류는 그 id 앞으로
	 * 기록되므로, 그것이 먹이는 패널은 무엇이 잘못됐는지 말하면서 이웃 패널은
	 * 그대로 그려진다.
	 */
	async runAll(
		logGroup: string,
		w: Window,
		queries: Query[],
		signal?: AbortSignal
	): Promise<{ results: Map<string, QueryResult>; errors: Map<string, Error> }> {
		const results = new Map<string, QueryResult>();
		const errors = new Map<string, Error>();

		await Promise.all(
			queries.map(async (q) => {
				try {
					results.set(q.id, await this.run(logGroup, w, q, signal));
				} catch (err) {
					errors.set(q.id, err instanceof Error ? err : new Error(String(err)));
				}
			})
		);
		return { results, errors };
	}
}

function buildResult(
	q: Query,
	rows: { field?: string | undefined; value?: string | undefined }[][],
	statistics?: { bytesScanned?: number | undefined; recordsMatched?: number | undefined }
): QueryResult {
	const out: QueryResult = {
		id: q.id,
		rows: [],
		bytesScanned: statistics?.bytesScanned ?? 0,
		recordsMatched: statistics?.recordsMatched ?? 0,
		truncated: false,
		elapsedMs: 0
	};

	for (const row of rows) {
		const m: Record<string, string> = {};
		for (const f of row) {
			const name = f.field ?? '';
			if (name === '@ptr') continue;
			m[name] = f.value ?? '';
		}
		out.rows.push(m);
	}

	const limit = q.limit <= 0 || q.limit > insightsMaxRows ? insightsMaxRows : q.limit;
	out.truncated = out.rows.length >= limit;
	return out;
}

function wait(ms: number, signal: AbortSignal, queryID: string): Promise<void> {
	return new Promise((resolve, reject) => {
		const onAbort = () => {
			clearTimeout(timer);
			reject(new Error(`query ${queryID}: ${message(signal.reason)}`));
		};
		const timer = setTimeout(() => {
			signal.removeEventListener('abort', onAbort);
			resolve();
		}, ms);
		if (signal.aborted) {
			onAbort();
			return;
		}
		signal.addEventListener('abort', onAbort, { once: true });
	});
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}

/** totalBytesScanned 는 결과 묶음이 든 비용을 더한다. */
export function totalBytesScanned(results: Map<string, QueryResult>): number {
	let total = 0;
	for (const r of results.values()) total += r.bytesScanned;
	return total;
}

/**
 * sortedRows 는 결과의 행을 이름 붙인 필드로 정렬한다. CloudWatch 가 동점을
 * 다른 순서로 돌려줘도 표가 새로고침마다 뒤섞이지 않게 한다.
 */
export function sortedRows(
	rows: Record<string, string>[],
	field: string
): Record<string, string>[] {
	return [...rows].sort((a, b) => {
		const x = a[field] ?? '';
		const y = b[field] ?? '';
		return x < y ? -1 : x > y ? 1 : 0;
	});
}
