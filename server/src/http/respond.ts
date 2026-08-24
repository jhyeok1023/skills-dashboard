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
 * 끝의 개행은 Go 의 json.Encoder 가 붙이던 것이다. 한편 Go 는 `<`·`>`·`&` 를
 * < 식으로 이스케이프하고 JSON.stringify 는 하지 않는다 — 디코딩하면 같은
 * 값이라 프론트도 parity diff 도 구분하지 못하지만, 바이트로 비교하는 사람이
 * 있을까 봐 적어 둔다.
 */
export function json(status: number, value: unknown): Response {
	return new Response(JSON.stringify(value) + '\n', {
		status,
		headers: {
			'Content-Type': 'application/json; charset=utf-8',
			'Cache-Control': 'no-store'
		}
	});
}

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
