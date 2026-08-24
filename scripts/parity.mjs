#!/usr/bin/env node
//
// 두 엔진이 같은 질문에 같은 답을 내는지 본다.
//
//   node scripts/parity.mjs
//   node scripts/parity.mjs --range 2h --period 5m
//
// Go 바이너리와 node 번들을 각각 띄우고, 같은 요청을 던져 응답을 비교한다.
// 이식이 끝날 때까지 이것이 유일한 채점표다 — Go 테스트 6,600줄이 지키던 동작을
// 옮겨 온 코드가 실제로 그대로 내는지 확인할 방법이 이것뿐이기 때문이다.
//
// 의존성은 없다. 저장소의 다른 스크립트와 같은 규칙이다.

import { spawn } from 'node:child_process';
import { setTimeout as sleep } from 'node:timers/promises';

import { sep } from 'node:path';

import { repoRoot } from './launcher.mjs';

const args = process.argv.slice(2);
const flag = (name, fallback) => {
	const i = args.indexOf(`--${name}`);
	return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
};

const range = flag('range', '1h');
const period = flag('period', '1m');
const goPort = Number(flag('go-port', '8090'));
const nodePort = Number(flag('node-port', '8091'));

const pages = ['overview', 'pod-logs', 'waf', 'targetgroup', 'kubernetes', 'database'];
const panels = [
	'targetgroup', 'pod-cpu', 'pod-mem', 'node-cpu', 'node-mem', 'node-disk',
	'counts', 'pod-status', 'rds-proxy', 'waf-metrics', 'pod-latency',
	'pod-status-codes', 'pod-status-breakdown', 'pod-errors', 'waf-traffic',
	'waf-blocked', 'waf-breakdown'
];
const discovery = ['loadbalancers', 'targetgroups', 'loggroups', 'rdsproxies', 'webacls', 'clusters'];

const endpoints = [
	'/api/health',
	'/api/meta',
	'/api/config',
	'/api/identity',
	...discovery.map((k) => `/api/discovery/${k}`),
	...pages.map((p) => `/api/page/${p}?range=${range}&period=${period}`),
	...panels.map((p) => `/api/panel/${p}?range=${range}&period=${period}`)
];

// 호출마다 달라지는 값. 창의 끝은 요청 시각에서 내림한 것이고, 스캔 바이트는
// CloudWatch 가 같은 쿼리에도 다르게 답한다. 비교에서 빼지 않으면 diff 가
// 언제나 붉게 뜬다.
const volatileKeys = new Set(['now', 'generatedAt', 'elapsedMs', 'at', 'bytesScanned']);

function normalise(value) {
	if (Array.isArray(value)) return value.map(normalise);
	if (value && typeof value === 'object') {
		const out = {};
		for (const key of Object.keys(value).sort()) {
			if (volatileKeys.has(key)) continue;
			out[key] = normalise(value[key]);
		}
		return out;
	}
	return value;
}

// 실행 파일 옆의 .env 를 찾는 경로는 엔진마다 다르다. bin/ 과 server/dist/ 는
// 서로 다른 자리에 있는 것이 맞으므로, 그 차이는 지운 뒤 비교한다.
function scrubPaths(text) {
	// JSON 안에서는 Windows 경로의 역슬래시가 두 개로 늘어난다. 날것과 이스케이프된
	// 형태를 둘 다 지워야 한다. 이스케이프 형태는 JSON.stringify 에게 물어 만든다 —
	// 손으로 역슬래시를 세는 것보다 틀릴 자리가 적다.
	const roots = [
		`${repoRoot}${sep}bin`,
		`${repoRoot}${sep}server${sep}dist`
	];
	let out = text;
	for (const root of roots) {
		const escaped = JSON.stringify(root).slice(1, -1);
		out = out.split(root).join('<exeDir>');
		out = out.split(escaped).join('<exeDir>');
	}
	return out;
}

async function fetchBody(port, path) {
	const res = await fetch(`http://127.0.0.1:${port}${path}`, {
		signal: AbortSignal.timeout(120_000)
	});
	const text = await res.text();
	let body;
	try {
		body = JSON.parse(text);
	} catch {
		body = { unparseable: text.slice(0, 200) };
	}
	return { status: res.status, body };
}

function start(engine, port) {
	const child = spawn(
		process.execPath,
		['start.mjs', '--port', String(port), '--open=false'],
		{
			cwd: repoRoot,
			env: { ...process.env, SKILLS_DASHBOARD_ENGINE: engine },
			stdio: ['ignore', 'ignore', 'pipe']
		}
	);
	child.stderr.setEncoding('utf8');
	child.stderr.on('data', (chunk) => {
		if (/level=(ERROR|WARN)/.test(chunk) && !/no .env found|AWS is unavailable/.test(chunk)) {
			process.stderr.write(`[${engine}] ${chunk}`);
		}
	});
	return child;
}

async function waitReady(port) {
	for (let i = 0; i < 60; i++) {
		try {
			const res = await fetch(`http://127.0.0.1:${port}/api/health`, {
				signal: AbortSignal.timeout(1000)
			});
			if (res.ok) return true;
		} catch {
			// 아직 뜨는 중이다.
		}
		await sleep(250);
	}
	return false;
}

const children = [start('binary', goPort), start('node', nodePort)];
const cleanup = () => children.forEach((c) => c.kill());
process.on('exit', cleanup);
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => process.exit(1));

if (!(await waitReady(goPort)) || !(await waitReady(nodePort))) {
	console.error('두 엔진 중 하나가 뜨지 않았습니다.');
	process.exit(1);
}

let same = 0;
let missing = 0;
const failures = [];

for (const path of endpoints) {
	const [go, nd] = await Promise.all([fetchBody(goPort, path), fetchBody(nodePort, path)]);

	// node 쪽이 아직 그 엔드포인트를 모르는 것은 실패가 아니라 진행 상황이다.
	if (nd.status === 404 && go.status !== 404) {
		missing++;
		console.log(`--  ${path}  (node 미구현)`);
		continue;
	}

	const a = scrubPaths(JSON.stringify(normalise(go.body), null, 1));
	const b = scrubPaths(JSON.stringify(normalise(nd.body), null, 1));
	if (go.status === nd.status && a === b) {
		same++;
		console.log(`OK  ${path}`);
		continue;
	}
	failures.push({ path, go, nd, a, b });
	console.log(`XX  ${path}  (${go.status} vs ${nd.status})`);
}

for (const f of failures) {
	console.log(`\n--- ${f.path}`);
	const left = f.a.split('\n');
	const right = f.b.split('\n');
	for (let i = 0; i < Math.max(left.length, right.length); i++) {
		if (left[i] !== right[i]) {
			console.log(`  go  : ${left[i] ?? '(없음)'}`);
			console.log(`  node: ${right[i] ?? '(없음)'}`);
		}
	}
}

console.log(`\n일치 ${same} · 미구현 ${missing} · 불일치 ${failures.length}`);
cleanup();
process.exit(failures.length === 0 ? 0 : 1);
