#!/usr/bin/env node
//
// server/dist/skills-dashboard.mjs 하나를 만든다.
//
//   npm run build            (server/ 에서)
//   mise run node:build
//
// 대회장에는 node 만 있고 node_modules 도 없다. 그래서 의존성까지 전부 번들에
// 넣고 그 결과물을 커밋한다. 실행에 필요한 것은 파일 하나와 node 뿐이다.

import { execFileSync } from 'node:child_process';
import {
	copyFileSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	statSync,
	writeFileSync
} from 'node:fs';
import { dirname, join, posix, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';

const serverDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = resolve(serverDir, '..');

// Go 의 //go:embed 와 같은 곳을 읽는다. SPA 는 한 번만 빌드하고 두 엔진이
// 나눠 쓴다 — adapter 출력 경로를 옮기면 Go 쪽 임베드가 깨진다.
const webDist = join(repoRoot, 'internal', 'web', 'dist');

const outFile = join(serverDir, 'dist', 'skills-dashboard.mjs');

// 확장자 → Content-Type. 브라우저가 실제로 만나는 것만 적는다. 모르는 것은
// application/octet-stream 으로 두어 잘못된 타입으로 실행되지 않게 한다.
const mimeTypes = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'text/javascript; charset=utf-8',
	'.mjs': 'text/javascript; charset=utf-8',
	'.css': 'text/css; charset=utf-8',
	'.json': 'application/json; charset=utf-8',
	'.map': 'application/json; charset=utf-8',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.jpg': 'image/jpeg',
	'.jpeg': 'image/jpeg',
	'.gif': 'image/gif',
	'.webp': 'image/webp',
	'.ico': 'image/x-icon',
	'.woff': 'font/woff',
	'.woff2': 'font/woff2',
	'.ttf': 'font/ttf',
	'.txt': 'text/plain; charset=utf-8',
	'.webmanifest': 'application/manifest+json'
};

function contentType(name) {
	const dot = name.lastIndexOf('.');
	if (dot < 0) return 'application/octet-stream';
	return mimeTypes[name.slice(dot).toLowerCase()] ?? 'application/octet-stream';
}

function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const full = join(dir, entry);
		if (statSync(full).isDirectory()) out.push(...walk(full));
		else out.push(full);
	}
	return out;
}

// .gitkeep 은 자산이 아니다. //go:embed 가 매칭 0개면 컴파일 에러라 그것만을
// 위해 존재하는 파일이고, 여기서 내보내면 /.gitkeep 이 서비스된다.
function collectAssets() {
	let files;
	try {
		files = walk(webDist);
	} catch {
		return [];
	}
	return files
		.map((full) => ({ name: relative(webDist, full).split(sep).join(posix.sep), full }))
		.filter(({ name }) => name !== '.gitkeep')
		.sort((a, b) => (a.name < b.name ? -1 : 1));
}

// 자산을 소스 트리에 생성하지 않는다. internal/web/dist 는 .gitignore 대상이라
// 생성 파일도 커밋할 수 없고, 커밋하지 않으면 tsc 가 없는 파일을 찾는다.
// 가상 모듈로 두면 타입은 assets.d.ts 가, 내용은 번들 시점이 책임진다.
function virtualAssetsPlugin(assets) {
	const filter = /^virtual:web-assets$/;
	return {
		name: 'virtual-web-assets',
		setup(pluginBuild) {
			pluginBuild.onResolve({ filter }, (args) => ({
				path: args.path,
				namespace: 'virtual-web-assets'
			}));
			pluginBuild.onLoad({ filter, namespace: 'virtual-web-assets' }, () => {
				const rows = assets.map(({ name, full }) => {
					const body = readFileSync(full).toString('base64');
					return `[${JSON.stringify(name)},${JSON.stringify(contentType(name))},${JSON.stringify(body)}]`;
				});
				return {
					contents: [
						`const files = [\n${rows.join(',\n')}\n];`,
						'export const assets = new Map(',
						'\tfiles.map(([name, type, body]) => [name, { type, body: Buffer.from(body, "base64") }])',
						');'
					].join('\n'),
					loader: 'js'
				};
			});
		}
	};
}

function shortHead() {
	try {
		return execFileSync('git', ['rev-parse', '--short', 'HEAD'], {
			cwd: repoRoot,
			encoding: 'utf8'
		}).trim();
	} catch {
		return 'unknown';
	}
}

const assets = collectAssets();
if (assets.length === 0) {
	console.error(
		[
			'',
			`[경고] ${webDist} 가 비어 있습니다.`,
			'',
			'웹 UI 없이 번들을 만듭니다. 이대로 실행하면 안내 페이지만 뜹니다.',
			'먼저 아래를 실행하세요.',
			'',
			'  mise run web:build',
			''
		].join('\n')
	);
}

// minify 하지 않는다. 이 파일은 커밋되므로 줄 구조가 남아야 git 이 델타를
// 만들 수 있고, 대회장에서 스택 트레이스를 읽을 일이 생기면 그게 전부다.
const result = await build({
	entryPoints: [join(serverDir, 'src', 'main.ts')],
	outfile: outFile,
	bundle: true,
	platform: 'node',
	target: 'node22',
	format: 'esm',
	minify: false,
	keepNames: true,
	sourcemap: false,
	logLevel: 'info',
	// scripts/licenses.mjs 가 이것을 읽어 번들에 실제로 들어간
	// node_modules 패키지를 알아낸다. 번들 내용에는 영향이 없다.
	metafile: true,
	// CJS 의존성 몇 개가 런타임에 require 를 부른다(@smithy 압축 미들웨어가
	// node:zlib 을 그렇게 가져온다). ESM 번들에는 require 가 없으므로 esbuild 가
	// "Dynamic require is not supported" 를 던지는 shim 을 심는데, 그 shim 은
	// 모듈 스코프에 require 가 있으면 그것을 쓴다. 여기서 하나 만들어 준다.
	banner: {
		js: [
			'#!/usr/bin/env node',
			"import { createRequire as __nodeCreateRequire } from 'node:module';",
			'const require = __nodeCreateRequire(import.meta.url);'
		].join('\n')
	},
	plugins: [virtualAssetsPlugin(assets)]
});

const bytes = statSync(outFile).size;

mkdirSync(dirname(outFile), { recursive: true });
writeFileSync(join(serverDir, 'dist', 'meta.json'), JSON.stringify(result.metafile, null, '\t'));

// 번들에는 의존성이 전부 인라인되어 있다. 이 파일만 따로 건네받은 사람도
// 고지를 볼 수 있도록 라이선스와 서드파티 고지를 산출물 옆에 함께 둔다.
for (const name of ['LICENSE', 'THIRD_PARTY_NOTICES.md']) {
	try {
		copyFileSync(join(repoRoot, name), join(serverDir, 'dist', name));
	} catch {
		console.error(`[경고] ${name} 을 dist 로 복사하지 못했습니다. mise run licenses 를 먼저 돌리세요.`);
	}
}

writeFileSync(
	join(serverDir, 'dist', 'BUILD.txt'),
	[
		`commit: ${shortHead()}`,
		`built : ${new Date().toISOString().replace(/\.\d+Z$/, 'Z')}`,
		`node  : ${process.version}`,
		`assets: ${assets.length}개`,
		''
	].join('\n')
);

console.error(
	`\n  ${relative(repoRoot, outFile)}  ${(bytes / 1024 / 1024).toFixed(1)}MB  자산 ${assets.length}개\n`
);
