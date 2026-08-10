<script lang="ts">
	/**
	 * The first load, before there is anything to show.
	 *
	 * Only ever rendered when the page has no payload at all. A refresh keeps
	 * the numbers that are already on screen — blanking readable data to report
	 * that newer data is coming is worse than saying nothing — so this stands in
	 * for an empty screen, never for a stale one.
	 *
	 * It earns its place on the WAF page in particular: that page runs seven
	 * Logs Insights queries and the server builds its panels one after another,
	 * so the wait is long enough that a single line of text reads as a hang.
	 *
	 * The shape is deliberately generic. Which panels a page has is the
	 * backend's decision and /api/meta does not describe it, so drawing an
	 * accurate outline would mean keeping a copy of the panel list here that
	 * silently goes stale. The block sizes match PanelCard's real ones — the
	 * 220px chart is `chartHeight`'s default — so the swap to real data moves
	 * things as little as possible.
	 */

	const CARDS = 4;
	const STATS = 3;
</script>

<!--
	One animation, on the outer element.

	Pulsing each block would put ~40 elements on their own compositor layers,
	all animating in step, to say one thing. Animating the container says it
	once. No shimmer: a travelling gradient repaints every frame, where opacity
	composites.
-->
<div class="skeleton grid" data-skeleton role="status" aria-busy="true">
	<span class="sr">데이터를 불러오는 중입니다</span>

	{#each { length: CARDS }, i (i)}
		<div class="card sk-card" aria-hidden="true">
			<div class="bar title"></div>

			<div class="stats">
				{#each { length: STATS }, j (j)}
					<div class="tile">
						<div class="bar label"></div>
						<div class="bar value"></div>
					</div>
				{/each}
			</div>

			<div class="chart"></div>

			<div class="legend">
				<div class="bar chip"></div>
				<div class="bar chip"></div>
			</div>
		</div>
	{/each}
</div>

<style>
	.skeleton {
		animation: sk-pulse 1400ms ease-in-out infinite;
	}

	@keyframes sk-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.55;
		}
	}

	.sk-card {
		display: flex;
		flex-direction: column;
		gap: 7px;
		min-width: 0;
	}

	.bar {
		background: var(--fill-primary);
		border-radius: 4px;
	}

	.title {
		width: 7rem;
		height: 13px;
	}

	/* The same grid rule PanelCard gives its stat tiles, so the columns do not
	   jump when the real tiles arrive. */
	.stats {
		display: grid;
		gap: 6px;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr));
	}

	.tile {
		display: flex;
		flex-direction: column;
		gap: 5px;
		padding: 6px 8px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
	}

	.label {
		width: 55%;
		height: 9px;
	}

	.value {
		width: 75%;
		height: 17px;
	}

	/* Matches UPlotChart's default height, so the panel does not resize under
	   the reader when the canvas mounts. */
	.chart {
		height: 220px;
		border-radius: var(--radius-control);
		background: var(--fill-secondary);
	}

	.legend {
		display: flex;
		gap: 8px;
	}

	.chip {
		width: 5rem;
		height: 9px;
	}

	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
		white-space: nowrap;
	}

	/* An infinite pulse cannot just be shortened — it would flicker. It stops,
	   and the placeholder still reads as "not here yet". */
	@media (prefers-reduced-motion: reduce) {
		.skeleton {
			animation: none !important;
			opacity: 0.75;
		}
	}
</style>
