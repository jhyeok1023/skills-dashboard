<script lang="ts">
	/**
	 * A value that can be copied in one click.
	 *
	 * Pod names, ARNs, request paths and client addresses are things an
	 * operator immediately wants to paste somewhere else, so the value itself
	 * is the button. It never truncates: `data-value` opts it into the
	 * no-clipping rules in app.css, and the Playwright suite checks that every
	 * such element fits its own box.
	 */

	interface Props {
		/** What is shown. */
		value: string;
		/** What is copied, when that differs from what is shown. */
		copy?: string;
		mono?: boolean;
		/** Renders as a plain span with a small button beside it. */
		inline?: boolean;
		label?: string;
	}

	let { value, copy, mono = false, inline = false, label }: Props = $props();

	let copied = $state(false);
	let failed = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	const text = $derived(copy ?? value);
	const title = $derived(label ? `${label} 복사` : '클립보드로 복사');

	async function writeClipboard(s: string): Promise<boolean> {
		try {
			if (navigator.clipboard?.writeText) {
				await navigator.clipboard.writeText(s);
				return true;
			}
		} catch {
			// The async clipboard API needs a secure context. The dashboard is
			// served over plain http on localhost, which browsers usually treat
			// as secure — but not always, and not in every embedded webview, so
			// there is a fallback below rather than a silently dead button.
		}
		try {
			const ta = document.createElement('textarea');
			ta.value = s;
			ta.setAttribute('readonly', '');
			ta.style.position = 'fixed';
			ta.style.opacity = '0';
			document.body.appendChild(ta);
			ta.select();
			const ok = document.execCommand('copy');
			document.body.removeChild(ta);
			return ok;
		} catch {
			return false;
		}
	}

	async function handleCopy() {
		if (!text) return;
		const ok = await writeClipboard(text);
		copied = ok;
		failed = !ok;
		clearTimeout(timer);
		timer = setTimeout(() => {
			copied = false;
			failed = false;
		}, 1400);
	}
</script>

{#if inline}
	<span class="inline-wrap">
		<span data-value class:mono>{value}</span>
		<button
			type="button"
			class="icon"
			onclick={handleCopy}
			aria-label={title}
			{title}
			data-copy-button
		>
			{#if copied}✓{:else if failed}!{:else}⧉{/if}
		</button>
	</span>
{:else}
	<button
		type="button"
		class="value-button"
		class:mono
		onclick={handleCopy}
		{title}
		data-copy-button
	>
		<span data-value>{value}</span>
		<span class="hint" aria-hidden="true">
			{#if copied}✓{:else if failed}!{:else}⧉{/if}
		</span>
		<span class="sr">{title}</span>
	</button>
{/if}

<style>
	.inline-wrap {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		min-width: 0;
	}

	.value-button {
		display: inline-flex;
		align-items: baseline;
		gap: 5px;
		border: none;
		background: transparent;
		padding: 1px 3px;
		margin: -1px -3px;
		border-radius: 5px;
		text-align: left;
		/* The value wraps rather than being clipped, so the button grows with
		   its content instead of hiding it. */
		white-space: normal;
		overflow-wrap: anywhere;
		min-width: 0;
		max-width: 100%;
	}

	.value-button:hover,
	.inline-wrap:hover .icon {
		background: var(--fill-secondary);
	}

	.icon {
		border: none;
		background: transparent;
		border-radius: 4px;
		padding: 0 3px;
		font-size: 11px;
		line-height: 1.4;
		color: var(--label-tertiary);
	}

	.hint {
		font-size: 11px;
		color: var(--label-quaternary);
		flex: none;
	}

	.value-button:hover .hint,
	.value-button:focus-visible .hint {
		color: var(--accent);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip-path: inset(50%);
		white-space: nowrap;
	}
</style>
