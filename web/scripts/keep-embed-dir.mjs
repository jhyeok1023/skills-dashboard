// adapter-static 은 출력 디렉터리를 먼저 비우고, 그때 .gitkeep 도 함께 지운다.
// 그 파일이 없으면 internal/web/embed.go 의 `//go:embed all:dist` 가 매칭 0개로
// 컴파일 에러가 나므로, 프론트를 빌드한 적 없는 clone 에서 `go build` 가 죽는다.
//
// vite build 뒤에 붙여 두는 이유는 빌드 경로가 하나가 아니기 때문이다 — mise
// 태스크, npm run build, playwright 의 webServer 가 각각 부른다. 한 군데에만
// 두면 나머지 경로가 조용히 파일을 지우고, 증상은 몇 커밋 뒤에 나타난다.

import { writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = resolve(
	dirname(fileURLToPath(import.meta.url)),
	'..',
	'..',
	'internal',
	'web',
	'dist'
);

writeFileSync(
	join(dist, '.gitkeep'),
	'이 파일은 지우지 마세요. //go:embed all:dist 는 매칭 파일이 0개면 컴파일 에러입니다.'
);
