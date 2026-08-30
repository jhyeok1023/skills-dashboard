import { expect, test } from '@playwright/test';
import { mockApi, PAGES } from './fixtures';

/**
 * README 와 docs/behavior.md 에 넣을 화면을 찍는다.
 *
 * 픽스처는 layout.e2e.ts 가 쓰는 것을 그대로 쓴다. 캡처 전용 데이터를 따로
 * 만들면 문서의 그림과 테스트가 보는 화면이 갈라지고, 그 차이는 아무도
 * 발견하지 못한다. 여기 나오는 값은 전부 e2e/fixtures.ts 가 만든 가짜이며
 * AWS 계정이 필요 없다 — 찍는 데도, 읽는 데도.
 *
 *   SHOTS=1 npm --prefix web run test:e2e -- shots
 *
 * 리눅스에서 찍을 때는 폰트를 먼저 잡아야 한다. tokens.css 의 --font-ui /
 * --font-mono 스택은 macOS 와 Windows 폰트뿐이라 리눅스에는 하나도 없고,
 * sans-serif 가 한글 없는 폰트로 떨어지면 Chromium 이 글자마다 폴백하다가
 * 조합되지 않은 자모(ㅇㅣㅆㄴㅡㄴ)를 그린다. 화면 자체의 문제가 아니라 찍는
 * 기계의 문제이며, sans-serif · system-ui · monospace 를 Noto Sans (Mono) CJK
 * KR 로 보내는 fontconfig 를 FONTCONFIG_FILE 로 물리면 없어진다.
 */

// 설정과 트래픽 점검 화면에는 패널이 없다. layout.e2e.ts 의 open() 이 두는
// 예외와 같은 것이며, 갈라지면 한쪽이 스켈레톤을 찍는다.
const NO_PANELS = new Set(['/settings', '/check']);

// 기본 e2e 실행에는 딸려 돌지 않는다. 캡처는 docs/screenshots 를 덮어쓰므로,
// 폰트를 잡지 않은 기계에서 무심코 돌면 깨진 그림이 커밋된다.
test.describe('screenshots', () => {
	test.skip(!process.env.SHOTS, 'SHOTS=1 을 주었을 때만 찍는다');

	for (const { path, name } of PAGES) {
		test(name, async ({ page }) => {
			await mockApi(page);
			await page.goto(path);
			await expect(page.locator('h1')).toBeVisible();

			// 차트는 effect 안에서 마운트된다. 스켈레톤이 걷히기 전에 찍으면
			// 회색 상자만 남는다.
			if (!NO_PANELS.has(path)) {
				await page.locator('[data-panel]').first().waitFor();
			}
			await page.waitForTimeout(600);

			await page.screenshot({
				path: `../docs/screenshots/${name}.png`,
				fullPage: true,
				animations: 'disabled'
			});
		});
	}
});
