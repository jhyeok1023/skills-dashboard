// Logs Insights 쿼리를 만든다. internal/domain/query.go 의 이식이다.
//
// 역슬래시가 많은 파일이라 정규식과 이스케이프는 String.raw 로 쓴다. Go 의
// 백틱 문자열과 같은 성질이라, 원본을 옮길 때 역슬래시를 세어 가며 배로 늘릴
// 일이 없다.

import type { LogFormat } from '../contract.ts';
import { compactDuration, type Period, type Window } from './window.ts';

/**
 * Query 는 Logs Insights 쿼리 하나다. 무엇을 위한 것인지 꼬리표를 달아, 핸들러가
 * 결과를 위치로 짐작하지 않고 패널에 되맞출 수 있게 한다.
 */
export interface Query {
	id: string;
	text: string;
	limit: number;
}

function query(id: string, text: string, limit = 0): Query {
	return { id, text, limit };
}

/**
 * fieldRe 는 필드 참조를 Logs Insights 가 실제로 받아 주는 모양으로 묶는다:
 * 선택적인 @ 접두사, 그다음 점으로 이은 식별자. 필드 이름은 사용자 설정에서
 * 이 모듈에 닿으므로, 그 모양을 벗어나는 것은 끼워 넣지 않고 거절한다 — 안
 * 그러면 `x | delete @message |` 같은 "필드 이름" 이 쿼리를 고쳐 쓴다.
 */
