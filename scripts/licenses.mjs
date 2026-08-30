#!/usr/bin/env node
//
// THIRD_PARTY_NOTICES.md 를 만든다.
//
//   mise run licenses
//
// 두 엔진과 SPA 에 실제로 들어가는 서드파티만 모은다. 선언된 의존성이 아니라
// 빌드가 남긴 사실을 읽는다 — go 는 컴파일 그래프, node 는 esbuild metafile,
// SPA 는 rolldown 이 번들한 모듈 id. 셋 다 빌드 산출물이므로 이 스크립트를
// 돌리기 전에 두 엔진을 빌드해야 한다.
//
// 형식은 두 축을 따른다. 라이선스 이름은 SPDX 식별자(ISO/IEC 5962:2021)를
// 쓰고, 문서 배치는 AOSP/Google NOTICE 관례 — 구성요소 식별정보와 라이선스
// 전문을 구분선으로 나눠 싣는다. 다만 Apache-2.0 전문이 40여 번 반복되지
// 않도록, 같은 텍스트는 해시로 묶어 한 번만 싣고 적용 대상을 함께 적는다.

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outFile = join(repoRoot, 'THIRD_PARTY_NOTICES.md');

// 산출물 이름. 한 패키지가 여러 곳에 들어가면 전부 붙는다.
const GO = 'go binary';
const NODE = 'node bundle';
const SPA = 'spa';

// 라이선스 파일을 담지 않고 배포된 패키지가 있다. package.json 이 선언한
// SPDX 식별자는 있으므로, 그 라이선스의 전문이 저작권자를 본문에 담지 않는
// 종류라면 다른 구성요소에서 얻은 동일 전문을 그대로 쓸 수 있다.
//
// Apache-2.0 과 MPL-2.0 이 그렇다. 두 전문은 고정 문서이고 저작권자는 본문이
// 아니라 NOTICE 와 파일 헤더에 적힌다. MIT·ISC·BSD 는 본문에 저작권자가
// 박히므로 남의 것을 가져다 쓰면 틀린 고지가 된다 — 여기 넣으면 안 된다.
const CANONICAL_TEXT_OK = new Set(['Apache-2.0', 'MPL-2.0']);

function die(lines) {
	console.error('\n' + lines.join('\n') + '\n');
	process.exit(1);
}

// ---------------------------------------------------------------------------
// 라이선스 파일 찾기
//
// 확장자를 가정하면 안 된다. 실제로 aws-sdk-go-v2 는 LICENSE.txt, smithy-go 는
// LICENSE, svelte 는 LICENSE.md 다.
// ---------------------------------------------------------------------------

const LICENSE_RE = /^(licen[cs]e|copying|unlicense)(\.(txt|md|markdown|rst))?$/i;
const NOTICE_RE = /^notice(\.(txt|md|markdown))?$/i;

function findFiles(dir, re) {
	let entries;
	try {
		entries = readdirSync(dir);
	} catch {
		return [];
	}
	return entries
		.filter((name) => re.test(name))
		.filter((name) => {
			try {
				return statSync(join(dir, name)).isFile();
			} catch {
				return false;
			}
		})
		.sort()
		.map((name) => ({ name, text: readFileSync(join(dir, name), 'utf8').replace(/\r\n/g, '\n').trim() }))
		.filter(({ text }) => text.length > 0);
}

