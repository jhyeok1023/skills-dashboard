// 런처가 공유하는 세 가지 판단: 어느 엔진으로 띄울지, 어느 실행 파일을 띄울지,
// .env 를 어떻게 넘길지.
//
// start.mjs 와 dev.mjs 가 같은 답을 내야 한다. 갈라지면 한쪽에서는 되던 것이
// 다른 쪽에서 안 되고, 그 차이가 어디서 왔는지 보이지 않는다.
//
// 표준 라이브러리만 쓴다. 갓 clone 한 저장소에는 node_modules 가 없다.

import { accessSync, chmodSync, constants, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

const exeSuffix = process.platform === 'win32' ? '.exe' : '';

// 탐색 순서에 의미가 있다.
//
//   1. SKILLS_DASHBOARD_BIN — 다른 빌드를 임시로 물려 볼 때의 탈출구.
//   2. bin/<platform>-<arch> — `mise run build` 의 산출물. 이름은 GOOS/GOARCH 가
//      아니라 node 의 process.platform/arch 를 따른다. 런처가 두 어휘를 번역하지
//      않고 그대로 조립하게 하려는 것이다.
//   3. bin/skills-dashboard — 손으로 복사해 둔 경우.
//   4. dist/ — 다른 자리에 빌드해 둔 경우의 여지.
//
// bin/ 이 dist/ 보다 먼저인 것은 `mise run build` 가 쓰는 곳이 bin/ 이기
// 때문이다. 방금 만든 다른 빌드를 강제하려면 1번으로 뒤집는다.
function candidatePaths() {
	return [
		process.env.SKILLS_DASHBOARD_BIN,
		join(repoRoot, 'bin', `skills-dashboard-${process.platform}-${process.arch}${exeSuffix}`),
		join(repoRoot, 'bin', `skills-dashboard${exeSuffix}`),
		join(repoRoot, 'dist', `skills-dashboard${exeSuffix}`)
	].filter(Boolean);
}

/** findBinary 는 띄울 실행 파일의 경로를 돌려준다. 없으면 안내하고 종료한다. */
export function findBinary() {
	const tried = candidatePaths();
	const found = tried.find((p) => existsSync(p));
	if (!found) {
		console.error(
			[
				'',
				'대시보드 실행 파일을 찾지 못했습니다.',
				'',
				'다음을 확인했습니다:',
				...tried.map((p) => `  ${p}`),
				'',
				'Go 엔진은 아래로 만듭니다. Go 툴체인이 필요합니다.',
				'',
				'  mise run build',
				'',
				'Go 툴체인이 없다면 node 엔진을 쓰세요. node 만 있으면 됩니다.',
				'',
				'  npm --prefix server ci',
				'  mise run node:build',
				''
			].join('\n')
		);
		process.exit(1);
	}
	ensureExecutable(found);
	return found;
}

/** 순수 node 서버 번들. 있으면 실행 파일 없이도 대시보드가 뜬다. */
export const nodeBundle = join(repoRoot, 'server', 'dist', 'skills-dashboard.mjs');

const engineKinds = ['node', 'binary'];

/**
 * findEngine 은 무엇을 띄울지 정한다.
 *
 * 트랙이 둘이다. `binary` 는 Go 백엔드를 빌드한 bin/ 의 실행 파일이고, `node`
 * 는 TypeScript 백엔드를 esbuild 로 묶은 server/dist 의 번들이다. 대회장에서
 * 실행이 허용된 목록에 Go 가 없고 node 는 있어서, 허용된 런타임 위에서 도는
 * 두 번째 트랙을 만들었다. 명령은 양쪽 다 `node start.mjs` 하나로 유지된다.
 * 왜 둘인지는 README 의 "엔진이 둘입니다".
 *
 *   SKILLS_DASHBOARD_ENGINE=node    번들을 띄운다
 *   SKILLS_DASHBOARD_ENGINE=binary  실행 파일을 띄운다
 *   (지정하지 않으면) 번들이 있으면 node, 없으면 binary
 *
 * 기본값이 번들 쪽인 이유는 하나다. 번들을 만들어 두었다는 것은 이 저장소가
 * node 트랙을 갖췄다는 뜻이고, 그렇다면 실제로 돌릴 것도 그쪽이다.
 */
export function findEngine() {
	const requested = process.env.SKILLS_DASHBOARD_ENGINE?.trim();
	if (requested && !engineKinds.includes(requested)) {
		console.error(
			[
				'',
				`SKILLS_DASHBOARD_ENGINE 값이 잘못되었습니다: ${requested}`,
				`  ${engineKinds.join(' 또는 ')} 중 하나여야 합니다.`,
				''
			].join('\n')
		);
		process.exit(1);
	}

	const kind = requested || (existsSync(nodeBundle) ? 'node' : 'binary');

	if (kind === 'node') {
		if (!existsSync(nodeBundle)) {
			console.error(
				[
					'',
					'node 엔진을 요청했지만 번들이 없습니다.',
					'',
					`  ${nodeBundle}`,
					'',
					'아래로 만드세요. Go 툴체인이 없어도 됩니다.',
					'',
					'  npm --prefix server ci',
					'  mise run node:build',
					'',
					'Go 툴체인이 있다면 `mise run build` 로 만든 뒤',
					'SKILLS_DASHBOARD_ENGINE=binary 로 Go 엔진을 쓸 수도 있습니다.',
					''
				].join('\n')
			);
			process.exit(1);
		}
		// 번들을 실행하는 것은 지금 돌고 있는 바로 그 node 다. PATH 에 다른
		// node 가 있어도 런처와 서버의 런타임이 갈라지지 않는다.
		return { kind, command: process.execPath, args: [nodeBundle], label: nodeBundle };
	}

	const bin = findBinary();
	return { kind, command: bin, args: [], label: bin };
}


// 빌드 산출물을 zip 이나 USB 로 옮기면 실행 비트가 사라진다. Windows 에는 해당
// 개념이 없다.
function ensureExecutable(bin) {
	if (process.platform === 'win32') return;
	try {
		accessSync(bin, constants.X_OK);
	} catch {
		try {
			chmodSync(bin, 0o755);
		} catch (err) {
			console.error(`실행 권한을 줄 수 없습니다: ${bin}`);
			console.error(`  chmod +x "${bin}" 를 직접 실행하세요. (${err.message})`);
			process.exit(1);
		}
	}
}

/**
 * withEnvFlag 는 저장소 루트의 .env 를 엔진에 명시적으로 넘긴다.
 *
 * 엔진은 자기 옆의 .env 를 먼저, 없으면 ~/.skills-dashboard/.env 를 본다.
 * 현재 작업 디렉터리는 일부러 보지 않는다 — 어디서 실행했느냐로 읽는 키가
 * 달라지지 않게 하려는 설계다(README 참고). 그래서 bin/ 안의 실행 파일을 그냥
 * 띄우면 저장소 루트의 .env 가 무시되고, 화면에는 자격증명 오류만 뜬다.
 *
 * 없는 경로를 --env 로 넘기면 엔진이 즉시 죽는다(오타를 삼키지 않겠다는
 * 의도적 동작). 그래서 파일이 있을 때만 넘기고, 없으면 엔진 자신의 탐색
 * 순서에 맡긴 뒤 무엇을 해야 하는지 알려 준다.
 */
export function withEnvFlag(args) {
	// Go 의 flag 패키지는 -env, --env, --env=경로 를 모두 받는다.
	if (args.some((a) => /^--?env(=|$)/.test(a))) return args;

	const envPath = join(repoRoot, '.env');
	if (existsSync(envPath)) return [...args, '--env', envPath];

	console.error(
		[
			'',
			`[안내] ${envPath} 가 없습니다.`,
			'',
			'자격증명 없이도 대시보드는 뜹니다. 설정 화면이 무엇을 채워야 하는지 알려 줍니다.',
			'지금 넣으려면 .env.example 을 복사한 뒤 AWS_ACCESS_KEY_ID ·',
			'AWS_SECRET_ACCESS_KEY · AWS_REGION 을 채우고 다시 실행하세요.',
			'',
			'  Windows : copy .env.example .env',
			'  그 외    : cp .env.example .env',
			''
		].join('\n')
	);
	return args;
}

/** killTree 는 자식을 정리한다. 이미 죽었으면 아무것도 하지 않는다. */
export function killTree(child, signal) {
	if (!child || child.exitCode !== null || child.signalCode !== null) return;
	child.kill(signal);
}
