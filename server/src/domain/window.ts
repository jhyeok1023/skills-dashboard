// 시간 창을 고르는 규칙. internal/domain/window.go 의 이식이다.
//
// Go 는 time.Duration(나노초)으로 다뤘고 여기서는 밀리초로 다룬다. JS 의 Date
// 가 밀리초라 그쪽에 맞추는 편이 변환을 없앤다.

import { parseGoDuration } from './logfmt.ts';

const minute = 60_000;
const hour = 60 * minute;

/** 화면이 얼마나 뒤까지 보는지. 네 시간은 제품이 정한 단단한 상한이다. */
export type Range = number;
/** 시간축에서 한 칸의 너비. */
export type Period = number;

export const range15m: Range = 15 * minute;
export const range30m: Range = 30 * minute;
export const range1h: Range = hour;
export const range2h: Range = 2 * hour;
export const range4h: Range = 4 * hour;

export const period1m: Period = minute;
export const period5m: Period = 5 * minute;
export const period10m: Period = 10 * minute;
export const period1h: Period = hour;

/**
 * maxRange 는 모든 로그·메트릭 조회에 제품이 씌우는 천장이다. 창을 묶어 두는
 * 것이 Insights 스캔량을 예측 가능하게 만들므로, 이것은 UI 의 가장 큰 선택지로
 * 제시되는 데 그치지 않고 서버에서 강제된다.
 */
export const maxRange = 4 * hour;

// 창은 시계열로 읽힐 만큼 칸이 많아야 하고, 브라우저에 잡음을 그리게 하지 않을
// 만큼 적어야 한다. 양쪽 모두 포함이다.
const minBuckets = 4;
const maxBuckets = 250;

const allRanges: Range[] = [range15m, range30m, range1h, range2h, range4h];
const allPeriods: Period[] = [period1m, period5m, period10m, period1h];

/** 해상도를 살리되 칸 수를 얌전하게 두는 기본값. */
const defaultPeriods = new Map<Range, Period>([
	[range15m, period1m],
	[range30m, period1m],
	[range1h, period1m],
	[range2h, period1m],
	[range4h, period5m]
]);

/** 와이어와 URL 에서 쓰는 짧은 표기: "15m", "4h". */
export function compactDuration(ms: number): string {
	if (ms % hour === 0) return `${Math.trunc(ms / hour)}h`;
	if (ms % minute === 0) return `${Math.trunc(ms / minute)}m`;
	return `${Math.trunc(ms / 1000)}s`;
}

/** CloudWatch 의 Period 파라미터가 기대하는 단위. */
export function periodSeconds(p: Period): number {
	return Math.trunc(p / 1000);
}

/** 고를 수 있는 범위 전부. 짧은 것부터. */
export function ranges(): Range[] {
	return [...allRanges];
}

function joinDurations(list: number[]): string {
	return list.map(compactDuration).join(', ');
}

/**
 * parseRange 는 짧은 표기를 받고 정해진 집합 밖은 거절한다. 자유 형식 기간을
 * 일부러 지원하지 않는다 — 임의의 범위는 반올림 오차로 maxRange 를 빠져나갈
 * 여지를 주고, 범위·주기 호환표를 무한히 만든다.
 */
export function parseRange(s: string): Range {
	for (const r of allRanges) {
		if (compactDuration(r) === s) return r;
	}
	const ns = parseGoDuration(s);
	if (ns === null) throw new Error(`unknown range "${s}"`);
	if (ns / 1e6 > maxRange) {
		throw new Error(`range ${s} exceeds the ${compactDuration(maxRange)} maximum`);
	}
	throw new Error(`unsupported range "${s}"; allowed: ${joinDurations(allRanges)}`);
}

/** parsePeriod 는 칸 너비의 짧은 표기를 받는다. */
export function parsePeriod(s: string): Period {
	for (const p of allPeriods) {
		if (compactDuration(p) === s) return p;
	}
	throw new Error(`unsupported period "${s}"; allowed: ${joinDurations(allPeriods)}`);
}

function bucketCount(r: Range, p: Period): number {
	if (p <= 0) return 0;
	return Math.trunc(r / p);
}

/**
 * periodsFor 는 r 에 대해 말이 되는 칸 수를 내는 너비를 나열한다. UI 선택기를
 * 이것으로 만들므로, 프론트는 서버가 거절할 조합을 제시할 수 없다.
 */
export function periodsFor(r: Range): Period[] {
	return allPeriods.filter((p) => {
		const n = bucketCount(r, p);
		return n >= minBuckets && n <= maxBuckets;
	});
}

/** 요청이 주기를 빼먹었을 때 쓰는 칸 너비. */
export function defaultPeriod(r: Range): Period {
	const p = defaultPeriods.get(r);
	if (p !== undefined) return p;
	// minBuckets 를 넘기는 가장 성긴 주기로 물러난다.
	const valid = periodsFor(r);
	return valid.length === 0 ? period1m : (valid[valid.length - 1] as Period);
}

/**
 * Window 는 한 응답의 모든 패널이 공유하는 단 하나의 시간 창이다.
 *
 * 이전 구현은 패널마다 제 나름의 now 를 잡았다. 그래서 한 대시보드가 몇 초씩
 * 어긋나고 칸도 다르게 잘린 창을 설명하는 메트릭 차트와 로그 차트를 함께
 * 보여 줄 수 있었다. 요청의 가장자리에서 창을 한 번 만들어 아래로 꿰는 것이,
 * 두 패널이 — 그리고 같은 패널의 오버뷰와 상세가 — 일치하게 만드는 방법이다.
 *
 * start·end 는 epoch 밀리초다.
 */
