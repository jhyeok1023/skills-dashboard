// Go 의 slog TextHandler 와 같은 줄을 낸다.
//
//   time=2026-08-24T20:12:33.123+09:00 level=INFO msg="dashboard is listening" url=http://…
//
// 형식을 맞추는 데는 이유가 있다. 대회장 리허설 절차(README)가 로그 줄을 눈으로
// 대조한다 — `dashboard is listening` 으로 실제 포트를 찾고, `reading
// credentials envFile=…` 으로 .env 가 제대로 넘어갔는지 본다. 엔진을 바꿨다고
// 그 절차가 달라지면 두 트랙을 같은 방법으로 점검할 수 없다.

export type LogValue = string | number | boolean | null | undefined | Error | readonly string[];

const levels = { DEBUG: 10, INFO: 20, WARN: 30, ERROR: 40 } as const;

export type Level = keyof typeof levels;

let threshold: number = levels.INFO;

export function setVerbose(verbose: boolean): void {
	threshold = verbose ? levels.DEBUG : levels.INFO;
}

// slog 는 공백·따옴표·`=` 가 들어 있거나 빈 값일 때만 따옴표를 두른다.
function quote(raw: string): string {
	if (raw === '') return '""';
	return /[\s"=]/.test(raw) ? JSON.stringify(raw) : raw;
}

function render(value: LogValue): string {
	if (value instanceof Error) return quote(value.message);
	if (value === null || value === undefined) return '<nil>';
	// slog 는 []string 을 fmt 의 %v 로 낸다. 대괄호에 공백 구분이다.
	if (Array.isArray(value)) return quote(`[${value.join(' ')}]`);
	return quote(String(value));
}

// RFC3339 + 밀리초 + 로컬 오프셋. Date 의 toISOString 은 UTC 라 쓸 수 없다.
function stamp(now: Date): string {
	const pad = (n: number, width = 2) => String(n).padStart(width, '0');
	const offset = -now.getTimezoneOffset();
	const sign = offset >= 0 ? '+' : '-';
	const abs = Math.abs(offset);
	return (
		`${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}` +
		`T${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}` +
		`.${pad(now.getMilliseconds(), 3)}` +
		`${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
	);
}

function emit(level: Level, msg: string, attrs: Record<string, LogValue>): void {
	if (levels[level] < threshold) return;
	const pairs = Object.entries(attrs).map(([key, value]) => `${key}=${render(value)}`);
	process.stderr.write(
		[`time=${stamp(new Date())}`, `level=${level}`, `msg=${quote(msg)}`, ...pairs].join(' ') + '\n'
	);
}

export const logger = {
	debug: (msg: string, attrs: Record<string, LogValue> = {}) => emit('DEBUG', msg, attrs),
	info: (msg: string, attrs: Record<string, LogValue> = {}) => emit('INFO', msg, attrs),
	warn: (msg: string, attrs: Record<string, LogValue> = {}) => emit('WARN', msg, attrs),
	error: (msg: string, attrs: Record<string, LogValue> = {}) => emit('ERROR', msg, attrs)
};
