#!/usr/bin/env node
//
// 개발용. Vite(:5173)와 API 바이너리(:8080)를 함께 띄운다.
//
//   npm run dev
//
// 대회장에서는 쓰지 않는다 — web/node_modules 가 필요하다. 프로덕션 실행은
// start.mjs 하나로 끝난다.

import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

import { findBinary, killTree, repoRoot, withEnvFlag } from './launcher.mjs';

const webDir = join(repoRoot, 'web');

// npm 을 spawn 하지 않는다. Windows 에서는 npm.cmd 라 shell 을 거쳐야 하고,
// shell 이 끼면 자식 정리가 한 겹 더 어려워진다. vite 의 진입점을 node 로
// 직접 연다 — 자식이 진짜 node 프로세스라 kill 이 정확히 먹는다.
const viteBin = join(webDir, 'node_modules', 'vite', 'bin', 'vite.js');
if (!existsSync(viteBin)) {
	console.error('\nweb/node_modules 가 없습니다. 먼저 실행하세요.\n\n  npm run install:web\n');
	process.exit(1);
}

const bin = findBinary();

// --port 를 명시한다. vite.config.ts 의 프록시 타깃이 127.0.0.1:8080 으로
// 고정이라, 명시하지 않으면 8080 이 막혔을 때 바이너리가 조용히 8081 로 옮겨
// 가고 프록시는 8080 을 계속 두드린다. 명시하면 대신 "포트를 열 수 없습니다"
// 로 즉시 죽는다 — 그쪽이 옳다.
//
// --open=false 는 `=` 가 필수다. Go 의 bool 플래그는 `--open false` 를 받지
// 않고 false 를 위치 인자로 흘려보낸다. 브라우저는 vite 쪽 주소로 열어야 한다.
const api = spawn(bin, withEnvFlag(['--port', '8080', '--open=false']), {
	stdio: ['ignore', 'pipe', 'pipe']
});
const web = spawn(process.execPath, [viteBin, 'dev'], {
	cwd: webDir,
	stdio: ['ignore', 'pipe', 'pipe']
});

prefix(api, '[api]');
prefix(web, '[web]');

// 줄 단위로 모아서 접두사를 붙인다. 파이프 경계는 줄 경계와 무관하므로,
// 버퍼링 없이 붙이면 로그가 중간에서 잘린다.
function prefix(child, label) {
	for (const stream of [child.stdout, child.stderr]) {
		let rest = '';
		stream.setEncoding('utf8');
		stream.on('data', (chunk) => {
			const lines = (rest + chunk).split('\n');
			rest = lines.pop() ?? '';
			for (const line of lines) process.stdout.write(`${label} ${line}\n`);
		});
		stream.on('end', () => {
			if (rest) process.stdout.write(`${label} ${rest}\n`);
		});
	}
}

let down = false;

function shutdown(code, who) {
	if (down) return;
	down = true;
	if (who) console.error(`\n${who} 가 종료되어 나머지도 정리합니다.`);
	killTree(api);
	killTree(web);
	process.exitCode = code;
}

api.on('exit', (code) => shutdown(code ?? 1, '[api]'));
web.on('exit', (code) => shutdown(code ?? 1, '[web]'));
api.on('error', () => shutdown(1, '[api]'));
web.on('error', () => shutdown(1, '[web]'));

for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => shutdown(0));
process.on('exit', () => {
	killTree(api);
	killTree(web);
});

console.error('\n  UI  http://127.0.0.1:5173   (여기를 엽니다)\n  API http://127.0.0.1:8080\n');