const fieldRe = /^@?[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$/;

/** field 는 설정된 필드 참조가 끼워 넣어도 되는지 확인한다. */
export function field(name: string): string {
	if (name === '') throw new Error('empty field reference');
	if (!fieldRe.test(name)) throw new Error(`invalid field reference "${name}"`);
	return name;
}

// 역슬래시 한 글자. '\' 로 쓰면 이 파일을 지나는 도구마다 개수를 다르게 세므로
// 코드포인트로 못박는다.
const backslash = '\u005c';

const quoteEscapes: Record<string, string> = {
	[backslash]: backslash + backslash,
	"'": backslash + "'",
	'\n': backslash + 'n',
	'\r': backslash + 'r'
};

/** quote 는 Logs Insights 문자열 리터럴을 만든다. */
export function quote(s: string): string {
	let out = "'";
	for (const ch of s) out += quoteEscapes[ch] ?? ch;
	return out + "'";
}

/** Go 의 regexp.QuoteMeta. 정규식 메타문자를 이스케이프한다. */
export function quoteMeta(s: string): string {
	const specials = new Set([...String.raw`.+*?()|[]{}^$`, backslash]);
	let out = '';
	for (const ch of s) out += specials.has(ch) ? backslash + ch : ch;
	return out;
}

/**
 * headerNameRe 는 운영자가 준 헤더 이름을 RFC 9110 이 필드 이름에 허용하는
 * 문자로 묶는다. 그래야 아래 parse 패턴에 그대로 끼워 넣어도 이스케이프
 * 사고가 나지 않는다.
 */
const headerNameRe = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;

/** isQueryableHeader 는 이 헤더 이름을 쿼리에 넣을 수 있는지만 본다. */
export function isQueryableHeader(name: string): boolean {
	return headerNameRe.test(name);
}

/**
 * headerParse 는 WAF 레코드에서 요청 헤더 하나를 꺼내 alias 에 묶는 명령을
 * 만든다.
 *
 * WAF 는 헤더를 {name, value} 객체의 배열로 저장한다. Insights 는 배열 원소로
 * 그룹을 지을 수 없고, 위치로 인덱싱하는 것(headers.0.value)은 요청마다 순서가
 * 달라 의미가 없다. 그래서 원본 레코드에서 parse 로 값을 끌어낸다. 이전 구현은
 * 같은 문제를 저장된 모든 행에 대한 SQLite json_each 크로스 조인으로 풀었고,
 * 그것이 그 구현에서 가장 비싼 쿼리였다.
 *
 * 헤더별 분해와 최근 요청 목록이 둘 다 이것을 쓰고, 둘이 똑같이 동작해야 한다 —
 * 헤더가 안 잡히기 시작했을 때 들여다볼 정규식이 하나여야 한다.
 */
export function headerParse(name: string, alias: string): string {
	if (!isQueryableHeader(name)) {
		throw new Error(`invalid header name "${name}"`);
	}
	return `parse @message /"name":"(?i)${quoteMeta(name)}","value":"(?<${alias}>[^"]*)"/`;
}

/**
 * Insights 의 parse 에 넘기는 Gin 액세스 로그 패턴.
 *
 * Go 쪽은 백틱 문자열에 역슬래시를 두 벌로 넣어 두고 쓸 때 한 벌로 줄였다.
 * regexp 패키지가 그 문자열을 컴파일할 일이 없는데도 두 벌이었던 것은 그저
 * 원본을 그렇게 적었기 때문이므로, 여기서는 내보낼 모양 그대로 둔다.
 *
 * `/` 만 쓰는 자리에서 이스케이프한다. Insights 는 정규식을 슬래시로 감싸므로
 * 날짜의 슬래시가 패턴을 끝내 버린다.
 */
const ginInsightsPattern = String.raw`(?:\x1b\[[0-9;]*m)*\[GIN\]\s+\d{4}/\d{2}/\d{2}\s+-\s+\d{2}:\d{2}:\d{2}\s+\|(?:\x1b\[[0-9;]*m)*\s*(?<ginStatus>\d{3})\s*(?:\x1b\[[0-9;]*m)*\|(?:\x1b\[[0-9;]*m)*\s*(?:(?<ginHours>\d+)h)?(?:(?<ginMinutes>\d+)m)?(?<ginLatency>[\d.]+)(?<ginLatencyUnit>ns|µs|μs|us|ms|s)\s*(?:\x1b\[[0-9;]*m)*\|\s*(?<ginClientIp>\S+)\s*\|(?:\x1b\[[0-9;]*m)*\s*(?<ginMethod>[A-Z]+)\s*(?:\x1b\[[0-9;]*m)*\s+"?(?<ginTarget>(?:\.|[^"\s])+?)"?(?:\s|$)`;

/** LogQueries 는 LogFormat 하나로 로그 그룹 하나의 쿼리를 만든다. */
export class LogQueries {
	readonly format: LogFormat;

	constructor(format: LogFormat) {
		this.format = format;
	}

	/**
	 * namespaceFilter 는 결과를 설정된 네임스페이스로 좁힌다. 이전 구현은 Go 에
	 * "default" 를 박아 두고 나머지는 내려받은 뒤에 버렸다. 이 필터는 집계보다
	 * 먼저 돌지만, 계정에 맞는 필드 인덱스가 없으면 Insights 스캔 바이트를 줄이지는
	 * 않는다.
	 */
	namespace(): string {
		if (this.format.namespace === '') return '';
		return `| filter kubernetes.namespace_name = ${quote(this.format.namespace)}\n`;
	}

	/**
	 * excludePathFilter 는 팟 로그 쿼리에서 프로브 트래픽을 떨군다.
	 *
	 * ispresent 로 감싸는 것이 중요하다. Insights 에서 레코드가 들고 있지 않은
	 * 필드에 대한 비교는 아무것에도 맞지 않으므로, 감싸지 않은 `path not in [...]`
	 * 은 모든 평문 로그 줄을 조용히 버린다 — ERROR 와 WARN 출력이 그냥 비게 되고,
	 * 그것은 이 필터가 없애려던 잡음보다 훨씬 나쁜 실패다.
	 */
	probes(): string {
		// 정확히 같은 것만 맞춘다. 부분 문자열이나 접두사 규칙은 /healthy-users
		// 를 조용히 삼키는 부류이고, 설정 화면의 미리보기가 쿼리와 같은 말을
		// 하려면 Go 쪽에도 똑같이 다시 구현돼야 한다. 명시적인 목록은 예측 가능한
		// 채로 남고, 두 곳이 어긋나지 않는다.
		const exact = this.format.excludePaths.filter((p) => p !== '').map(quote);
		if (exact.length === 0) return '';
		return `| filter not ispresent(path) or path not in [${exact.join(', ')}]\n`;
	}

	private processedField(name: string): string {
		if (name === '') throw new Error('field is not configured');
		if (this.format.processedField === '') return field(name);
		return field(`${this.format.processedField}.${name}`);
	}

	/**
	 * levelFilter 는 error·warn 줄을 고른다. 명시적인 level 필드를 먼저 보고,
	 * 없으면 원본 메시지에 대한 패턴 매치로 물러난다. 여기서 정의하는 `level`
	 * 컬럼이 podErrorSeries 가 묶는 대상이다.
	 */
	levelFilter(): string {
		// 시계열의 키 집합이 안정적이도록 두 갈래로 정규화한다.
		//
		// 메시지 필드는 별칭을 새로 달지 않고 그 자리에서 맞춘다. podErrorList 가
		// 이미 그 필드를 이름으로 선택하는데, Logs Insights 는 필드를 선택하면서
		// 동시에 재별칭하는 쿼리를 컴파일하지 않는다.
		return (
			'| fields tolower(rawLevel) as lvl\n' +
			"| filter lvl in ['error', 'err', 'fatal', 'panic', 'warn', 'warning']\n" +
			String.raw`    or (lvl = '' and not ispresent(status) and dashboardMessage like /(?i)\b(error|fatal|panic|warn|warning|oomkilled)\b/)` +
			'\n' +
			// 삼항이 아니라 if(). Logs Insights 에는 `? :` 가 없고 렉서에서 죽는다.
			String.raw`| fields if(lvl in ['warn', 'warning'] or (lvl = '' and not ispresent(status) and dashboardMessage like /(?i)\b(warn|warning)\b/), 'warn', 'error') as level` +
			'\n'
		);
	}

	/**
	 * accessFields 는 상세 목록이 고르는 필드다.
	 *
	 * 고정된 셋은 애플리케이션의 로그 줄이 아니라 쿠버네티스 봉투에서 오므로,
	 * 운영자가 로그 형식에 무엇을 적었든 존재한다. 그 덕에 이 패널은 User-Agent
	 * 필드가 설정된 경우에만이 아니라 무조건 행 상세를 내줄 수 있다.
	 *
	 * 나머지는 accessPreamble 이 정의하는 별칭이므로, 줄이 JSON 으로 왔든 Gin
	 * 액세스 로그로 왔든 같은 이름으로 읽힌다.
	 */
	accessFields(): string[] {
		const out = [
			'kubernetes.pod_name as pod',
			'kubernetes.container_name as container',
			'kubernetes.namespace_name as namespace',
			'app',
			'method',
			'path',
			'requestTarget',
			'status',
			'latencyMs',
			'clientIp'
		];
		// userAgent 에는 preamble 별칭이 없다. 짐작할 기본 이름이 없으므로,
		// 설정된 필드가 있을 때만 거기서 곧장 읽는다.
		if (this.format.userAgentField !== '') {
			try {
				out.push(`${this.processedField(this.format.userAgentField)} as userAgent`);
			} catch (err) {
				throw new Error(`userAgentField: ${message(err)}`);
			}
		}
		return out;
	}

	/**
	 * accessPreamble 은 구조화된 JSON 과 Gin 액세스 줄에 하나의 필드 이름 집합을
	 * 준다. 모든 쿼리가 그 별칭을 읽으므로, 자동 인식이 지연 패널과 비정상 응답
	 * 패널로 하여금 같은 줄에 대해 다른 말을 하게 만들 수 없다.
	 */
	accessPreamble(): string {
		let msg: string;
		try {
			msg = field(this.format.messageField);
		} catch (err) {
			throw new Error(`messageField: ${message(err)}`);
		}

		const jsonFields: Record<string, string> = {};
		if (this.format.preset !== 'gin') {
			const sources: [string, string][] = [
				['app', this.format.appField],
				['latency', this.format.latencyField],
				['status', this.format.statusField],
				['method', this.format.methodField],
				['target', this.format.pathField],
				['level', this.format.levelField],
				['clientIp', this.format.clientIpField]
			];
			for (const [key, name] of sources) {
				if (name === '') continue;
				try {
					jsonFields[key] = this.processedField(name);
				} catch (err) {
					throw new Error(`${key}Field: ${message(err)}`);
				}
			}
		}

		let b = `fields @timestamp, coalesce(${msg}, @message) as dashboardMessage\n`;
		if (this.format.preset !== 'json') {
			b += `| parse dashboardMessage /${ginInsightsPattern.split('/').join(backslash + '/')}/\n`;
		}

		const calculated: string[] = [];
		let jsonLatency = '';
		const latencySource = jsonFields['latency'];
		if (latencySource !== undefined && latencySource !== '') {
			const expr =
				this.format.latencyUnit === 's' ? `(${latencySource} * 1000)` : latencySource;
			calculated.push(aliasExpr(expr, 'jsonLatencyMs'));
			jsonLatency = 'jsonLatencyMs';
		}
		let ginLatency = '';
		let ginStatus = '';
		if (this.format.preset !== 'json') {
			calculated.push(
				aliasExpr('(ginStatus * 1)', 'ginStatusNumber'),
				aliasExpr(
					'case(ispresent(ginHours), ginHours * 3600000, 0) + ' +
						'case(ispresent(ginMinutes), ginMinutes * 60000, 0) + ' +
						"case(ginLatencyUnit = 's', ginLatency * 1000, " +
						"ginLatencyUnit in ['µs', 'μs', 'us'], ginLatency / 1000, " +
						"ginLatencyUnit = 'ns', ginLatency / 1000000, ginLatency)",
					'ginLatencyMs'
				)
			);
			ginLatency = 'ginLatencyMs';
			ginStatus = 'ginStatusNumber';
		}
		if (calculated.length > 0) b += `| fields ${calculated.join(', ')}\n`;

		const fields = [
			aliasExpr(coalesceExpr(jsonFields['app'], 'kubernetes.container_name'), 'app'),
			aliasExpr(coalesceExpr(jsonFields['status'], ginStatus), 'status'),
			aliasExpr(coalesceExpr(jsonLatency, ginLatency), 'latencyMs'),
			aliasExpr(
				coalesceExpr(jsonFields['method'], ginField(this.format.preset, 'ginMethod')),
				'method'
			),
			aliasExpr(
				coalesceExpr(jsonFields['target'], ginField(this.format.preset, 'ginTarget')),
				'requestTarget'
			),
			aliasExpr(
				coalesceExpr(jsonFields['clientIp'], ginField(this.format.preset, 'ginClientIp')),
				'clientIp'
			),
			aliasExpr(coalesceExpr(jsonFields['level'], "''"), 'rawLevel')
		];
		b += `| fields ${fields.join(', ')}\n`;
		b += String.raw`| parse requestTarget /^(?<requestPath>[^?]*)/` + '\n';
		b += '| fields coalesce(requestPath, requestTarget) as path\n';
		return b;
	}
}

function ginField(preset: LogFormat['preset'], name: string): string {
	return preset === 'json' ? '' : name;
}

function coalesceExpr(...values: (string | undefined)[]): string {
	const kept = values.filter((v): v is string => v !== undefined && v !== '');
	if (kept.length === 0) return "''";
	if (kept.length === 1) return kept[0] as string;
	return `coalesce(${kept.join(', ')})`;
}

function aliasExpr(expr: string, alias: string): string {
	return `${expr} as ${alias}`;
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}

/**
 * podTraffic 은 칸마다 지연 백분위와 요청 수를 낸다.
 *
 * 두 모집단을 같은 쿼리에서 세고 따로 이름 붙인다. `requests` 는 상태를 실은
 * 줄을, `latencySamples` 는 지연을 실은 줄을 센다. 이전 구현은 그 두 숫자를 서로
 * 다른 SQL 두 개에서 끌어내고는 UI 에서 둘 다 "요청 수" 라고 적었다. 같은 패널이
 * 총계를 두 개 보여 줄 수 있었던 이유가 그것이다. 여기서는 차이가 와이어에
 * 드러나고 각 stat 의 basis 로 보고된다.
 */
export function podTraffic(lq: LogQueries, w: Window): Query {
	const text =
		lq.accessPreamble() +
		lq.namespace() +
		lq.probes() +
		'| filter ispresent(status) or ispresent(latencyMs)\n' +
		'| stats count(status) as requests,\n' +
		'        count(latencyMs) as latencySamples,\n' +
		'        avg(latencyMs) as avg,\n' +
		'        pct(latencyMs, 50) as p50,\n' +
		'        pct(latencyMs, 90) as p90,\n' +
		'        pct(latencyMs, 99) as p99\n' +
		`    by bin(${compactDuration(w.period)}) as t, app\n` +
		'| sort t asc';
	return query('pod.traffic', text);
}

/**
 * podBadStatusSeries 는 칸과 상태 코드별로 비정상 응답을 센다. 완전한 집계이므로,
 * 이것을 더하면 잘린 상세 목록과 견줄 정직한 총계가 나온다.
 *
 * 칸과 상태로만 묶고 그 밖의 것으로는 묶지 않는다. 일부러다. 예전에는 아무도
 * 읽지 않는 `path` 를 세 번째 키로 달고 있었는데, 그 키가 총계의 정직함을
 * 결정하고 있었다. Insights 는 stats 결과를 insightsMaxRows 에서 자르고, 여기
 * 행 수는 칸 × 상태 × 경로다. 2시간·5분이면 24 × 5 × 경로이므로 서로 다른 경로
 * 여든 종 — 무작위 URL 을 긁는 스캐너 하나 — 이면 차트와 그 옆의 "비정상 응답"
 * 수치가 조용히 잘렸다. 경로별 분해는 이제 bin 없는 제 쿼리를 갖고, 거기서는
 * 같은 경로가 하나에 한 행씩만 든다.
 */
export function podBadStatusSeries(lq: LogQueries, w: Window): Query {
	const text =
		lq.accessPreamble() +
		lq.namespace() +
		lq.probes() +
		`| filter ispresent(status) and ${notInStatuses('status', lq.format.okStatuses)}\n` +
		`| stats count() as n by bin(${compactDuration(w.period)}) as t, status\n` +
		'| sort t asc';
	return query('pod.badStatus.series', text);
}

/**
 * podBadStatusByPath 는 칸이 아니라 창 전체에 걸쳐 상태 코드와 경로별로 비정상
 * 응답을 센다.
 *
 * 필터는 podBadStatusSeries·podBadStatusList 가 쓰는 것과 글자 하나까지 같으므로,
 * 셋은 하나의 모집단을 설명한다. 총계는 가장자리에서 여전히 다를 수 있다 —
 * 시계열은 창의 배타적 끝에 떨어지는 칸을 버리는데 Insights 의 포함적 EndTime 은
 * 그것을 이 쿼리에 넘긴다 — 다만 서로 다른 요청을 세어서 그런 것은 결코 아니다.
 *
 * limit 절이 없는 것은 상한이 없다는 뜻이 아니다. Insights 는 모든 결과 집합을
 * insightsMaxRows 에서 자르므로, (상태, 경로) 카디널리티가 10,000 을 넘으면
 * 아래의 `sort n desc` 가 무엇이 살아남을지 정한다 — 그리고 그것은 양으로 정한다.
 * 무작위 경로에 대한 404 홍수가 결국 드문 403 한 행을 밀어낸다는 뜻이다. 이
 * 정렬이 피하려던 실패가 바로 그것이고, 없어진 것이 아니라 미뤄졌을 뿐이다.
 * 경로를 열거하면서 행 상한 너머까지 코드별 총계를 옳게 지키는 Insights 스캔은
 * 하나도 없다. `limit N` 이 없어서 얻는 것은 평범한 경우다 — 모든 코드가 보이고,
 * 읽을 만한 수의 경로로 자르는 일이 Go 에서, 상태 코드마다, 전부를 본 뒤에
 * 일어난다. 상한에 닿으면 호출자가 그렇다고 말한다.
 *
 * bin() 을 버린 것이 상한 없는 stats 를 감당 가능하게 만든다: (칸, 상태, 경로)
 * 마다가 아니라 (상태, 경로) 마다 한 행이다. 창은 매개변수가 아니다 — bin() 이
 * 없으면 여기서 창에 따라 달라지는 것이 없고, 러너가 시작할 때 모든 쿼리를 창에
 * 묶는다.
 */
export function podBadStatusByPath(lq: LogQueries): Query {
	const text =
		lq.accessPreamble() +
		lq.namespace() +
		lq.probes() +
		`| filter ispresent(status) and ${notInStatuses('status', lq.format.okStatuses)}\n` +
		// 정렬이 아니라 max(@timestamp) 다. 호출자가 원하는 것은 그룹마다 가장
		// 최근의 것이고, Insights 는 @timestamp 를 고정 폭으로 내므로 그것을
		// 고르는 비교에 파싱이 필요 없다.
		'| stats count() as n, max(@timestamp) as lastTs by status, path\n' +
		'| sort n desc';
	return query('pod.badStatus.byPath', text);
}

/**
 * podBadStatusList 는 가장 최근의 비정상 응답을 최신순으로 낸다. 필터가
 * podBadStatusSeries 와 같으므로, 목록과 그 옆에 표시되는 수치가 서로 다른
 * 모집단을 설명하는 일이 있을 수 없다.
 */
export function podBadStatusList(lq: LogQueries, limit: number): Query {
	const text =
		lq.accessPreamble() +
		`| fields @timestamp, ${lq.accessFields().join(', ')}\n` +
		lq.namespace() +
		lq.probes() +
		`| filter ispresent(status) and ${notInStatuses('status', lq.format.okStatuses)}\n` +
		'| sort @timestamp desc\n' +
		`| limit ${limit}`;
	return query('pod.badStatus.list', text, limit);
}

/**
 * podErrorSeries 는 칸마다 ERROR·WARN 줄을 센다. 비정상 응답 집계와 마찬가지로,
 * 상세 목록의 머리말이 잘린 배열의 길이가 아니라 진짜 총계를 보일 수 있게 하려고
 * 존재한다.
 */
export function podErrorSeries(lq: LogQueries, w: Window): Query {
	const text =
		lq.accessPreamble() +
		lq.namespace() +
		lq.probes() +
		lq.levelFilter() +
		`| stats count() as n by bin(${compactDuration(w.period)}) as t, level\n` +
		'| sort t asc';
	return query('pod.errors.series', text);
}

/** podErrorList 는 가장 최근의 ERROR·WARN 줄을 낸다. */
export function podErrorList(lq: LogQueries, limit: number): Query {
	const text =
		lq.accessPreamble() +
		'| fields @timestamp, dashboardMessage, kubernetes.pod_name as pod, kubernetes.container_name as container\n' +
		lq.namespace() +
		lq.probes() +
		lq.levelFilter() +
		'| sort @timestamp desc\n' +
		`| limit ${limit}`;
	return query('pod.errors.list', text, limit);
}

/**
 * notInStatuses 는 정상 코드 제외를 만든다. 빈 집합은 상태가 있는 모든 응답이
 * 관심 대상이라는 뜻이다.
 */
export function notInStatuses(name: string, ok: number[]): string {
	if (ok.length === 0) return '1 = 1';
	return `${name} not in [${ok.join(', ')}]`;
}

/**
 * WAFQueries 는 WAF 로그 그룹의 쿼리를 만든다. WAF 레코드는 스키마가 고정이라,
 * 팟 로그와 달리 운영자가 관심 있는 헤더 말고는 설정할 것이 없다.
 */
export class WAFQueries {
	/** 트래픽을 분해할 HTTP 헤더 이름. */
	readonly headers: string[];

	constructor(headers: string[] = defaultWAFHeaders()) {
		this.headers = headers;
	}
}

/**
 * 기본으로 분해할 만한 헤더. 목록을 짧게 두는 것은 의도다 — 헤더 하나마다 창을
 * 한 번 더 통째로 스캔한다.
 */
export function defaultWAFHeaders(): string[] {
	return ['Host', 'User-Agent'];
}

/**
 * wafActionSeries 는 칸과 액션별로 요청을 센다. 허용·차단 차트와 그 옆의 정직한
 * 총계를 둘 다 여기서 끌어온다.
 */
export function wafActionSeries(w: Window): Query {
	return query(
		'waf.action.series',
		'fields @timestamp\n' +
			`| stats count() as n by bin(${compactDuration(w.period)}) as t, action\n` +
			'| sort t asc'
	);
}

/**
 * 모든 분해는 제 키와 함께 액션으로도 묶고, 그룹마다 가장 최근 시각을 싣는다.
 *
 * 액션이 없으면 "이 경로가 4,000번 요청됐다" 는 그 요청들이 애플리케이션에
 * 닿았는지에 대해 아무 말도 하지 않는다 — 허용된 것과 차단된 것이 한 숫자로
 * 더해졌다. 그룹이 그 둘을 가르고, (키, 액션) 마다의 max(@timestamp) 가 그 키에
 * 대해 어느 액션이 가장 최근이었는지를 호출자가 말할 수 있게 한다.
 *
 * 비용은 그대로다. 같은 창을 같은 만큼 스캔하고 묶는 키가 하나 늘 뿐이다.
 * 달라지는 것은 행 수이고, 그래서 호출자가 상한을 올린다 — actionFanout 참고.
 */
function breakdownStats(by: string): string {
	return `stats count() as n, max(@timestamp) as lastTs by ${by}, action\n| sort n desc\n`;
}

/** wafByMethod 는 HTTP 메서드와 액션별로 요청을 센다. */
export function wafByMethod(): Query {
	return query('waf.byMethod', breakdownStats('httpRequest.httpMethod as method'));
}

/**
 * wafByPath 는 URI·쿼리 문자열·액션별로 요청을 센다. URI 와 args 를 이어 붙이지
 * 않고 별도 컬럼으로 내는 것은 UI 가 각각에 복사 버튼을 줄 수 있게 하려는 것이다.
 */
export function wafByPath(limit: number): Query {
	return query(
		'waf.byPath',
		breakdownStats('httpRequest.uri as uri, httpRequest.args as args') + `| limit ${limit}`,
		limit
	);
}

/** wafBlocked 는 종료 규칙과 클라이언트 주소별로 차단된 요청을 센다. */
export function wafBlocked(limit: number): Query {
	return query(
		'waf.blocked',
		"filter action = 'BLOCK'\n" +
			'| stats count() as n by terminatingRuleId as rule, httpRequest.clientIp as clientIp, httpRequest.country as country\n' +
			'| sort n desc\n' +
			`| limit ${limit}`,
		limit
	);
}

/**
 * wafRecentList 는 개별 요청을 최신순으로, 각각에 WAF 가 취한 액션과 함께 낸다.
 *
 * 일부러 차단된 요청으로 거르지 않는다. 규칙이 무엇을 막았는지는 wafBlocked 가
 * 이미 요약한다. 이것이 답하는 것은 다른 질문이다 — 트래픽이 오기는 하는가, 그리고
 * 통과하고 있는가. 차단 목록만으로는 답할 수 없는 질문인데, 빈 목록은 "아무것도
 * 차단되지 않았다" 이거나 "아무것도 오지 않았다" 이기 때문이다.
 *
 * 표에 보이는 것 너머로, 운영자가 한 행에 대해 다음에 묻는 것을 함께 고른다.
 * 누가 보냈고, 자기를 뭐라고 부르며, WAF 가 실제로 무엇을 했는가. 이미 일어난
 * 스캔에 필드를 더 고르는 것이므로 값은 들지 않는다 — Insights 는 읽은 바이트로
 * 과금하고, 레코드는 어차피 통째로 읽혔다.
 *
 * 고르지 않는 것은 @message 다. WAF 원본 레코드는 헤더 배열과 규칙 그룹 목록까지
 * 해서 1KB 쯤 되고 아래 필드는 수백 바이트인데, 이 목록은 폴링마다 다시 가져온다.
 * 필드를 이름으로 고르는 것이 상세 화면이 다운로드가 되지 않게 막는다.
 */
export function wafRecentList(limit: number): Query {
	// 캡처 이름을 그것이 먹이는 컬럼과 다르게 두고 아래에서 별칭으로 옮긴다.
	//
	// Logs Insights 는 `fields` 안의 이름을 선택이 아니라 정의로 읽으므로, parse
	// 가 만든 필드를 거기 적으면 두 번 정의한 것이 되고 쿼리는 돌기 전에
	// 거절당한다: "Ephemeral field is already defined". 팟 에러 목록이 이미
	// 선택한 메시지 필드를 재별칭하지 못하는 것과 같은 규칙이다 — 쿼리는 임시
	// 필드를 마음껏 쓸 수 있고 새 이름으로 별칭할 수도 있지만, 다시 이름 지을
	// 수는 없다.
	const ua = headerParse('User-Agent', 'uaCapture');
	return query(
		'waf.recent.list',
		ua +
			'\n' +
			'| fields @timestamp, action, terminatingRuleId as rule, terminatingRuleType as ruleType,\n' +
			'       responseCodeSent as responseCode, uaCapture as userAgent,\n' +
			'       httpRequest.clientIp as clientIp,\n' +
			'       httpRequest.country as country, httpRequest.httpMethod as method,\n' +
			'       httpRequest.uri as uri, httpRequest.args as args\n' +
			'| sort @timestamp desc\n' +
			`| limit ${limit}`,
		limit
	);
}

/** wafByHeader 는 요청 헤더 하나의 서로 다른 값을 액션별로 센다. */
export function wafByHeader(name: string, limit: number): Query {
	const parse = headerParse(name, 'headerValue');
	return query(
		`waf.byHeader.${name.toLowerCase()}`,
		parse +
			'\n' +
			'| filter ispresent(headerValue)\n' +
			'| ' +
			breakdownStats('headerValue as value') +
			`| limit ${limit}`,
		limit
	);
}
