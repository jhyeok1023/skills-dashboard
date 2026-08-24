// 번들 시점에 esbuild 플러그인이 채우는 가상 모듈(server/scripts/build.mjs).
// 소스 트리에 생성 파일을 두지 않으려는 것이다 — internal/web/dist 는
// .gitignore 대상이라 거기서 만든 파일도 커밋할 수 없다.
declare module 'virtual:web-assets' {
	export interface WebAsset {
		type: string;
		body: Buffer;
	}

	/** 키는 dist 루트 기준 경로다. 예: `index.html`, `_app/immutable/…` */
	export const assets: Map<string, WebAsset>;
}
