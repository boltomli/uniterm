<template>
  <!-- Windows/Linux title-bar restore glyph: two overlapping squares. The
       rear square is drawn as an open path (skipping the edge hidden behind
       the front square), so no mask/id bookkeeping is needed. Sized by the
       caller's CSS. -->
  <svg viewBox="0 0 12 12" width="1em" height="1em" fill="none" aria-hidden="true">
    <path d="M3.5 3.5V1h6.5v6.5H7.5" stroke="currentColor" stroke-width="1"/>
    <rect x="1" y="3.5" width="6.5" height="6.5" fill="none" stroke="currentColor" stroke-width="1"/>
  </svg>
</template>