// 라이선스 전문에서 저작권 줄을 뽑는다. 없으면 package.json 이 메운다.
function copyrightFrom(text) {
	const lines = text
		.split('\n')
		.map((l) => l.trim())
		.filter((l) => /^(copyright|\(c\)|©)/i.test(l) && !/^copyright\s*\[/i.test(l));
	return [...new Set(lines)].join('\n');
}

// ---------------------------------------------------------------------------
// 수집
// ---------------------------------------------------------------------------

const components = new Map(); // key -> { name, version, spdx, source, artifacts:Set, licenses, notices, copyright }

function add(key, entry) {
	const found = components.get(key);
	if (found) {
		for (const a of entry.artifacts) found.artifacts.add(a);
		return;
	}
	components.set(key, entry);
}

// --- Go: 바이너리에 실제로 링크되는 모듈만 -------------------------------
function collectGo() {
	let out;
	try {
		out = execFileSync(
			'go',
			[
				'list',
				'-deps',
				'-f',
				'{{if and .Module .Module.Version}}{{.Module.Path}}\t{{.Module.Version}}\t{{.Module.Dir}}{{end}}',
				'./cmd/skills-dashboard'
			],
			{ cwd: repoRoot, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 }
		);
	} catch (err) {
		die([
			'go list 를 실행하지 못했습니다.',
			'',
			'  mise exec -- node scripts/licenses.mjs',
			'',
			'처럼 go 툴체인이 잡힌 상태로 돌리세요. 원인:',
			String(err.message).trim()
		]);
	}

	const rows = [...new Set(out.split('\n').map((l) => l.trim()).filter(Boolean))];
	for (const row of rows) {
		const [path, version, dir] = row.split('\t');
		const licenses = findFiles(dir, LICENSE_RE);
		const notices = findFiles(dir, NOTICE_RE);
		add(`go:${path}@${version}`, {
			name: path,
			version,
			source: `https://${path}`,
			artifacts: new Set([GO]),
			licenses,
			notices,
			spdx: null,
			copyright: licenses.map(({ text }) => copyrightFrom(text)).find(Boolean) ?? ''
		});
	}
	return rows.length;
}

// --- npm: 파일 경로에서 패키지 루트로 환원 ---------------------------------
//
// 위로 올라가며 package.json 이 있는 첫 디렉터리가 패키지 루트다. 스코프와
// 중첩 node_modules 를 모두 이렇게 처리할 수 있다 — web 에는
// @layerstack/svelte-actions/node_modules/@layerstack/utils 같은 사례가 있다.
function packageRootOf(file) {
	let dir = dirname(resolve(file));
	while (true) {
		if (existsSync(join(dir, 'package.json'))) return dir;
		const up = dirname(dir);
		if (up === dir) return null;
		dir = up;
	}
}

// package.json 의 repository 는 형태가 제각각이다 — 완전한 URL, git+https,
// git://, 그리고 "techniq/layerstack" 같은 GitHub 축약형까지. 축약형을 그대로
// 링크로 쓰면 깨진 링크가 된다.
function repoUrl(repo, directory) {
	if (!repo) return null;
	let url = repo
		.replace(/^git\+/, '')
		.replace(/^git:\/\//, 'https://')
		.replace(/^ssh:\/\/git@/, 'https://')
		.replace(/\.git$/, '');
	if (/^(github|gitlab|bitbucket):/.test(url)) {
		const [host, path] = url.split(':');
		url = `https://${host === 'github' ? 'github.com' : host === 'gitlab' ? 'gitlab.com' : 'bitbucket.org'}/${path}`;
	} else if (!/^https?:\/\//.test(url)) {
		if (!/^[\w.-]+\/[\w.-]+$/.test(url)) return null;
		url = `https://github.com/${url}`;
	}
	return directory ? `${url}/tree/HEAD/${directory}` : url;
}

function addNpmPackage(root, artifact) {
	let pkg;
	try {
		pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
	} catch {
		return;
	}
	if (!pkg.name) return;

	const key = `npm:${pkg.name}@${pkg.version ?? '0.0.0'}`;
	if (components.has(key)) {
		components.get(key).artifacts.add(artifact);
		return;
	}

	const licenses = findFiles(root, LICENSE_RE);
	const spdx = typeof pkg.license === 'string' ? pkg.license : (pkg.license?.type ?? null);
	const repo = typeof pkg.repository === 'string' ? pkg.repository : pkg.repository?.url;
	const author = typeof pkg.author === 'string' ? pkg.author : pkg.author?.name;
	const dir = typeof pkg.repository === 'object' ? pkg.repository?.directory : null;

	add(key, {
		name: pkg.name,
		version: pkg.version ?? '',
		source: pkg.homepage ?? repoUrl(repo, dir) ?? `https://www.npmjs.com/package/${pkg.name}`,
		artifacts: new Set([artifact]),
		licenses,
		notices: findFiles(root, NOTICE_RE),
		spdx,
		copyright: licenses.map(({ text }) => copyrightFrom(text)).find(Boolean) || (author ? `Copyright (c) ${author}` : '')
	});
}

function collectFromPaths(paths, baseDir, artifact) {
	const roots = new Set();
	for (const p of paths) {
		const abs = resolve(baseDir, p);
		if (!abs.split(sep).includes('node_modules')) continue;
		const root = packageRootOf(abs);
		if (root) roots.add(root);
	}
	for (const root of roots) addNpmPackage(root, artifact);
	return roots.size;
}

// --- node 엔진: esbuild metafile -------------------------------------------
function collectNode() {
	const meta = join(repoRoot, 'server', 'dist', 'meta.json');
	if (!existsSync(meta)) {
		die([
			'server/dist/meta.json 이 없습니다.',
			'',
			'  npm run install:server && mise run node:build',
			'',
			'를 먼저 돌려 node 엔진을 빌드하세요.'
		]);
	}
	const inputs = Object.keys(JSON.parse(readFileSync(meta, 'utf8')).inputs ?? {});
	return collectFromPaths(inputs, join(repoRoot, 'server'), NODE);
}

// --- SPA: rolldown 이 번들한 모듈 id ---------------------------------------
function collectSpa() {
	const list = join(repoRoot, 'web', '.licenses', 'spa-modules.json');
	if (!existsSync(list)) {
		die([
			'web/.licenses/spa-modules.json 이 없습니다.',
			'',
			'  npm run install:web && mise run web:build',
			'',
			'를 먼저 돌려 SPA 를 빌드하세요.'
		]);
	}
	const ids = JSON.parse(readFileSync(list, 'utf8'));
	return collectFromPaths(ids, join(repoRoot, 'web'), SPA);
}

// ---------------------------------------------------------------------------
// 렌더링
// ---------------------------------------------------------------------------

// SPDX 식별자를 라이선스 전문에서 알아본다. package.json 이 말해 주지 않는
// go 모듈 때문에 필요하다.
function detectSpdx(text) {
	const t = text.replace(/\s+/g, ' ');
	if (/Apache License Version 2\.0/i.test(t)) return 'Apache-2.0';
	if (/Permission is hereby granted, free of charge/i.test(t)) return 'MIT';
	if (/Permission to use, copy, modify, and\/or distribute this software/i.test(t)) return 'ISC';
	if (/Redistribution and use in source and binary forms/i.test(t)) {
		if (/Neither the name of/i.test(t)) return 'BSD-3-Clause';
		return 'BSD-2-Clause';
	}
	if (/This is free and unencumbered software released into the public domain/i.test(t)) return 'Unlicense';
	if (/Mozilla Public License Version 2\.0/i.test(t)) return 'MPL-2.0';
	if (/Blue Oak Model License/i.test(t)) return 'BlueOak-1.0.0';
	return null;
}

function render(list) {
	// 라이선스 전문을 해시로 묶는다.
	const groups = new Map(); // hash -> { spdx, text, members:[] }
	for (const c of list) {
		for (const { text } of c.licenses) {
			const hash = createHash('sha256').update(text).digest('hex');
			if (!groups.has(hash)) groups.set(hash, { spdx: c.spdx ?? detectSpdx(text), text, members: [] });
			groups.get(hash).members.push(c);
		}
	}
	const ordered = [...groups.values()].sort((a, b) => {
		const s = (a.spdx ?? 'zzz').localeCompare(b.spdx ?? 'zzz');
		return s !== 0 ? s : b.members.length - a.members.length;
	});

	const out = [];
	out.push('# Third-Party Notices');
	out.push('');
	out.push('skills-dashboard 자체는 BSD 3-Clause 로 배포됩니다(`LICENSE`). 이 문서는 그와 별개로,');
	out.push('빌드 산출물 **안에 들어가는** 서드파티 소프트웨어의 저작권 고지와 라이선스 전문을 싣습니다.');
	out.push('');
	out.push('덮는 산출물은 셋입니다.');
	out.push('');
	out.push('| 이름 | 실체 | 서드파티가 들어가는 방식 |');
	out.push('| --- | --- | --- |');
	out.push('| `go binary` | `bin/skills-dashboard-<플랫폼>` | Go 모듈 정적 링크 + SPA `//go:embed` |');
	out.push('| `node bundle` | `server/dist/skills-dashboard.mjs` | esbuild 가 의존성 전부 인라인 + SPA base64 인라인 |');
	out.push('| `spa` | `internal/web/dist` | 두 엔진 모두에 임베드 |');
	out.push('');
	out.push('산출물을 남에게 넘길 때는 `LICENSE` 와 이 파일을 함께 넘기세요. 빌드가 두 파일을');
	out.push('`bin/` 과 `server/dist/` 에 함께 복사해 둡니다.');
	out.push('');
	out.push('라이선스 이름은 SPDX 식별자입니다. 같은 라이선스 전문은 한 번만 싣고 적용 대상을 함께 적었습니다.');
	out.push('');
	out.push('이 문서는 `mise run licenses` 가 생성합니다. 직접 고치지 마세요.');
	out.push('');

	// --- 구성요소 표 ---
	out.push('## 구성요소');
	out.push('');
	out.push(`총 ${list.length}개.`);
	out.push('');
	out.push('| 구성요소 | 버전 | 라이선스 | 산출물 |');
	out.push('| --- | --- | --- | --- |');
	for (const c of list) {
		const spdx = c.spdx ?? c.licenses.map(({ text }) => detectSpdx(text)).find(Boolean) ?? '(파일 참조)';
		const where = [GO, NODE, SPA].filter((a) => c.artifacts.has(a)).join(' · ');
		out.push(`| [${c.name}](${c.source}) | ${c.version} | ${spdx} | ${where} |`);
	}
	out.push('');

	// --- NOTICE (Apache-2.0 4(d)) ---
	const withNotice = list.filter((c) => c.notices.length > 0);
	if (withNotice.length > 0) {
		out.push('## NOTICE');
		out.push('');
		out.push('Apache License 2.0 제4조 (d) 에 따라 원 배포물의 NOTICE 내용을 그대로 옮깁니다.');
		out.push('');
		for (const c of withNotice) {
			for (const n of c.notices) {
				out.push(`### ${c.name} ${c.version} — \`${n.name}\``);
				out.push('');
				out.push('```');
				out.push(n.text);
				out.push('```');
				out.push('');
			}
		}
	}

	// --- 라이선스 전문 ---
	out.push('## 라이선스 전문');
	out.push('');
	for (const [i, g] of ordered.entries()) {
		out.push('---');
		out.push('');
		out.push(`### ${i + 1}. ${g.spdx ?? '아래 전문 참조'}`);
		out.push('');
		out.push(`다음 ${g.members.length}개 구성요소에 적용됩니다.`);
		out.push('');
		for (const m of [...g.members].sort((a, b) => a.name.localeCompare(b.name))) {
			const note = m.borrowedFrom
				? ` — 이 패키지는 라이선스 파일 없이 배포됩니다. \`package.json\` 이 선언한 \`${m.spdx}\` 의 전문을 \`${m.borrowedFrom}\` 에서 가져왔습니다.`
				: '';
			out.push(`- ${m.name} ${m.version}${note}`);
		}
		const copyrights = [...new Set(g.members.map((m) => m.copyright).filter(Boolean))];
		if (copyrights.length > 0) {
			out.push('');
			out.push('저작권자:');
			out.push('');
			out.push('```');
			for (const c of copyrights) out.push(c);
			out.push('```');
		}
		out.push('');
		out.push('```');
		out.push(g.text);
		out.push('```');
		out.push('');
	}

	return out.join('\n');
}

// ---------------------------------------------------------------------------

const goCount = collectGo();
const nodeCount = collectNode();
const spaCount = collectSpa();

const list = [...components.values()].sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));

