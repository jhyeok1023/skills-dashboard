/**
 * The wire contract, mirrored from internal/domain/series.go.
 *
 * Note what the frontend is given and what it is not. It receives finished
 * numbers in `stats`, a table whose `total` was counted independently of the
 * `rows` it carries, and series already aligned to `window.timestamps`. It is
 * not given the raw material to recompute any of that — which is deliberate,
 * because recomputing display numbers in the view is how the same value came to
 * differ between an overview and its detail page.
 */

export type Unit = '' | 'ms' | 's' | '%' | 'count' | '/s' | 'bytes' | 'conn';
export type Intent = 'neutral' | 'good' | 'warn' | 'bad';

/** A null value is a genuine gap in the data, not a zero. */
export type Point = number | null;

export interface Series {
	label: string;
	unit: Unit;
	color?: string;
	/** '', 'dashed' or 'dotted'. Absent means a solid line. */
	dash?: string;
	values: Point[];
}

export interface Stat {
	key: string;
	label: string;
	value: Point;
	unit: Unit;
	text?: string;
	/** The population this number was computed over. */
	basis?: string;
	intent?: Intent;
}

export interface Column {
	key: string;
	label: string;
	unit?: Unit;
	copyable?: boolean;
	mono?: boolean;
	numeric?: boolean;
	/**
	 * Shown only in a row's expanded detail, never as a column.
	 *
	 * It also decides which tables offer an expander at all: a table that
	 * declares none has nothing to reveal, so an aggregate row — already its own
	 * summary — stays inert without the view knowing any panel by name.
	 */
	detail?: boolean;
}

export type Row = Record<string, unknown>;

export interface Table {
	columns: Column[];
	rows: Row[];
	/** Counted independently of `rows`; never derive this from rows.length. */
	total: number;
	truncated: boolean;
	limit: number;
}

/**
 * Names the columns of a table that carry a category and a count, so a bar
 * chart and the table under it render the same rows rather than being derived
 * independently.
 */
export interface Bars {
	keyColumn: string;
	valueColumn: string;
	groupColumn?: string;
}

export interface Panel {
	id: string;
	title: string;
	series?: Series[];
	stats?: Stat[];
	table?: Table;
	bars?: Bars;
	warnings?: string[];
}

export interface WindowJSON {
	start: number;
	end: number;
	period: number;
	range: string;
	/** The single time axis every series in this payload is aligned to. */
	timestamps: number[];
}

export interface Payload {
	window: WindowJSON;
	panels: Panel[];
	warnings?: string[];
}

export interface MetaRange {
	range: string;
	seconds: number;
	periods: string[];
	defaultPeriod: string;
}

export interface Meta {
	maxRangeSeconds: number;
	ranges: MetaRange[];
	defaultRange: string;
	limits: {
		logRows: number;
		topN: number;
		insightsConcurrency: number;
		queryTimeoutSeconds: number;
		cacheTtlSeconds: number;
	};
	/** What loading the stored config had to discard, if anything. */
	notices?: string[];
}

export interface Identity {
	account: string;
	arn: string;
	userId: string;
	region: string;
	/** Where CLOUDFRONT-scope WAF is read from. Absent until credentials resolve. */
	wafRegion?: string;
}

export interface LogFormat {
	preset: 'auto' | 'gin' | 'json';
	timeField: string;
	messageField: string;
	processedField: string;
	streamField: string;
	appField: string;
	latencyField: string;
	latencyUnit: Unit;
	statusField: string;
	methodField: string;
	pathField: string;
	levelField: string;
	clientIpField: string;
	/** Empty unless the operator names it; an access log has one only if the application wrote it. */
	userAgentField: string;
	textPattern: string;
	levelPattern: string;
	namespace: string;
	okStatuses: number[];
	/** Request paths dropped from every pod-log panel. Matched exactly. */
	excludePaths: string[];
}

export interface Config {
	region: string;
	wafRegion: string;
	clusterName: string;
	namespace: string;
	podLogGroup: string;
	wafLogGroup: string;
	loadBalancer: string;
	targetGroups: string[];
	rdsProxies: string[];
	webAcls: string[];
	wafHeaders: string[];
	logFormat: LogFormat;
	limits: Meta['limits'];
	check: HealthCheck;
}

/** The one endpoint the dashboard requests itself. See internal/api/check.go. */
export interface HealthCheck {
	url: string;
	/** 0 means any 2xx counts as healthy. */
	expectStatus: number;
}

/** One completed probe. A failed probe is still a completed probe. */
export interface CheckResult {
	url: string;
	ok: boolean;
	status?: number;
	elapsedMs: number;
	at: string;
	error?: string;
	/** What the status was compared against, in words. */
	expect: string;
}

export interface Resource {
	id: string;
	name: string;
	arn?: string;
	extra?: Record<string, string>;
}

export interface DiscoveryResponse {
	kind: string;
	resources: Resource[];
	/** The page cap stopped the walk, so the list may be missing entries. */
	truncated?: boolean;
	/** Scopes that failed without failing the whole call, in words. */
	partial?: string[];
}

export interface LogLine {
	timestamp: string;
	app: string;
	pod: string;
	namespace: string;
	container: string;
	stream: string;
	method: string;
	path: string;
	requestTarget: string;
	clientIp: string;
	status: number;
	latencyMs: number | null;
	level: string;
	message: string;
	hasAccess: boolean;
}

export interface LogFormatPreview {
	parsed: LogLine;
	matched: boolean;
	badStatus: boolean;
	/** The line's path is on the exclusion list, so no panel would show it. */
	excluded: boolean;
	suggestion?: string;
}

export interface ApiError {
	error: string;
	detail?: string;
	hint?: string;
}
