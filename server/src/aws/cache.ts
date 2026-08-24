// 비싼 AWS 호출을 짧은 창 동안 기억하고, 동시에 들어온 미스를 호출 하나로 접는다.
// internal/awsx/cache.go 의 이식이다.
//
// 여기 두 가지는 이전 구현의 캐시가 부하에서 어떻게 굴었는지에 대한 직접적인
// 답이다. 그 캐시는 로더를 부르기 전에 락을 놓아서, 동시에 들어온 모든 미스가
// 제 나름의 AWS 호출을 냈다 — 대시보드가 가장 바쁠 때 정확히 도착하는 스탬피드다.
// 그리고 실패를 저장하지 않아서, 스로틀링당하거나 실패하는 의존성을 매 틱마다
// 전속력으로 다시 두드렸다. 일시적 오류가 지속적 오류가 된 경위가 그것이다.
//
// node 에서는 락이 필요 없다. 한 줄로 돌기 때문이다. 대신 "키를 먼저 선점한다" 는
// 성질은 그대로 남는다 — 진입한 호출자가 곧바로 항목을 꽂고, 뒤따르는 호출자는
// 그 약속을 기다린다.

type Settled = { ok: true; value: unknown } | { ok: false; error: unknown };

interface Entry {
	/** 절대 거절하지 않는다. 기다리는 쪽이 거절을 삼키다 놓치는 일이 없게. */
	promise: Promise<Settled>;
	expires: number;
	/** 아직 적재 중인 항목은 쓸어내지 않는다. 기다리는 호출자가 있기 때문이다. */
	settled: boolean;
}

export interface CacheOptions {
	/** 성공한 값을 메모리에서 내주는 기간(ms). */
	ttlMs?: number;
	/**
	 * 실패를 기억하는 기간(ms). 짧지만 0 은 아니다 — 재시도 폭풍을 멈출 만큼은
	 * 길고, 회복한 의존성을 금방 알아챌 만큼은 짧다.
	 */
	errorTtlMs?: number;
	/** 테스트가 시계를 고정할 수 있게 열어 둔다. */
	now?: () => number;
}

export class Cache {
	private entries = new Map<string, Entry>();
	private readonly ttlMs: number;
	private readonly errorTtlMs: number;
	private readonly now: () => number;

	constructor(options: CacheOptions = {}) {
		this.ttlMs = options.ttlMs !== undefined && options.ttlMs > 0 ? options.ttlMs : 30_000;
		this.errorTtlMs =
			options.errorTtlMs !== undefined && options.errorTtlMs > 0 ? options.errorTtlMs : 5_000;
		this.now = options.now ?? (() => Date.now());
	}

	/**
	 * do 는 key 의 캐시된 값을 준다. 미스로 들어온 동시 호출자를 통틀어 load 는
	 * 많아야 한 번 불린다.
	 */
	async do<T>(key: string, signal: AbortSignal | undefined, load: () => Promise<T>): Promise<T> {
		for (;;) {
			this.prune();

			const waiting = this.entries.get(key);
			if (waiting !== undefined) {
				const settled = await waiting.promise;
				// 기다리는 사이에 항목이 만료됐을 수 있다. 그러면 낡은 것을
				// 내주는 대신 처음부터 다시 한다.
				const fresh = this.entries.get(key) === waiting && this.now() < waiting.expires;
				if (fresh) return unwrap<T>(settled);
				if (this.entries.get(key) === waiting) this.entries.delete(key);
				continue;
			}

			// 락을 놓기 전에 키를 선점한다. 그것이 이 캐시의 요점이다 — 다른
			// 모든 호출자가 제 호출을 내는 대신 이 항목을 찾아 기다린다.
			const entry: Entry = {
				promise: settle(load),
				expires: this.now() + this.ttlMs,
				settled: false
			};
			this.entries.set(key, entry);

			const settled = await entry.promise;
			entry.settled = true;

			if (settled.ok) {
				entry.expires = this.now() + this.ttlMs;
				return settled.value as T;
			}

			// *이* 호출자가 사라져서 생긴 실패는 데이터에 대한 사실이 아니라
			// 호출자에 대한 사실이다. 다른 누구에게도 건네서는 안 된다.
			//
			// 대시보드가 간헐적으로 실패한 원인이 이것이었다. 브라우저는 선택이
			// 바뀔 때마다 진행 중인 페이지 요청을 중단하고, /api/meta 가 범위
			// 표를 갈아 끼우며 적재 이펙트를 다시 당기므로 마운트마다 한 번
			// 더 그렇게 한다. 중단된 요청이 대개 키를 쥐고 있던 쪽이라, 그
			// "context canceled" 가 캐시된 실패로 저장돼 그것을 대체한 요청에
			// 그대로 나갔다. 창은 주기로 내림되므로 1분 주기에서는 키가 꼬박
			// 1분 동안 같고, 재시도는 같은 오염된 항목에 떨어진다.
			//
			// 만료가 아니라 삭제다. 이미 e 를 기다리던 호출자는 키가 더 이상
			// 자기를 가리키지 않는 것을 보고 다시 적재하러 돈다.
			if (signal?.aborted === true) {
				if (this.entries.get(key) === entry) this.entries.delete(key);
			} else {
				entry.expires = this.now() + this.errorTtlMs;
			}
			throw settled.error;
		}
	}

	/**
	 * prune 은 만료된 항목을 버린다. 캐시는 요청 모양으로 키를 잡으므로 실제로는
	 * 유계지만, 한 번도 쓸지 않는 무한 맵은 새 키 형식을 기다리는 누수다.
	 */
	private prune(): void {
		const now = this.now();
		for (const [key, entry] of this.entries) {
			if (entry.settled && now > entry.expires) this.entries.delete(key);
		}
	}

	/**
	 * invalidate 는 전부 버린다. 설정 화면이 저장 뒤에 부르므로, 다음 읽기가
	 * 새 리소스 선택을 즉시 반영한다.
	 */
	invalidate(): void {
		this.entries.clear();
	}

	/** 들고 있는 항목 수. 테스트와 진단용이다. */
	get size(): number {
		return this.entries.size;
	}
}

async function settle<T>(load: () => Promise<T>): Promise<Settled> {
	try {
		return { ok: true, value: await load() };
	} catch (error) {
		return { ok: false, error };
	}
}

function unwrap<T>(settled: Settled): T {
	if (settled.ok) return settled.value as T;
	throw settled.error;
}
