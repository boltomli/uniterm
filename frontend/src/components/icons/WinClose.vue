<template>
  <!-- Win-style close glyph (12×12 stroke X). Also shared with the
       dialog/message-box close keys, which re-render the same path via
       --win-close-x in style.css (Element Plus internals cannot host
       components); the mac dialog dot uses --mac-close-x. Sized by the
       caller's CSS (1em fallback). -->
  <svg viewBox="0 0 12 12" width="1em" height="1em" fill="none" aria-hidden="true">
    <path d="M2 2l8 8M10 2L2 10" stroke="currentColor" stroke-width="1.2" />
  </svg>
</template>
