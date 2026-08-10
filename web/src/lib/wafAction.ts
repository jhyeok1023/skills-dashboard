/**
 * WAF actions: one fixed icon, label and colour per action, for the whole UI.
 *
 * Colour alone never distinguishes an action — every place an action appears
 * (chart legend, hover tooltip, stat tile, alarm bar) draws the icon beside it,
 * so the distinction survives a colourblind viewer and a greyscale print.
 *
 * The colours match what the backend already sends. `wafActionColor` in
 * internal/api/panels_logs.go tags each series with a system colour name, which
 * `colorVar()` resolves to the same custom property these tokens alias. The
 * mapping therefore has one source: change it there and the line, the swatch
 * and the badge all move together.
 */

export interface WafActionStyle {
	/** The canonical action name, upper-cased. */
	key: string;
	/** A monochrome glyph. Shape carries the meaning, not the colour. */
	icon: string;
	/** Korean label for the action. */
	label: string;
	color: string;
}

const ACTIONS: Record<string, WafActionStyle> = {
	ALLOW: { key: 'ALLOW', icon: '✓', label: '허용', color: 'var(--waf-allow)' },
	BLOCK: { key: 'BLOCK', icon: '✕', label: '차단', color: 'var(--waf-block)' },
	COUNT: { key: 'COUNT', icon: '◎', label: '카운트', color: 'var(--waf-count)' },
	CHALLENGE: { key: 'CHALLENGE', icon: '△', label: '챌린지', color: 'var(--waf-challenge)' },
	CAPTCHA: { key: 'CAPTCHA', icon: '◇', label: '캡차', color: 'var(--waf-captcha)' },
	EXCLUDED_AS_COUNT: {
		key: 'EXCLUDED_AS_COUNT',
		icon: '◎',
		label: '카운트(제외)',
		color: 'var(--waf-count)'
	}
};

/**
 * Looks up an action by the label a series or stat carries.
 *
 * Returns null for anything that is not a WAF action, so latency and pod
 * series are not decorated with an icon that would mean nothing there.
 */
export function wafAction(label: string | undefined): WafActionStyle | null {
	if (!label) return null;
	return ACTIONS[label.trim().toUpperCase()] ?? null;
}

/** Stat keys arrive as `waf.log.block`; this reads the action back off one. */
export function wafActionFromStatKey(key: string | undefined): WafActionStyle | null {
	if (!key) return null;
	const match = /^waf\.log\.(.+)$/.exec(key);
	return match ? wafAction(match[1]) : null;
}
