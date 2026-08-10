// The dashboard is a single-page app served out of a Go binary. There is no
// Node runtime in production and every byte of data comes from /api at runtime,
// so server-side rendering has nothing to render and is switched off.
export const ssr = false;
export const prerender = false;
export const trailingSlash = 'never';
