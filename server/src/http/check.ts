// 트래픽 점검. internal/api/check.go 의 이식이다.
//
// 이 대시보드에서 AWS 가 아닌 것에 닿는 유일한 자리다.
//
// 여기의 다른 모든 숫자는 CloudWatch 에서 오는데, 그것은 몇 분 늦고 패널이 비었을
// 때 아무 일도 없었던 것인지 아무것도 publish 되지 않은 것인지 말하지 못한다.
// "지금 서비스가 답하고 있는가" 는 데이터가 답할 수 있는 질문이 아니므로, 서비스에
// 직접 묻는다.
//
// 일부러 작게 둔다. GET 하나, 상태 코드 하나, 걸린 시간 하나, 누가 누를 때만.
// 아무것도 저장하지 않는다 — 이력을 남기는 점검은 모니터링 시스템이고, 이것은
// 버튼이다.

import type { Context } from 'hono';

import { checkOK } from '../config/config.ts';
import type { CheckResult } from '../contract.ts';
import type { Service } from '../service.ts';
import { badRequest, json, statusText } from './respond.ts';

/**
 * 점검 하나의 시한(ms). "느리다" 와 "죽었다" 를 가를 만큼 길고, 운영자가 아직
 * 보고 있는 동안 버튼이 돌아올 만큼 짧다.
 */
const checkTimeoutMs = 10_000;

/**
 * 요청이 하나뿐이므로 별도의 클라이언트를 만들지 않는다. Go 는 호출마다 새
 * http.Client 를 만들면 연결 풀이 하나씩 새므로 한 번 만들어 재사용했는데,
 * node 의 fetch 는 전역 디스패처를 쓰므로 그 문제가 애초에 없다.
 */
export async function handleCheck(service: Service, c: Context): Promise<Response> {
	const cfg = service.store.get().check;
	if (cfg.url === '') {
		return badRequest(new Error('점검할 주소가 설정되지 않았습니다'));
	}

	let expect = '2xx';
	if (cfg.expectStatus > 0) {
		const text = statusText(cfg.expectStatus);
		expect = text === 'Error' ? String(cfg.expectStatus) : `${cfg.expectStatus} ${text}`;
	}

	const started = service.now();
	const res: CheckResult = {
		url: cfg.url,
		ok: false,
		elapsedMs: 0,
		at: formatRFC3339(started),
		expect
	};

	const deadline = AbortSignal.timeout(checkTimeoutMs);
	const signal = AbortSignal.any([c.req.raw.signal, deadline]);

	const start = Date.now();
	let resp: Response;
	try {
		resp = await fetch(cfg.url, {
			method: 'GET',
			// 일부러 이름을 붙인다. 대상의 액세스 로그를 읽는 사람이 이 요청을
			// 그것이 대신하고 있는 진짜 트래픽과 구분할 수 있어야 한다.
			headers: { 'User-Agent': 'skills-dashboard/traffic-check' },
			signal
		});
	} catch (err) {
		res.elapsedMs = Date.now() - start;
		res.error = err instanceof Error ? err.message : String(err);
		return json(200, res);
	}
	res.elapsedMs = Date.now() - start;

	// 본문을 버리고 연결을 놓아준다. 다음에 누를 때 새로 다이얼하지 않고 풀로
	// 돌아가게 하려는 것이다. 본문을 어디에도 담지 않는다 — 이것이 보고하는 것은
	// 서비스가 답했는가이고, 화면에 뜬 응답 본문은 스크린샷 속의 응답 본문이다.
	try {
		await resp.arrayBuffer();
	} catch {
		// 본문을 못 읽어도 상태 코드는 이미 얻었다.
	}

	res.status = resp.status;
	res.ok = checkOK(cfg, resp.status);
	return json(200, res);
}

/** Go 의 time.RFC3339. 초 아래는 쓰지 않는다. */
function formatRFC3339(d: Date): string {
	return d.toISOString().slice(0, 19) + 'Z';
}