// 라이선스 파일 없이 배포된 패키지를, 같은 SPDX 식별자의 전문으로 메운다.
// 어디서 가져왔는지는 문서에 그대로 밝힌다.
const canonical = new Map();
for (const c of list) {
	for (const { text } of c.licenses) {
		const id = c.spdx ?? detectSpdx(text);
		if (id && CANONICAL_TEXT_OK.has(id) && !canonical.has(id)) canonical.set(id, { text, from: c });
	}
}
for (const c of list) {
	if (c.licenses.length > 0) continue;
	const hit = c.spdx && canonical.get(c.spdx);
	if (!hit) continue;
	c.licenses = [{ name: hit.from.licenses[0].name, text: hit.text }];
	c.borrowedFrom = hit.from.name;
}

// 그래도 전문을 못 찾은 것이 있으면 여기서 멈춘다. 조용히 빠지면 이 문서의
// 존재 이유가 없어진다.
const missing = list.filter((c) => c.licenses.length === 0);
if (missing.length > 0) {
	die([
		`라이선스 전문을 찾지 못한 구성요소가 ${missing.length}개 있습니다.`,
		'',
		...missing.map((c) => `  ${c.name} ${c.version}  (${[...c.artifacts].join(', ')})  선언: ${c.spdx ?? '없음'}`),
		'',
		'패키지 안에서 라이선스 파일을 확인하세요. 저작권자를 본문에 담지 않는',
		'라이선스라면 scripts/licenses.mjs 의 CANONICAL_TEXT_OK 로 메울 수 있습니다.'
	]);
}

writeFileSync(outFile, render(list));

console.error(
	`\n  THIRD_PARTY_NOTICES.md  구성요소 ${list.length}개  (go ${goCount} · node ${nodeCount} · spa ${spaCount})\n`
);
