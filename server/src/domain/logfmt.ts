// 팟 로그 그룹의 한 줄을 어떻게 읽을지 기술한다.
//
// internal/domain/logfmt.go 의 이식이다. 필드 이름이 전부 설정 가능한 이유는
// 애플리케이션 로그 모양이 아직 정해지는 중이기 때문이다. 기본값은 실제
// Container Insights 레코드에서 읽어 온 것이다 — fluent-bit 가 컨테이너 한 줄을
// 봉투에 싸고, 그 줄이 JSON 이면 해독한 필드를 log_processed 아래에 넣는다.
// 액세스 로그가 아닌 줄은 평문으로 오므로 두 경로가 다 동작해야 한다.

import type { LogFormat, LogLine, Unit } from '../contract.ts';
import { compileGoRegexp, execNamed, maxSubjectLength } from './goregexp.ts';

export type LogPreset = LogFormat['preset'];

/** Container Insights 레코드에 맞춘 기본값. */
export function defaultLogFormat(): LogFormat {
	return {
		preset: 'auto',
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
		userAgentField: '',

		textPattern: '',
		levelPattern: '(?i)\\b(error|err|fatal|panic|warn|warning|oomkilled)\\b',
		namespace: 'default',
		okStatuses: [200, 201],
		excludePaths: defaultExcludePaths()
	};
}

/** 따로 설정하지 않으면 제외되는 프로브 엔드포인트. */
export function defaultExcludePaths(): string[] {
	return ['/health', '/healthcheck'];
}

/**
 * 컴파일된 형식. Go 는 패턴을 구조체 안에 캐시했지만 여기서는 따로 든다 —
 * LogFormat 자체가 그대로 JSON 으로 나가는 값이라, 런타임 물건을 얹고 싶지 않다.
 */
export interface CompiledLogFormat {
	format: LogFormat;
	text: RegExp | null;
	level: RegExp | null;
}

/** compileLogFormat 은 설정된 패턴을 검사하고 컴파일한다. 실패하면 던진다. */
export function compileLogFormat(f: LogFormat): CompiledLogFormat {
	return {
		format: f,
		level: f.levelPattern === '' ? null : compileGoRegexp('levelPattern', f.levelPattern),
		text: f.textPattern === '' ? null : compileGoRegexp('textPattern', f.textPattern)
	};
}

const presets: LogPreset[] = ['auto', 'gin', 'json'];
const latencyUnits: Unit[] = ['ms', 's', ''];

/** validateLogFormat 은 이 형식을 쓸 수 있는지 본다. 못 쓰면 던진다. */
export function validateLogFormat(f: LogFormat): CompiledLogFormat {
	if (!presets.includes(f.preset)) {
		throw new Error(`preset "${f.preset}" must be auto, gin, or json`);
	}
	if (f.timeField === '') throw new Error('timeField must be set');
	if (f.messageField === '') throw new Error('messageField must be set');
	if (!latencyUnits.includes(f.latencyUnit)) {
		throw new Error(`latencyUnit "${f.latencyUnit}" must be ms, s, or empty`);
	}
	return compileLogFormat(f);
}

/**
 * 파싱된 한 줄. 지연은 여기서 밀리초로 통일한다 — 아래쪽 어느 소비자도 원본
 * 단위가 무엇이었는지 알 필요가 없게 하려는 것이다.
 *
 * 시각만 계약과 다르다. 바깥으로 나갈 때는 문자열이지만 안에서는 Date 로 든다.
 */
export type ParsedLogLine = Omit<LogLine, 'timestamp'> & { timestamp: Date };

function emptyLine(timestamp: Date, message: string): ParsedLogLine {
	return {
		timestamp,
		app: '',
		pod: '',
		namespace: '',
		container: '',
		stream: '',
		method: '',
		path: '',
		requestTarget: '',
		clientIp: '',
		status: 0,
		latencyMs: null,
		level: '',
		message,
		hasAccess: false
	};
}

/** 바깥으로 내보낼 때의 모양. Go 의 time.Time 은 RFC3339 문자열로 마샬됐다. */
export function toWire(line: ParsedLogLine): LogLine {
	return { ...line, timestamp: formatRFC3339Nano(line.timestamp) };
}

/**
 * formatRFC3339Nano 는 Go 의 time.Time 이 JSON 으로 나가는 모양을 낸다.
 *
 * toISOString 과 다른 점은 소수부다. Go 의 RFC3339Nano 는 뒤따르는 0 을 지우고,
 * 소수부가 통째로 0 이면 아예 쓰지 않는다 — `…:00Z` 이지 `…:00.000Z` 가 아니다.
 * 값으로는 같지만 두 엔진의 응답을 바이트로 비교할 때는 갈린다.
 *
 * 나노초까지 가지 못하는 것은 남는 차이다. Date 는 밀리초가 한계이므로, 원본
 * 로그가 그보다 정밀한 시각을 실어 보내면 Go 는 그것을 그대로 되돌려 주고
 * 이쪽은 밀리초에서 자른다.
 */
