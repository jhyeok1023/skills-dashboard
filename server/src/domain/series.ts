// 응답의 모양. internal/domain/series.go 의 이식이다.
//
// 화면에 뜨는 모든 숫자가 여기를 지난다. 프론트는 포맷팅만 하고 다시 집계하지
// 않는다 — 이전 구현은 표시값을 뷰에서 재집계했고, 그래서 "요청 수" 가 두 곳에서
// 서로 다른 모집단을 셌다.
//
// 클래스마다 toJSON 을 두는 이유는 Go 의 omitempty 때문이다. 빈 값을 그냥 실어
// 보내면 `"color":""` 같은 키가 늘어 두 엔진의 응답이 바이트로 갈린다.

import type {
	Bars,
	Column,
	Intent,
	Panel as PanelJSON,
	Payload as PayloadJSON,
	Point,
	Row,
	Series as SeriesJSON,
	Stat,
	Table as TableJSON,
	Unit,
	WindowJSON
} from '../contract.ts';
import { buckets, periodSeconds, timestamps, compactDuration, type Window } from './window.ts';

export type { Bars, Column, Intent, Point, Row, Stat, Unit };

// 선 무늬. 실선이 기본값인 것은 의도다 — 자기 무늬에 대해 아무 말도 하지 않는
// 시리즈는 이것이 생기기 전에 모든 패널이 그리던 선을 그대로 얻는다.
export const dashSolid = '';
export const dashDashed = 'dashed';
export const dashDotted = 'dotted';

const variantDashes = [dashSolid, dashDashed, dashDotted];

/**
 * variantDash 는 패널 안에서 i 번째 메트릭에 제 나름의 선 무늬를 준다. 같은
 * 주체의 두 메트릭이 한 색을 유지하면서도 구분되게 하려는 것이다. 모자라면
 * 돌려 쓴다 — 한 차트에 메트릭이 셋을 넘는 패널은 무늬로 고칠 수 없는 이유로
 * 이미 읽을 수 없다.
 */
export function variantDash(i: number): string {
	const n = i < 0 ? 0 : i;
	return variantDashes[n % variantDashes.length] as string;
}

/**
 * Series 는 차트의 선 하나다. 감싸는 payload 의 timestamps 와 인덱스가 하나씩
 * 맞물린다.
 *
 * 값이 null 인 것은 진짜 구멍이고 uPlot 까지 null 로 간다 — 선이 끊겨 그려진다.
 * 구멍을 0 으로 메우면 "CloudWatch 에 아무것도 없었다" 가 "값이 0 이었다" 로
 * 바뀌고, 지연 차트에서 그것은 트래픽 붕괴로 읽힌다.
 */
export class Series {
	label: string;
	unit: Unit;
	color: string;
	dash: string;
	values: Point[];

	constructor(label: string, unit: Unit, color: string, n: number) {
		this.label = label;
		this.unit = unit;
		this.color = color;
		this.dash = dashSolid;
		this.values = new Array<Point>(n).fill(null);
	}

	/** 범위 밖 인덱스는 무시한다. 엉뚱한 CloudWatch 타임스탬프가 핸들러를 죽이지 못하게. */
	set(i: number, v: number): void {
		if (i < 0 || i >= this.values.length) return;
		this.values[i] = v;
	}

	/** 칸에 누적한다. 구멍은 0 으로 본다. 여러 원본 행이 한 칸에 떨어지는 개수에 쓴다. */
	add(i: number, v: number): void {
		if (i < 0 || i >= this.values.length) return;
		const cur = this.values[i];
		this.values[i] = cur === null || cur === undefined ? v : cur + v;
	}

	/** 구멍이 아닌 표본의 수. */
	defined(): number {
		return this.values.reduce<number>((n, v) => (v === null ? n : n + 1), 0);
	}

	// max·min·sum·avg·last 는 시리즈를 숫자 하나로 줄인다. 전부 구멍이면 null 이다.
	//
	// 대시보드의 모든 대표 숫자가 이 중 하나를 지난다. 그게 요점이다 — 오버뷰
	// 타일과 상세 차트가 같은 시리즈의 같은 축약을 읽으므로, 이전 구현의 손으로
	// 쓴 클라이언트 축약들처럼 어긋날 수가 없다.
	max(): Point {
		return this.reduce(Math.max);
	}
	min(): Point {
		return this.reduce(Math.min);
	}
	sum(): Point {
		return this.reduce((a, b) => a + b);
	}

	avg(): Point {
		let sum = 0;
		let n = 0;
		for (const v of this.values) {
			if (v === null) continue;
			sum += v;
			n++;
		}
		return n === 0 ? null : sum / n;
	}

	/**
	 * last 는 가장 최근의 정의된 표본이다. newWindow 가 끝을 주기 경계로
	 * 내리므로 마지막 칸은 완성된 칸이다 — 이전 구현이 제 반쯤 찬 꼬리를
	 * 피하려고 쓰던 "끝에서 두 번째 칸" 같은 편법이 필요 없다.
	 */
	last(): Point {
		for (let i = this.values.length - 1; i >= 0; i--) {
			const v = this.values[i];
			if (v !== null && v !== undefined) return v;
		}
		return null;
	}