export interface Window {
	start: number;
	end: number;
	range: Range;
	period: Period;
}

/**
 * newWindow 는 now 기준으로 r/p 의 창을 만든다.
 *
 * end 를 주기 경계로 내리고 start 를 거기서 끌어내므로, 창 안의 모든 칸이 완성된
 * 칸이다. 이전 구현은 조회 경계에 날것의 now 를 쓰면서 칸은 (ts/너비)*너비 로
 * 잘랐고, 그래서 가장 최근 칸은 반쯤 차고 가장 오래된 칸은 잘려 있었다. UI 는
 * 한 곳에서는 끝에서 두 번째 칸을, 다른 곳에서는 마지막 칸을 읽는 것으로 그것을
 * 우회했는데, 같은 숫자가 한 화면의 두 자리에서 달라진 경위가 바로 그것이다.
 */
export function newWindow(now: Date, r: Range, p: Period): Window {
	validateWindow(r, p);
	// Go 의 time.Truncate 는 서기 1년 기준으로 내림한다. epoch 는 그 기준에서
	// 날짜 단위로 떨어지고 여기 쓰는 주기는 모두 하루를 정확히 나누므로,
	// epoch 밀리초를 내림하는 것과 결과가 같다.
	const end = Math.floor(now.getTime() / p) * p;
	return { start: end - r, end, range: r, period: p };
}

/** validateWindow 는 r 과 p 를 함께 쓸 수 있는지 본다. */
export function validateWindow(r: Range, p: Period): void {
	if (r <= 0) throw new Error('range must be positive');
	if (r > maxRange) {
		throw new Error(`range ${compactDuration(r)} exceeds the ${compactDuration(maxRange)} maximum`);
	}
	if (p <= 0) throw new Error('period must be positive');

	const n = bucketCount(r, p);
	if (n < minBuckets) {
		throw new Error(
			`period ${compactDuration(p)} over range ${compactDuration(r)} yields ${n} buckets, fewer than the ${minBuckets} minimum`
		);
	}
	if (n > maxBuckets) {
		throw new Error(
			`period ${compactDuration(p)} over range ${compactDuration(r)} yields ${n} buckets, more than the ${maxBuckets} maximum`
		);
	}
	if (r % p !== 0) {
		throw new Error(
			`period ${compactDuration(p)} does not divide range ${compactDuration(r)} evenly`
		);
	}
}

/** 시간축의 점 개수. */
export function buckets(w: Window): number {
	return bucketCount(w.range, w.period);
}

/**
 * timestamps 는 모든 칸의 왼쪽 경계를 Unix 초로 오름차순 나열한다. 프론트가
 * 그리는 x 축이고, 응답의 모든 시계열이 맞춰지는 인덱스다.
 */
export function timestamps(w: Window): number[] {
	const n = buckets(w);
	const step = w.period / 1000;
	const start = Math.trunc(w.start / 1000);
	const out: number[] = new Array<number>(n);
	for (let i = 0; i < n; i++) out[i] = start + i * step;
	return out;
}

/**
 * indexOf 는 t 가 들어가는 칸과, 애초에 창 안에 들어가기는 하는지를 준다. 밖의
 * 점은 자르지 않고 버린다 — 자르면 창 밖 데이터가 가장자리 칸에 조용히 쌓여
 * 그 칸을 부풀린다.
 */
export function indexOf(w: Window, t: Date | number): number | null {
	const ms = typeof t === 'number' ? t : t.getTime();
	if (ms < w.start || ms >= w.end) return null;
	return Math.trunc((ms - w.start) / w.period);
}

/** contains 는 t 가 창 안에 드는지 답한다. */
export function contains(w: Window, t: Date | number): boolean {
	return indexOf(w, t) !== null;
}

/** align 은 t 를 그 칸의 왼쪽 경계로 내린다. */
export function align(w: Window, t: Date | number): number {
	const ms = typeof t === 'number' ? t : t.getTime();
	return Math.floor(ms / w.period) * w.period;
}

/**
 * windowsEqual 은 두 창이 같은 구간을 같은 해상도로 말하는지 본다. 핸들러가
 * 조립한 패널을 가로질러 이것을 확인한다.
 */
export function windowsEqual(a: Window, b: Window): boolean {
	return a.start === b.start && a.end === b.end && a.period === b.period;
}

export function windowString(w: Window): string {
	return `${new Date(w.start).toISOString()}..${new Date(w.end).toISOString()}/${compactDuration(w.period)}`;
}

/**
 * resolveWindow 는 쿼리 문자열의 날값을 창으로 바꾸고 기본값을 채운다. 범위가
 * 비어 있으면 1시간, 주기가 비어 있으면 그 범위의 기본 주기다.
 */
export function resolveWindow(now: Date, rangeStr: string, periodStr: string): Window {
	const r = rangeStr === '' ? range1h : parseRange(rangeStr);
	const p = periodStr === '' ? defaultPeriod(r) : parsePeriod(periodStr);
	return newWindow(now, r, p);
}
