// 빌드된 SvelteKit 앱을 번들 안에서 서비스한다.
//
// internal/web/embed.go 의 이식이다. 지켜야 하는 것은 셋이다.
//
//   1. /api/* 는 여기서 무조건 404. API 는 앞에 붙지만, 라우팅 순서가 틀어져
//      여기까지 흘러왔을 때 JSON 을 기대한 자리에 HTML 이 오면 안 된다.
//   2. _app/immutable/* 만 영구 캐시. index.html 까지 캐시하면 낡은 껍데기가
//      사라진 자산을 계속 불러온다.
//   3. 실제 파일이 아닌 경로는 index.html 로 떨어뜨린다. /logs/pod 를 하드
//      리프레시해도 사는 이유가 이것이다.

import { assets } from 'virtual:web-assets';

import type { Hono } from 'hono';

const notBuiltPage = `<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><title>빌드되지 않음</title>
<style>
body{font:16px/1.6 ui-sans-serif,system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f5f5f7;color:#1d1d1f}
main{max-width:38rem;padding:2rem}
code{font:14px ui-monospace,SFMono-Regular,Menlo,monospace;background:#e8e8ed;padding:.15em .4em;border-radius:.3em}
pre{background:#e8e8ed;padding:1rem;border-radius:.6em;overflow-x:auto}
@media(prefers-color-scheme:dark){body{background:#1d1d1f;color:#f5f5f7}code,pre{background:#2c2c2e}}
</style></head>
<body><main>
<h1>프론트엔드가 빌드되지 않았습니다</h1>
<p>이 번들에는 웹 UI가 포함되어 있지 않습니다. 아래를 실행한 뒤 다시 빌드하세요.</p>
<pre>npm run install:web
mise run web:build
mise run node:build</pre>
<p>API는 정상 동작합니다. <code>/api/health</code> 로 확인할 수 있습니다.</p>
</main></body></html>`;

/** 프론트엔드가 번들에 들어 있는지. 들어 있지 않으면 안내 페이지만 뜬다. */
export const isBuilt = assets.has('index.html');

// 경로를 자산 키로 바꾼다. `..` 는 정규화하지만 그것만으로 안전을 주장하지
// 않는다 — 조회가 Map 이라 키가 없으면 그냥 못 찾은 것이 되고, 트리 밖으로
// 나갈 방법 자체가 없다.
function assetKey(pathname: string): string {
	let decoded = pathname;
	try {
		decoded = decodeURIComponent(pathname);
	} catch {
		// 잘못 인코딩된 경로는 원문 그대로 조회한다. 어차피 찾지 못한다.
	}

	const parts: string[] = [];
	for (const segment of decoded.split('/')) {
		if (segment === '' || segment === '.') continue;
		if (segment === '..') parts.pop();
		else parts.push(segment);
	}
	return parts.join('/');
}

function headersFor(type: string, length: number, cacheControl: string): Headers {
	const headers = new Headers({
		'Content-Type': type,
		'Content-Length': String(length),
		'Cache-Control': cacheControl
	});
	return headers;
}

function serveIndex(method: string): Response {
	const index = assets.get('index.html');
	if (!index) {
		return new Response('index.html is missing from the bundle', { status: 500 });
	}
	const headers = headersFor(index.type, index.body.byteLength, 'no-store');
	if (method === 'HEAD') return new Response(null, { status: 200, headers });
	return new Response(index.body, { status: 200, headers });
}

/**
 * mountWeb 은 SPA 를 마지막 라우트로 붙인다. API 를 먼저 등록한 뒤에 부른다.
 *
 * Go 의 http.FileServer 가 주던 Range·ETag·Last-Modified 는 없다. 자산은
 * 해시 이름으로 영구 캐시되고 index.html 은 no-store 라, 조건부 요청이 아낄
 * 것이 남아 있지 않다.
 */
export function mountWeb(app: Hono): void {
	app.on(['GET', 'HEAD'], '*', (c) => {
		const { pathname } = new URL(c.req.url);
		if (pathname.startsWith('/api/')) return c.notFound();

		if (!isBuilt) {
			return c.html(notBuiltPage);
		}

		const key = assetKey(pathname);
		const asset = key === '' ? undefined : assets.get(key);
		if (!asset) return serveIndex(c.req.method);

		const cacheControl = key.startsWith('_app/immutable/')
			? 'public, max-age=31536000, immutable'
			: 'no-cache';
		const headers = headersFor(asset.type, asset.body.byteLength, cacheControl);
		if (c.req.method === 'HEAD') return new Response(null, { status: 200, headers });
		return new Response(asset.body, { status: 200, headers });
	});
}
