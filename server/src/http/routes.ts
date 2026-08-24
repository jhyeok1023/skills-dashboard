// API 라우터. internal/api/server.go 의 Handler() 이식이다.
//
// /api 아래만 여기서 처리하고 나머지는 SPA 핸들러가 받는다. 붙이는 순서가
// 계약이다 — mountWeb 이 마지막이어야 한다.

import { Hono } from 'hono';

import { logger } from '../log';
import type { Service } from '../service';
import { mountWeb } from '../web/handler';
import { fail, json } from './respond';

export function createApp(service: Service): Hono {
	const app = new Hono();

	// 핸들러 하나가 터져도 응답 하나로 끝나야 한다. Go 의 recoverPanics 와
	// 같은 자리다. 버그가 아니게 되는 것은 아니므로 스택은 남긴다.
	app.onError((err, c) => {
		logger.error('panic while serving', {
			path: new URL(c.req.url).pathname,
			panic: err.message,
			stack: err.stack ?? ''
		});
		return fail(500, new Error(`internal error while serving ${new URL(c.req.url).pathname}`));
	});

	// 키 순서가 Go 와 같다. Go 는 map 을 정렬해 내보내므로 credentials 가
	// 먼저다 — 두 엔진의 응답을 바이트로 비교할 때 잡음이 되지 않게 맞춘다.
	app.get('/api/health', () =>
		json(200, {
			credentials: service.credentialError === null,
			ok: service.credentialError === null
		})
	);

	mountWeb(app);
	return app;
}
