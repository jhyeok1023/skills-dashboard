// 응답의 모양은 한 곳에서만 정한다. internal/api/server.go 의 writeJSON·
// writeError 이식이다.

/** 실패는 전부 이 모양이다. 프론트의 ApiFailure 가 detail·hint 를 읽는다. */
export interface ErrorResponse {
	error: string;
	detail?: string;
	hint?: string;
}

// Go 의 http.StatusText. 쓰는 것만 적는다 — 목록을 다 옮기면 어느 상태를
// 실제로 내는지가 코드에서 보이지 않는다.
const statusTexts: Record<number, string> = {
	200: 'OK',
	400: 'Bad Request',
	404: 'Not Found',
	500: 'Internal Server Error',
	502: 'Bad Gateway',
	503: 'Service Unavailable',
	504: 'Gateway Timeout'
};

export function statusText(status: number): string {
	return statusTexts[status] ?? 'Error';
}

/**
 * json 은 성공 응답을 낸다.
 *
 * no-store 를 붙이는 이유는 이 대시보드가 실시간 데이터를 읽기 때문이다.
 * 캐시된 응답이 돌아오면 사용자가 범위를 바꿨는데도 옛 창이 조용히 남는다.
 *
 * 끝의 개행과 이스케이프 둘 다 Go 의 json.Encoder 를 따라간다. 두 엔진의
 * 응답을 바이트로 비교하는 것이 이 이식의 주된 검증 수단이라, 디코딩하면 같은
 * 값이라도 바이트가 갈리면 diff 가 잡음으로 가득 찬다.
 */
export function json(status: number, value: unknown): Response {
	return new Response(escapeLikeGo(JSON.stringify(value)) + '\n', {
		status,
		headers: {
			'Content-Type': 'application/json; charset=utf-8',
			'Cache-Control': 'no-store'
		}
	});
}

/**
 * escapeLikeGo 는 Go 의 json.Encoder 가 기본으로 하는 이스케이프를 흉내낸다.
 *
 * Go 는 HTML 안에 그대로 끼워 넣어도 안전하도록 `<`·`>`·`&` 를 이스케이프하고,
 * JS 소스에 넣었을 때 줄이 끊기지 않도록 U+2028·U+2029 도 이스케이프한다.
 * JSON 구조 문자에는 이 다섯이 절대 나오지 않으므로 직렬화된 문자열 전체를
 * 훑어도 안전하다.
 */
function escapeLikeGo(body: string): string {
	return body.replace(/[<>&\u2028\u2029]/g, (ch) => escapes[ch] as string);
}

const escapes: Record<string, string> = {
	'<': '\\u003c',
	'>': '\\u003e',
	'&': '\\u0026',
	'\u2028': '\\u2028',
	'\u2029': '\\u2029'
};

export function fail(status: number, err: unknown, hint = ''): Response {
	const detail = err instanceof Error ? err.message : String(err);
	const body: ErrorResponse = { error: statusText(status) };
	if (detail !== '') body.detail = detail;
	if (hint !== '') body.hint = hint;
	return json(status, body);
}

/** 호출자의 잘못. 4시간을 넘는 범위 같은 것. */
export function badRequest(err: unknown): Response {
	return fail(400, err);
}

/** AWS 가 실패한 것. 호출자의 잘못이 아니다. */
export function upstream(err: unknown): Response {
	return fail(502, err, 'AWS 호출이 실패했습니다. 자격증명과 권한을 확인하세요.');
}
