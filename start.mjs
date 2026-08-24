#!/usr/bin/env node
//
// 대시보드를 띄운다.
//
//   node start.mjs
//   node start.mjs --port 9000 --verbose
//   npm start -- --port 9000
//
// 대회장에는 Go 툴체인이 없고 node 만 허용된다. 그래서 여기서 하는 일은 미리
// 만들어 커밋해 둔 것을 자식 프로세스로 띄우는 것뿐이다 — bin/ 의 Go 실행
// 파일이거나 server/dist 의 node 번들이고, 어느 쪽인지는 findEngine 이 정한다
// (SKILLS_DASHBOARD_ENGINE). 포트 폴백, 브라우저 자동 실행, graceful shutdown
// 은 양쪽 다 스스로 한다 — 다시 구현하지 않는다. 런처가 더하는 것은 엔진
// 선택과 .env 전달이다.
//
// 의존성은 하나도 없다. node_modules 없이 갓 clone 한 저장소에서 돌아야 한다.

import { spawn } from 'node:child_process';

import { findEngine, killTree, withEnvFlag } from './scripts/launcher.mjs';

const engine = findEngine();
const args = [...engine.args, ...withEnvFlag(process.argv.slice(2))];

console.error(
	`대시보드를 시작합니다. 종료하려면 Ctrl+C 를 누르세요.\n  [${engine.kind}] ${engine.label}\n`
);

// stdio 를 물려준다. 바이너리의 로그가 그대로 흐르고, 실패했을 때 stdin 을
// 읽어 창을 붙잡는 reportFatal 경로도 살아 있다. 파이프로 바꾸면 그게 깨진다.
const child = spawn(engine.command, args, { stdio: 'inherit' });

child.on('error', (err) => {
	const why =
		err.code === 'EACCES'
			? '실행 권한이 없습니다.'
			: err.code === 'ENOENT'
				? '실행 파일이 사라졌거나 손상되었습니다.'
				: err.message;
	console.error(`\n대시보드를 시작하지 못했습니다.\n  ${engine.label}\n  ${why}\n`);
	process.exit(1);
});

child.on('exit', (code, signal) => {
	// Ctrl+C 로 내린 것은 실패가 아니다. 빨간 오류로 끝나면 정상 종료와
	// 진짜 고장이 구분되지 않는다.
	process.exit(signal === 'SIGINT' || signal === 'SIGTERM' ? 0 : (code ?? 1));
});

let stopping = false;

function stop() {
	// Windows 콘솔은 Ctrl+C 를 프로세스 그룹 전체에 이미 보냈다. 여기서 kill
	// 하면 TerminateProcess 가 되어 바이너리의 10초 graceful shutdown 을
	// 빼앗는다. 기다리는 쪽이 옳다.
	if (process.platform === 'win32') return;
	if (stopping) {
		killTree(child, 'SIGKILL'); // 두 번째 Ctrl+C 는 참지 않는다
		return;
	}
	stopping = true;
	killTree(child, 'SIGTERM');
}

// 핸들러를 다는 것 자체에 의미가 있다. 달지 않으면 node 가 Ctrl+C 에 먼저
// 죽고, 자식이 고아가 된 채 포트를 붙들고 남는다.
for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(signal, stop);

// 런처가 예외로 죽어도 자식을 남기지 않는다. 창을 강제로 닫는 경우까지는
// 막지 못한다 — 그때는 README 의 복구 명령을 쓴다.
process.on('exit', () => killTree(child));
