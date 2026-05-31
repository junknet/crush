// Dev-only logger. Metro defines the global __DEV__ as false in release
// bundles, so in production these collapse to a no-op and the per-interaction
// [TRACE]/[PERF]/[CrushApi] spam never reaches logcat (each console.log bridges
// to native, which is real cost on a hot path). console.error is intentionally
// left un-gated so genuine failures still surface in production.
export const dlog: (...args: unknown[]) => void =
    typeof __DEV__ !== 'undefined' && __DEV__ ? console.log.bind(console) : () => {}
