// Node 엔진의 진입점. cmd/skills-dashboard/main.go 의 이식이다.
//
// 대회장에는 node 만 있고 Go 툴체인이 없다. main 브랜치는 미리 빌드한 exe 를
// 런처가 띄우는 방식으로 그 문제를 풀었고, 이 파일은 두 번째 답이다 —
// 실행 파일이 아예 없으므로 서명 없는 exe 가 차단당할 일도 없다.

import { spawn } from 'node:child_process';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';

import { getRequestListener } from '@hono/node-server';

import { Cache } from './aws/cache.ts';
import { globalWAFRegion, newClients, whoAmI } from './aws/client.ts';
import { InsightsRunner } from './aws/insights.ts';
import { MetricFetcher } from './aws/metrics.ts';
import { ConfigStore, configPath } from './config/store.ts';
import { loadCredentials, redacted, resolveEnvFile, validateCredentials } from './config/env.ts';
import { parseFlags } from './flags.ts';
import { createApp } from './http/routes.ts';
import { logger, setVerbose } from './log.ts';
import { newService } from './service.ts';

/** --port 를 명시하지 않았을 때 몇 개까지 더 시도하는지. */
const portFallbacks = 5;

async function main(): Promise<void> {
	const flags = parseFlags(process.argv.slice(2));
	setVerbose(flags.verbose);

	const cfgPath = configPath();
	let store: ConfigStore;
	try {
		store = ConfigStore.load(cfgPath);
	} catch (err) {
		throw new Error(`read the config at ${cfgPath}: ${message(err)}`);
	}
	const cfg = store.get();
	const cache = new Cache({
		ttlMs: cfg.limits.cacheTtlSeconds * 1000,
		errorTtlMs: 5_000
	});
	const service = newService(store, cache);

	// 저장된 설정이 포기해야 했던 것은 두 번 말한다. 터미널을 보고 있는
	// 사람에게 여기서 한 번, 설정 화면을 위해 /api/meta 로 한 번 — 값을 다시
	// 골라야 하는 곳이 거기이기 때문이다.
	for (const note of service.configNotices) {
		logger.warn('config was repaired on load', { detail: note, file: cfgPath });
	}

	loadCredentialsInto(service, flags.env);
	await connectAWS(service);

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

/**
 * loadCredentialsInto 는 자격증명을 한 번 읽는다.
 *
 * 실패를 치명적으로 다루지 않고 들고 간다. UI 는 그대로 뜨고 설정 화면이 무엇을
 * 고쳐야 하는지 설명하는 편이, 운영자가 이유를 보기도 전에 종료하는 프로세스보다
 * 낫다.
 *
 * 명령줄로 준 경로는 반드시 있어야 한다. 그 외의 경우는 운영자가 거기 없는
 * 파일을 달라고 한 것이고, 자격증명 없이 계속 가면 엉뚱한 주제에 대한 메시지로
 * 답하게 된다.
 */
function loadCredentialsInto(service: ReturnType<typeof newService>, named: string): void {
	let resolved;
	try {
		resolved = resolveEnvFile(named);
	} catch (err) {
		throw new Error(`read the .env at ${named}: ${message(err)}`);
	}

	if (resolved.path !== '') {
		logger.info('reading credentials', { envFile: resolved.path });
	} else {
		logger.warn('no .env found; falling back to the process environment', {
			tried: resolved.tried
		});
	}
	service.envFile = resolved.path;

	try {
		const creds = loadCredentials(resolved.path);
		try {
			validateCredentials(creds);
		} catch (err) {
			// 파일이 아무 데도 없으면 빠진 키는 이야기의 절반일 뿐이다. 나머지
			// 절반은 어디에 있어야 했는가다.
			const detail =
				resolved.path === ''
					? `${message(err)} (.env 를 다음에서 찾지 못했습니다: ${resolved.tried.join(', ')})`
					: message(err);
			throw new Error(detail);
		}
		service.pendingCredentials = creds;
	} catch (err) {
		service.credentialError = err instanceof Error ? err : new Error(String(err));
	}

	if (service.credentialError !== null) {
		logger.warn('AWS is unavailable; the UI will explain what to configure', {
			error: service.credentialError,
			envFile: service.envFile
		});
	}
}

/**
 * connectAWS 는 클라이언트를 만들고 자격증명이 실제로 통하는지 확인한다.
 *
 * 여기서도 실패는 들고 간다. 잘못된 키 하나로 프로세스가 죽으면 그것을 고칠
 * 설정 화면에 닿을 수 없다.
 */
async function connectAWS(service: ReturnType<typeof newService>): Promise<void> {
	const creds = service.pendingCredentials;
	if (creds === undefined) return;
	service.pendingCredentials = undefined;

	const store = service.store;
	let cfg = store.get();

	try {
		const clients = newClients(creds, cfg.wafRegion === '' ? globalWAFRegion : cfg.wafRegion);
		service.clients = clients;
		service.metrics = new MetricFetcher(clients.cw);
		service.insights = new InsightsRunner(clients.logs, {
			concurrency: cfg.limits.insightsConcurrency,
			timeoutMs: cfg.limits.queryTimeoutSeconds * 1000
		});
		// WAF 로그는 제 러너가 필요하다. CLOUDFRONT 범위 web ACL 은 us-east-1
		// 에만 로그를 내보내므로, 작업 리전 클라이언트로 그 로그 그룹을 조회하면
		// 없는 그룹이라며 실패한다. 두 리전이 같으면 러너를 공유해 동시 실행
		// 상한이 같은 크기의 풀 둘이 아니라 하나로 남는다.
		service.insightsGlobal =
			clients.wafRegion === clients.region
				? service.insights
				: new InsightsRunner(clients.logsGlobal, {
						concurrency: cfg.limits.insightsConcurrency,
						timeoutMs: cfg.limits.queryTimeoutSeconds * 1000
					});

		// 리전을 정하는 것은 자격증명이고 config.json 은 그것을 기록할 뿐이다.
		// 파일에 낡은 리전을 남겨 두는 것이, 대시보드가 한 리전을 따지면서 다른
		// 리전을 부르게 만든 원인이었다.
		if (cfg.region !== clients.region) {
			logger.info("recording the credentials' region in the config", {
				was: cfg.region,
				now: clients.region
			});
			cfg = { ...cfg, region: clients.region };
			try {
				store.set(cfg);
			} catch (err) {
				logger.warn('could not save the region to the config', { error: message(err) });
			}
		}

		const identity = await whoAmI(clients.sts, clients.region, AbortSignal.timeout(15_000));
		identity.wafRegion = clients.wafRegion;
		service.identity = identity;
		logger.info('credentials accepted', {
			account: identity.account,
			region: identity.region,
			wafRegion: identity.wafRegion,
			key: redacted(creds)
		});
	} catch (err) {
		service.credentialError = new Error(`자격증명 확인 실패: ${message(err)}`);
		logger.warn('AWS is unavailable; the UI will explain what to configure', {
			error: service.credentialError,
			envFile: service.envFile
		});
	}
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
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
