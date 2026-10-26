// noVNC's package exports its RFB entry only and does not ship TypeScript
// declarations. The Apple adapter is pinned to its 1.7 internal interfaces.
declare module '@novnc/novnc';
declare module '@novnc-internals/*';
