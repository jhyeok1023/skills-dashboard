// Node 엔진의 진입점. cmd/skills-dashboard/main.go 의 이식이다.
//
// 대회장에는 node 만 있고 Go 툴체인이 없다. main 브랜치는 미리 빌드한 exe 를
// 런처가 띄우는 방식으로 그 문제를 풀었고, 이 파일은 두 번째 답이다 —
// 실행 파일이 아예 없으므로 서명 없는 exe 가 차단당할 일도 없다.

import { spawn } from 'node:child_process';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';

import { getRequestListener } from '@hono/node-server';

import { parseFlags } from './flags';
import { createApp } from './http/routes';
import { logger, setVerbose } from './log';
import { newService } from './service';

/** --port 를 명시하지 않았을 때 몇 개까지 더 시도하는지. */
const portFallbacks = 5;

async function main(): Promise<void> {
	const flags = parseFlags(process.argv.slice(2));
	setVerbose(flags.verbose);

	const service = newService();

	// TODO(config): .env 해석과 자격증명 적재는 다음 단계에서 붙인다.
	// 지금은 런처가 넘기는 --env 를 받아 두기만 한다.
	if (flags.env !== '') service.envFile = flags.env;

	const app = createApp(service);

	const server = createServer(getRequestListener(app.fetch));

	// 타임아웃은 전부 의도적으로 건다. 이전 구현은 http.ListenAndServe 에
	// 아무것도 걸지 않아서, 느린 AWS 호출을 붙든 핸들러가 얼마나 오래 살지
	// 아무것도 정하지 않았고 막힌 요청이 그대로 쌓였다.
	//
	// Go 의 WriteTimeout 에 해당하는 것은 node 에 없다. 응답 쪽 상한은
	// 페이지 예산 90초(pageBudget)가 대신 맡는다.
	server.headersTimeout = 10_000; // ReadHeaderTimeout
	server.requestTimeout = 30_000; // ReadTimeout
	server.keepAliveTimeout = 120_000; // IdleTimeout

	const addr = await listen(server, flags.addr, flags.port, flags.portWasChosen);

	const url = `http://${formatHost(addr)}:${addr.port}`;
	logger.info('dashboard is listening', { url });
	if (flags.open) openBrowser(url);

	installShutdown(server);
}

function formatHost(addr: AddressInfo): string {
	// IPv6 주소는 대괄호로 감싸야 URL 이 된다.
	return addr.family === 'IPv6' ? `[${addr.address}]` : addr.address;
}

/**
 * listen 은 대시보드의 소켓을 연다.
 *
 * 포트가 이미 쓰이면 프로세스를 끝내는 게 원래 동작이었는데, Windows 에서
 * 더블클릭으로 띄운 콘솔 창은 그 이유를 안은 채 사라진다. 그래서 기본 포트는
 * 출발점일 뿐이고, 다음 몇 개를 시도한 뒤 실제로 열린 포트를 로그에 남긴다.
 * 운영자가 직접 지정한 포트는 존중한다 — 8080 이라고 말했으면 8080 을 원한
 * 것이다 — 그리고 실패 메시지가 무엇을 하면 되는지 말한다.
 */
function listen(
	server: Server,
	addr: string,
	port: number,
	chosen: boolean
): Promise<AddressInfo> {
	const last = chosen ? port : port + portFallbacks;

	const attempt = (candidate: number): Promise<AddressInfo> =>
		new Promise((resolve, reject) => {
			const onError = (err: NodeJS.ErrnoException) => {
				server.removeListener('listening', onListening);
				reject(err);
			};
			const onListening = () => {
				server.removeListener('error', onError);
				resolve(server.address() as AddressInfo);
			};
			server.once('error', onError);
			server.once('listening', onListening);
			server.listen({ host: addr, port: candidate });
		});

	const next = async (candidate: number): Promise<AddressInfo> => {
		try {
			const bound = await attempt(candidate);
			if (candidate !== port) {
				logger.warn('the requested port was busy; using the next free one', {
					requested: port,
					using: candidate
				});
			}
			return bound;
		} catch (err) {
			if (candidate < last) return next(candidate + 1);
			const why = err instanceof Error ? err.message : String(err);
			throw new Error(
				chosen
					? `${candidate} 포트를 열 수 없습니다 (${why}). 이미 사용 중이라면 --port 로 다른 포트를 지정하세요`
					: `${port}..${last} 포트가 모두 사용 중입니다 (${why}). --port 로 다른 포트를 지정하세요`
			);
		}
	};

	return next(port);
}

function installShutdown(server: Server): void {
	let stopping = false;

	const stop = () => {
		if (stopping) return;
		stopping = true;
		logger.info('shutting down');

		// close() 만으로는 keep-alive 로 놀고 있는 연결을 기다린다.
		// keepAliveTimeout 이 120초라 그만큼 매달릴 수 있다.
		server.closeIdleConnections();
		server.close(() => process.exit(0));

		// 10초 안에 끝나지 않으면 남은 연결을 끊고 나간다. Go 의
		// Shutdown 컨텍스트와 같은 상한이다.
		setTimeout(() => {
			server.closeAllConnections();
			process.exit(0);
		}, 10_000).unref();
	};

	for (const signal of ['SIGINT', 'SIGTERM'] as const) process.on(signal, stop);
}

/**
 * openBrowser 는 되면 좋은 것이다. 브라우저를 못 열었다고 서비스를 거부할
 * 이유는 없다.
 */
function openBrowser(url: string): void {
	const [command, args] =
		process.platform === 'win32'
			? ['rundll32', ['url.dll,FileProtocolHandler', url]]
			: process.platform === 'darwin'
				? ['open', [url]]
				: ['xdg-open', [url]];

	try {
		const child = spawn(command as string, args as string[], {
			stdio: 'ignore',
			detached: true
		});
		child.on('error', (err) => logger.debug('could not open a browser', { error: err }));
		child.unref();
	} catch (err) {
		logger.debug('could not open a browser', { error: err instanceof Error ? err : String(err) });
	}
}

// 핸들러 밖에서 난 거절까지 프로세스를 끝내지는 않는다. 대시보드가 죽는 것보다
// 패널 하나가 비는 편이 낫다.
process.on('unhandledRejection', (reason) => {
	logger.error('unhandled rejection', {
		error: reason instanceof Error ? reason : String(reason)
	});
});

main().catch((err: unknown) => {
	const detail = err instanceof Error ? err.message : String(err);
	process.stderr.write(`\n대시보드를 시작하지 못했습니다.\n\n  ${detail}\n\n`);
	process.exit(1);
});
