// cronstrue's package `types` only covers the root entry (dist/cronstrue.d.ts);
// the per-locale builds under `cronstrue/locales/*` ship plain JavaScript with
// no declaration. Each locale module is the same ExpressionDescriptor class
// with bundled strings, so it is typed as the root default export. Only the
// zh_CN locale is imported today; the wildcard keeps further locales from
// needing another entry.
declare module 'cronstrue/locales/*' {
  import cronstrue from 'cronstrue'
  export default cronstrue
}