export function formatRFC3339Nano(d: Date): string {
	const iso = d.toISOString(); // 2026-08-24T11:00:00.000Z
	const fraction = iso.slice(20, 23).replace(/0+$/, '');
	return fraction === '' ? iso.slice(0, 19) + 'Z' : iso.slice(0, 20) + fraction + 'Z';
}

/**
 * isExcludedPath 는 이 요청 경로가 팟 로그 패널에서 걸러지는지 본다. 쿼리 쪽
 * 필터와 정확히 같아야 설정 화면의 미리보기가 진실을 말한다.
 */
export function isExcludedPath(f: LogFormat, path: string): boolean {
	if (path === '') return false;
	return f.excludePaths.some((p) => p !== '' && p === path);
}

/** isBadStatus 는 응답 코드가 운영자가 정상으로 본 집합 밖인지 본다. */
export function isBadStatus(f: LogFormat, status: number): boolean {
	if (status <= 0) return false;
	return !f.okStatuses.includes(status);
}

/**
 * parseLogLine 은 원본 레코드 하나를 읽는다. fallback 은 봉투에 쓸 만한 시각이
 * 없을 때 쓴다 — CloudWatch 이벤트 시각을 넘긴다.
 *
 * 어느 시계가 이기는지가 중요하다. 봉투 자신의 시각이다. 이전 구현은 봉투
 * 시각을 저장하면서 적재 워터마크는 CloudWatch 이벤트 시각으로 올렸다. 그래서
 * 워터마크보다 뒤처진 시각으로 기록된 레코드가 이후 모든 조회 창 밖으로
 * 떨어졌다. 시계를 하나만 읽으면 그 부류의 어긋남이 사라진다.
 */
export function parseLogLine(cf: CompiledLogFormat, raw: string, fallback: Date): ParsedLogLine {
	const f = cf.format;
	const line = emptyLine(fallback, raw.trim());

	const env = parseJSONObject(raw);
	if (env === null) {
		// 봉투가 아예 아니다. 전체를 평문 메시지로 본다.
		applyText(cf, line, line.message);
		applyLevel(cf, line);
		return line;
	}

	const timeValue = env[f.timeField];
	if (typeof timeValue === 'string') {
		const ts = parseTimestamp(timeValue);
		if (ts !== null) line.timestamp = ts;
	}
	const streamValue = env[f.streamField];
	if (typeof streamValue === 'string') line.stream = streamValue;

	const k8s = env['kubernetes'];
	if (isObject(k8s)) {
		line.pod = str(k8s['pod_name']);
		line.namespace = str(k8s['namespace_name']);
		line.container = str(k8s['container_name']);
	}

	let msg = '';
	const msgValue = env[f.messageField];
	if (typeof msgValue === 'string') msg = msgValue;
	else if (msgValue !== undefined) msg = JSON.stringify(msgValue).trim();
	if (msg !== '') line.message = msg.trim();

	let processed: Record<string, unknown> = {};
	const processedValue = env[f.processedField];
	if (isObject(processedValue)) processed = processedValue;

	if (Object.keys(processed).length === 0 && msg !== '') {
		// fluent-bit 가 안쪽 줄을 해독하지 않았다. 직접 해 본다 — JSON 파서
		// 필터가 없는 클러스터에서도 액세스 필드가 나오게 하려는 것이다.
		const inner = parseJSONObject(msg);
		if (inner !== null) processed = inner;
	}

	if (Object.keys(processed).length > 0 && f.preset !== 'gin') {
		applyProcessed(f, line, processed);
	} else {
		applyText(cf, line, line.message);
	}

	if (line.app === '') line.app = line.container;
	applyLevel(cf, line);
	return line;
}

function applyProcessed(f: LogFormat, line: ParsedLogLine, m: Record<string, unknown>): void {
	line.app = firstString(line.app, str(m[f.appField]));
	line.method = firstString(line.method, str(m[f.methodField]));
	const target = str(m[f.pathField]);
	line.requestTarget = firstString(line.requestTarget, target);
	line.path = firstString(line.path, requestPath(target));
	line.clientIp = firstString(line.clientIp, str(m[f.clientIpField]));
	line.level = firstString(line.level, str(m[f.levelField]).toLowerCase());

	const status = num(m[f.statusField]);
	if (status !== null) {
		line.status = Math.trunc(status);
		line.hasAccess = true;
	}
	const latency = num(m[f.latencyField]);
	if (latency !== null) {
		line.latencyMs = toMillis(f, latency);
		line.hasAccess = true;
	}
}

