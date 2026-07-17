# Graphite Granularity Control Design

## Objective

Replace the browser-native-looking `Day / Month` buttons in the top toolbar
with a compact macOS-style graphite segmented control. The control must match
the existing gray application palette and remain visually distinct from the
adjacent date-range choices.

## Scope

- Restyle only the top-toolbar `#granularity-switch` and its two buttons.
- Preserve the existing `day` and `month` values, URL synchronization, data
  loading, and rule that the control is visible only when `All Time` is active.
- Do not change the `Today`, `7 Days`, `30 Days`, `All Time`, or custom date
  range behavior.
- Do not introduce blue selection fills, gradients, icons, or new dependencies.

## Visual Design

The granularity control is a separate segmented group, not an extension of the
date-range buttons:

- Separate it from `All Time` with 8px of horizontal space and a subtle
  hairline divider.
- Use an inline-flex graphite container with a one-pixel translucent border,
  8px corner radius, 2px inner padding, and a restrained small shadow.
- Keep both segments equal in height and visual weight, aligned with the
  surrounding range buttons.
- Render inactive segments with a transparent background and muted gray text.
- Render the active segment with `var(--surface3)`, `var(--text)`, a 6px corner
  radius, and a small neutral shadow. The active state must not use
  `var(--accent)` or a pure-white fill.
- On hover, brighten only the text and neutral background. On press, slightly
  reduce brightness or opacity without moving the layout.
- Use a 140-150ms transition for background, color, and shadow.
- Use a neutral gray `focus-visible` outline or ring that remains visible in
  both dark and light themes.

The styling must be scoped to `#granularity-switch` so the Token Rate panel's
`Weighted / Avg` segmented control remains unchanged.

## Markup and Accessibility

- Add `type="button"` to both buttons.
- Add `role="group"` and `aria-label="Chart granularity"` to the wrapper.
- Keep the visible labels `Day` and `Month`.
- Maintain `aria-pressed="true"` on the active segment and `false` on the
  inactive segment whenever state is initialized, restored from the URL, or
  changed by a click.

## Responsive Behavior

The control remains on the same toolbar row while space is available and
follows the toolbar's existing wrapping behavior on narrow screens. The
divider and spacing remain attached to the control, so wrapping cannot leave a
detached divider after `All Time`.

## Testing and Verification

- Add a static frontend regression test that confirms the semantic wrapper,
  button types, and initial `aria-pressed` values.
- Test the state synchronization helper so active classes and `aria-pressed`
  values stay consistent for both `day` and `month`.
- Run all existing frontend and Go tests.
- Rebuild and restart the development instance on ports 14317 and 18899.
- Verify dark-theme and light-theme appearance, hover, keyboard focus, Day and
  Month selection, URL state, and `All Time` visibility in the browser.
- Capture a screenshot of the rebuilt development UI before deploying the same
  binary to the global instance.

## Non-goals

- No chart, API, database, or aggregation changes.
- No redesign of the entire date-range selector.
- No sliding-thumb animation that requires measuring segment geometry.
- No change to the Token Rate panel controls.