	private reduce(f: (a: number, b: number) => number): Point {
		let acc: number | null = null;
		for (const v of this.values) {
			if (v === null || v === undefined) continue;
			acc = acc === null ? v : f(acc, v);
		}
		return acc;
	}

	// 키 순서는 Go 의 구조체 선언 순서를 따른다: label, unit, color, dash, values.
	toJSON(): SeriesJSON {
		return {
			label: this.label,
			unit: this.unit,
			...(this.color !== '' ? { color: this.color } : {}),
			...(this.dash !== '' ? { dash: this.dash } : {}),
			values: this.values
		};
	}
}

/** newSeries 는 구멍 n 개짜리 시리즈를 만든다. 인덱스로 채워 넣으면 된다. */
export function newSeries(label: string, unit: Unit, color: string, n: number): Series {
	return new Series(label, unit, color, n);
}

/**
 * newTable 은 행과, 그 행과 무관하게 센 총계를 짝지어 준다.
 *
 * total 은 rows 에서 유도하지 않는다. 이전 구현은 SQL 이 300개로 자른 배열의
 * length 를 총계로 표시했고, 그래서 대표 숫자가 300에서 조용히 세기를 멈추고
 * 옆의 집계와 어긋났다. 호출자에게 total 을 따로 내라고 요구하는 것이 그 모양의
 * 버그를 다시 쓰지 못하게 막는다.
 */
export function newTable(
	columns: Column[],
	rows: Row[] | null,
	total: number,
	limit: number
): TableJSON {
	const list = rows ?? [];
	return { columns, rows: list, total, truncated: total > list.length, limit };
}

/** Panel 은 대시보드의 카드 하나다: 차트, 대표 숫자, 그리고 선택적으로 표. */
export class Panel {
	id: string;
	title: string;
	series: Series[] = [];
	stats: Stat[] = [];
	table: TableJSON | null = null;
	bars: Bars | null = null;
	warnings: string[] = [];

	constructor(id: string, title: string) {
		this.id = id;
		this.title = title;
	}

	/** UI 가 삼키지 말고 드러내야 할 단서를 붙인다. */
	warn(message: string): void {
		this.warnings.push(message);
	}

	toJSON(): PanelJSON {
		const out: PanelJSON = { id: this.id, title: this.title };
		if (this.series.length > 0) out.series = this.series.map((s) => s.toJSON());
		if (this.stats.length > 0) out.stats = this.stats;
		if (this.table !== null) out.table = this.table;
		if (this.bars !== null) out.bars = this.bars;
		if (this.warnings.length > 0) out.warnings = this.warnings;
		return out;
	}
}

/** windowJSON 은 창을 와이어 형태로 만든다. */
export function windowJSON(w: Window): WindowJSON {
	return {
		start: Math.trunc(w.start / 1000),
		end: Math.trunc(w.end / 1000),
		period: periodSeconds(w.period),
		range: compactDuration(w.range),
		timestamps: timestamps(w)
	};
}

/**
 * Payload 는 모든 데이터 엔드포인트가 내는 것이다. 단일 패널 엔드포인트는 패널
 * 하나를, 페이지 엔드포인트는 여럿을 내되 — 전부 하나의 창 아래다. 한 응답의 두
 * 패널이 서로 다른 구간을 설명하는 일을 막는 장치가 그것이다.
 */
export class Payload {
	window: WindowJSON;
	panels: Panel[] = [];
	warnings: string[] = [];

	constructor(w: Window) {
		this.window = windowJSON(w);
	}

	add(...panels: Panel[]): this {
		this.panels.push(...panels);
		return this;
	}

	warn(message: string): void {
		this.warnings.push(message);
	}

	toJSON(): PayloadJSON {
		const out: PayloadJSON = { window: this.window, panels: this.panels.map((p) => p.toJSON()) };
		if (this.warnings.length > 0) out.warnings = this.warnings;
		return out;
	}
}

/**
 * validatePayload 는 프론트가 기대는 불변식을 확인한다. 핸들러가 응답을 쓰기
 * 전에 부르므로, 시간축과 어긋난 시리즈는 조용히 밀린 데이터를 그리는 대신
 * 여기서 큰 소리로 실패한다.
 */
export function validatePayload(p: Payload): void {
	const n = p.window.timestamps.length;
	if (n === 0) throw new Error('payload has no timestamps');

	for (const panel of p.panels) {
		for (const s of panel.series) {
			if (s.values.length !== n) {
				throw new Error(
					`panel "${panel.id}" series "${s.label}" has ${s.values.length} values, want ${n}`
				);
			}
		}
		const t = panel.table;
		if (t !== null) {
			if (t.total < t.rows.length) {
				throw new Error(
					`panel "${panel.id}" table reports total ${t.total} below its ${t.rows.length} rows`
				);
			}
			const want = t.total > t.rows.length;
			if (t.truncated !== want) {
				throw new Error(
					`panel "${panel.id}" table truncated=${t.truncated} disagrees with total ${t.total} over ${t.rows.length} rows`
				);
			}
		}
	}
}

/** newPayload 는 w 를 위한 payload 를 시작한다. */
export function newPayload(w: Window): Payload {
	return new Payload(w);
}

/** 창의 칸 수. 시리즈를 만들 때 길이로 쓴다. */
export function bucketsOf(w: Window): number {
	return buckets(w);
}
