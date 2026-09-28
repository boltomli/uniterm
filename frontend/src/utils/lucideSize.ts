// lucide types its `size` prop as `24 | number`, but the Icon runtime forwards
// the value unchanged to the SVG width/height attributes, which accept CSS
// lengths (`<svg width="0.875rem">`). Icon sizing here is rem-based so it
// tracks the root font-size baseline from style.css (16px, 18.6667px on macOS)
// — px numbers would drift on macOS. lucideSize is the single bridge between
// the two: the string reaches the attribute unchanged, the number satisfies
// the package's prop type.
export type CssLength = `${number}${'px' | 'rem' | 'em' | '%' | 'ch'}`

export function lucideSize(size: CssLength): number {
  return size as unknown as number
}