function applyText(cf: CompiledLogFormat, line: ParsedLogLine, msg: string): void {
	const f = cf.format;
	if ((f.preset === 'auto' || f.preset === 'gin') && applyGin(line, msg)) return;
	if (cf.text === null || msg === '') return;

	const m = execNamed(cf.text, msg);
	if (m === null || !m.groups) return;

	for (const [name, value] of Object.entries(m.groups)) {
		if (value === undefined || value === '') continue;

		// 순서가 Go 의 switch 와 같아야 한다. 필드 이름이 고정 별칭과 겹칠 때
		// 어느 쪽이 이기는지가 그 순서로 정해진다.
		if (name === f.appField || name === 'app') {
			line.app = firstString(line.app, value);
		} else if (name === f.methodField || name === 'method') {
			line.method = firstString(line.method, value);
		} else if (name === f.pathField || name === 'path') {
			line.requestTarget = firstString(line.requestTarget, value);
			line.path = firstString(line.path, requestPath(value));
		} else if (name === f.clientIpField || name === 'client_ip' || name === 'clientIp') {
			line.clientIp = firstString(line.clientIp, value);
		} else if (name === f.levelField || name === 'level') {
			line.level = firstString(line.level, value.toLowerCase());
		} else if (name === f.statusField || name === 'status') {
			const v = atoi(value);
			if (v !== null) {
				line.status = v;
				line.hasAccess = true;
			}
		} else if (name === f.latencyField || name === 'latency' || name === 'latency_ms') {
			const v = parseFloatStrict(value);
			if (v !== null) {
				line.latencyMs = toMillis(f, v);
				line.hasAccess = true;
			}
		}
	}
}

const ansiPattern = /\x1b\[[0-9;]*m/g;

// Go 의 ginPattern 을 그대로 옮긴 것이다. 이름 있는 그룹만 (?P<x>) 에서
// (?<x>) 로 바뀐다.
const ginPattern =
	/^\[GIN\]\s+\d{4}\/\d{2}\/\d{2}\s+-\s+\d{2}:\d{2}:\d{2}\s+\|\s*(?<status>\d{3})\s*\|\s*(?<latency>(?:\d+h)?(?:\d+m)?[\d.]+(?:ns|µs|μs|us|ms|s))\s*\|\s*(?<client_ip>\S+)\s*\|\s*(?<method>[A-Z]+)\s+(?:"(?<quoted_target>(?:\\.|[^"])*)"|(?<plain_target>\S+))/;

function applyGin(line: ParsedLogLine, msg: string): boolean {
	const stripped = msg.replace(ansiPattern, '');
	const m = execNamed(ginPattern, stripped);
	if (m === null || !m.groups) return false;

	const g = m.groups;
	line.status = atoi(g['status'] ?? '') ?? 0;
	line.method = g['method'] ?? '';
	line.clientIp = g['client_ip'] ?? '';

	let target = g['plain_target'] ?? '';
	const quoted = g['quoted_target'] ?? '';
	if (quoted !== '') {
		// Go 는 strconv.Unquote 를 썼다. JSON 이 받아 주는 이스케이프는 그보다
		// 좁으므로, 못 풀면 원문을 그대로 둔다.
		try {
			target = JSON.parse('"' + quoted + '"') as string;
		} catch {
			target = quoted;
		}
	}
	line.requestTarget = target;
	line.path = requestPath(target);
	line.hasAccess = true;

	const ns = parseGoDuration(g['latency'] ?? '');
	if (ns !== null) line.latencyMs = ns / 1e6;
	return true;
}

export function requestPath(target: string): string {
	const i = target.indexOf('?');
	return i >= 0 ? target.slice(0, i) : target;
}

/**
 * applyLevel 은 레벨이 없는 줄을 본문으로 분류한다. 이미 상태 코드를 보고한
 * 요청 줄은 건드리지 않는다 — URL 안의 "error" 라는 낱말이 200 을 에러 로그로
 * 승격시켜서는 안 된다.
 */
function applyLevel(cf: CompiledLogFormat, line: ParsedLogLine): void {
	switch (line.level.toLowerCase()) {
		case 'error':
		case 'err':
		case 'fatal':
		case 'panic':
			line.level = 'error';
			return;
		case 'warn':
		case 'warning':
			line.level = 'warn';
			return;
		case '':
			break;
		default:
			line.level = line.level.toLowerCase();
			return;
	}

	if (line.hasAccess || cf.level === null) return;
	const m = execNamed(cf.level, line.message);
	if (m === null || m[0] === '') return;
	line.level = m[0].toLowerCase().startsWith('w') ? 'warn' : 'error';
}

