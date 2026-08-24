// 런처가 공유하는 두 가지 판단: 어느 실행 파일을 띄울지, .env 를 어떻게 넘길지.
//
// start.mjs 와 dev.mjs 가 같은 답을 내야 한다. 갈라지면 개발 중에는 되던 것이
// 대회장에서 안 되고, 그 차이가 어디서 왔는지 보이지 않는다.
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
//   2. bin/<platform>-<arch> — 커밋해서 대회장까지 가져가는 것. clone 하면 이게
//      잡힌다. 이름은 GOOS/GOARCH 가 아니라 node 의 process.platform/arch 를
//      따른다. 런처가 문자열을 변환하지 않고 그대로 조립하게 하려는 것이다.
//   3. bin/skills-dashboard — 손으로 복사해 둔 경우.
//   4. dist/ — mise 로컬 빌드 산출물. .gitignore 대상이라 개발 중에만 있다.
//
// bin/ 이 dist/ 보다 먼저다. 로컬에서 도는 것과 대회장에서 도는 것을 같게
// 하려는 것이고, 방금 만든 빌드를 쓰고 싶으면 1번으로 뒤집는다.
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
				'Go 툴체인이 있는 기계라면 아래로 다시 만드세요.',
				'',
				'  mise run build',
				'',
				'없다면 bin/ 폴더가 clone 에 함께 내려왔는지 보세요. git-lfs 를 쓰지',
				'않으므로 clone 만으로 실행 파일이 들어 있어야 정상입니다.',
				''
			].join('\n')
		);
		process.exit(1);
	}
	ensureExecutable(found);
	return found;
}

// git 은 실행 비트를 보존하지만 zip 이나 USB 를 거치면 사라진다. Windows 에는
// 해당 개념이 없다.
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
 * withEnvFlag 는 저장소 루트의 .env 를 바이너리에 명시적으로 넘긴다.
 *
 * 바이너리는 자기 옆의 .env 를 먼저, 없으면 ~/.skills-dashboard/.env 를 본다.
 * 현재 작업 디렉터리는 일부러 보지 않는다 — 어디서 실행했느냐로 읽는 키가
 * 달라지지 않게 하려는 설계다(README 참고). 그래서 bin/ 안의 실행 파일을 그냥
 * 띄우면 저장소 루트의 .env 가 무시되고, 화면에는 자격증명 오류만 뜬다.
 *
 * 없는 경로를 --env 로 넘기면 바이너리가 즉시 죽는다(오타를 삼키지 않겠다는
 * 의도적 동작). 그래서 파일이 있을 때만 넘기고, 없으면 바이너리 자신의 탐색
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
