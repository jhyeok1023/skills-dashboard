/**
 * setInterval that goes quiet while the tab is hidden.
 *
 * A hidden dashboard has no reader, but its polls still cost real money
 * (Logs Insights bills per byte scanned) or hit a production service (the
 * traffic check). Ticks that land while hidden are skipped; if any were,
 * the callback runs once as soon as the tab is visible again, so the screen
 * catches up immediately instead of waiting out the rest of an interval.
 *
 * Returns a cleanup function, so it drops straight into an `$effect`.
 */
export function visibleInterval(fn: () => void, ms: number): () => void {
	let missed = false;
	const id = setInterval(() => {
		if (document.hidden) {
			missed = true;
			return;
		}
		fn();
	}, ms);
	const onVisible = () => {
		if (!document.hidden && missed) {
			missed = false;
			fn();
		}
	};
	document.addEventListener('visibilitychange', onVisible);
	return () => {
		clearInterval(id);
		document.removeEventListener('visibilitychange', onVisible);
	};
}