function toMillis(f: LogFormat, v: number): number {
	return f.latencyUnit === 's' ? v * 1000 : v;
}

// --- 값 읽기 도우미 ------------------------------------------------------
//
// Go 의 encoding/json 과 strconv 가 하던 판단을 옮긴 것이다. JS 의 Number() 는
// Go 의 ParseFloat 보다 훨씬 관대해서(빈 문자열은 0, "0x10" 은 16) 그대로 쓰면
// 로그에 섞인 쓰레기가 숫자로 둔갑한다.

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function parseJSONObject(raw: string): Record<string, unknown> | null {
	if (raw.length > maxSubjectLength) return null;
	try {
		const v: unknown = JSON.parse(raw);
		return isObject(v) ? v : null;
	} catch {
		return null;
	}
}

function str(v: unknown): string {
	if (typeof v === 'string') return v;
	if (v === null || v === undefined) return '';
	if (typeof v === 'number' || typeof v === 'boolean') return String(v);
	return JSON.stringify(v);
}

const decimal = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;
const integer = /^[+-]?\d+$/;

export function parseFloatStrict(s: string): number | null {
	const t = s.trim();
	if (!decimal.test(t)) return null;
	const v = Number(t);
	return Number.isFinite(v) ? v : null;
}

export function atoi(s: string): number | null {
	const t = s.trim();
	if (!integer.test(t)) return null;
	const v = Number(t);
	return Number.isSafeInteger(v) ? v : null;
}

function num(v: unknown): number | null {
	if (typeof v === 'number') return Number.isFinite(v) ? v : null;
	if (typeof v === 'string') return parseFloatStrict(v);
	return null;
}

function firstString(cur: string, next: string): string {
	return cur !== '' ? cur : next;
}

// --- 시각과 기간 ---------------------------------------------------------

const timestampWithZone =
	/^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2}(?:\.\d+)?)(Z|[+-]\d{2}:?\d{2})$/;
const timestampNaive = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2}(?:\.\d+)?)$/;
const epochMillis = /^\d+$/;

/**
 * parseTimestamp 은 Go 가 시도하던 네 가지 형식과 epoch 밀리초를 읽는다.
 *
 * 시간대가 없는 형태를 UTC 로 보는 것이 중요하다. Go 의 time.Parse 가 그렇게
 * 했고, JS 의 Date 는 반대로 로컬 시간으로 읽는다 — 그대로 두면 KST 기계에서
 * 아홉 시간이 어긋난다.
 */
export function parseTimestamp(s: string): Date | null {
	const t = s.trim();

	const zoned = timestampWithZone.exec(t);
	if (zoned) {
		const d = new Date(`${zoned[1]}T${zoned[2]}${zoned[3]}`);
		return Number.isNaN(d.getTime()) ? null : d;
	}

	const naive = timestampNaive.exec(t);
	if (naive) {
		const d = new Date(`${naive[1]}T${naive[2]}Z`);
		return Number.isNaN(d.getTime()) ? null : d;
	}

	// WAF 레코드가 쓰는 epoch 밀리초.
	if (epochMillis.test(t)) {
		const ms = Number(t);
		if (ms > 0 && Number.isSafeInteger(ms)) return new Date(ms);
	}
	return null;
}

const durationUnits: Record<string, number> = {
	ns: 1,
	us: 1e3,
	'µs': 1e3,
	'μs': 1e3,
	ms: 1e6,
	s: 1e9,
	m: 6e10,
	h: 3.6e12
};

const durationPart = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g;

/**
 * parseGoDuration 은 `1.5ms`, `12µs`, `1h2m3.5s` 같은 Go 기간 문자열을
 * 나노초로 읽는다. gin 로그의 지연이 이 형태로만 온다.
 */
export function parseGoDuration(raw: string): number | null {
	const t = raw.trim();
	if (t === '') return null;

	let total = 0;
	let covered = 0;
	durationPart.lastIndex = 0;
	for (;;) {
		const m = durationPart.exec(t);
		if (m === null) break;
		if (m.index !== covered) return null; // 사이에 알 수 없는 것이 끼었다
		const scale = durationUnits[m[2] as string];
		if (scale === undefined) return null;
		total += Number(m[1]) * scale;
		covered = durationPart.lastIndex;
	}
	if (covered !== t.length || covered === 0) return null;
	return total;
}
