# CC-OTEL Frontend Taste

This file is the authoritative visual taste guide for the CC-OTEL frontend.
Read it completely before designing, implementing, changing, or reviewing any
frontend work.

## Product Character

CC-OTEL should feel like a focused macOS system utility: calm, compact,
trustworthy, and information-dense. The closest reference is Activity Monitor,
not a marketing dashboard, gaming interface, or generic admin template.

The interface should disappear behind the data. Prefer quiet hierarchy,
precise alignment, restrained depth, and small moments of polish over large
decorative gestures.

## Core Principles

1. Use graphite and neutral gray as the default visual language.
2. Make hierarchy through spacing, surface elevation, typography, and contrast
   before reaching for saturated color.
3. Keep controls compact and unmistakably intentional; browser-native default
   controls are not an acceptable finished state.
4. Preserve high information density without making the interface cramped.
5. Match dark and light themes as one system, not two unrelated designs.
6. Prefer one coherent pattern reused well over several slightly different
   patterns.
7. Every visual decision must support comprehension, state, or interaction.

## Color

The existing CSS custom properties in `internal/web/static/style.css` are the
source of truth. Reuse `--bg`, `--surface`, `--surface2`, `--surface3`,
`--border`, `--border-hover`, `--text`, `--text-muted`, and `--text-dim` before
introducing another color.

- Graphite gray is the default for containers, selected controls, buttons,
  progress, and neutral emphasis.
- Do not use a bright blue fill as the default selected or primary-button
  treatment. It is visually foreign to this project.
- Reserve `--accent` for semantic emphasis that genuinely benefits from color,
  such as links, selected data, chart meaning, or a necessary focus cue.
- Green, orange, and red communicate status or meaning; they are not
  decoration.
- Avoid pure white fills in dark mode and pure black fills in light mode.
- Avoid gradients unless the visualization itself encodes a meaningful
  proportion already specified by the product rules.
- Never add a new saturated color merely to make a control noticeable.

## Surfaces, Borders, and Depth

- Use nested graphite surfaces to establish hierarchy: background, panel,
  raised control, then selected segment.
- Prefer one-pixel translucent borders. Strong opaque borders make the UI feel
  like a form builder rather than a system utility.
- Use small, neutral shadows only to separate adjacent surfaces. Shadows should
  not look soft, floating, or theatrical.
- Rounded corners should be compact and proportional. Use the existing radius
  tokens; most controls belong around 6-10px and cards around 12px.
- Do not put every element in its own bordered card. Group related information
  and let spacing do part of the work.

## Typography and Numbers

- Use the existing Apple-system font stack for interface text.
- Use `--font-mono` for token counts, currency, durations, timestamps, IDs, and
  other values that benefit from stable digit widths.
- Prefer regular and semibold weights. Heavy bold text is for the strongest
  KPI or section hierarchy only.
- Keep labels concise. Use muted text for supporting labels and full text color
  for the current value or active state.
- Maintain clear alignment between labels and numbers. Tables should scan
  vertically without visual jitter.

## Layout and Density

- Preserve the compact system-toolbar character of the top navigation.
- Align controls to a shared height and baseline. A control that is one or two
  pixels off is visible in a dense toolbar.
- Use consistent spacing increments. Related items sit close; different
  concepts receive a gap or a subtle divider.
- Keep data tables and charts dense enough for real monitoring work. Do not
  inflate rows, cards, or whitespace to imitate a landing page.
- Responsive layouts may wrap, but semantic groups must stay intact. A divider
  must never become detached from the group it introduces.

## Controls

All controls should look native to CC-OTEL, not native to the browser.

### Segmented controls

- Use segmented controls for a small, mutually exclusive set of views or
  modes.
- The container uses a graphite surface, subtle border, compact radius, and
  restrained shadow.
- Segments are equal in visual weight. The active segment uses a slightly
  brighter graphite surface and clear text, not blue or pure white.
- Inactive segments remain transparent or quiet, with muted text.
- Separate a segmented control from an adjacent control when their semantics
  differ. Use spacing and a hairline divider rather than color.
- The approved `Day / Month` control is the reference pattern for a compact
  toolbar segment. The `Weighted / Avg` control is the reference inside a
  content panel.

### Buttons

- Neutral actions use graphite surfaces and subtle borders.
- Primary actions may use strong light-on-dark contrast, but should still fit
  the gray system palette.
- Destructive actions use red only when the action is genuinely destructive.
- Avoid oversized buttons, pill shapes without purpose, and glossy or raised
  effects.

### Inputs and menus

- Style inputs and closed selects to match surrounding surfaces.
- If an operating-system popup cannot match the interface and the mismatch is
  prominent, use an accessible custom popover as already done for Token Rate.
- Menus use compact rows, clear selected state, and one restrained shadow.

## Icons

- Prefer simple SVG icons using `currentColor`.
- Use consistent stroke weight, optical size, and alignment.
- Icons support recognition; they do not replace an essential label when the
  meaning would be ambiguous.
- Avoid emoji, colorful icon packs, and mixed outline/filled styles in one
  control group.

## Interaction and Motion

- Hover states should be a small change in neutral surface, border, or text
  contrast.
- Pressed states may reduce opacity or brightness slightly; do not shift layout.
- Keep transitions around 120-180ms for color, border, opacity, and small
  transforms.
- Avoid bouncing, elastic motion, large scaling, or decorative animation.
- Loading and progress states should feel stable and quiet.
- Keyboard focus must be visible in both themes. Prefer a neutral ring unless
  accent color is necessary for clarity.

## Accessibility

- Use semantic elements and add `type="button"` to non-submit buttons.
- Mutually exclusive buttons must expose their current state with the correct
  ARIA attribute, such as `aria-pressed`.
- Icon-only controls require an accessible name.
- Do not remove focus indicators without providing a visible replacement.
- Maintain readable contrast for muted labels, selected states, and disabled
  controls in both themes.
- A visual state must never be communicated by color alone.

## Dark and Light Themes

- Implement with project variables rather than theme-specific duplicated
  components.
- Dark mode is the primary visual reference, but light mode must be deliberately
  checked for washed-out borders, white active fills, and low-contrast text.
- A surface that becomes bright white in light mode should still retain its
  border and hierarchy.
- Do not solve a dark-theme issue with a hard-coded white overlay that looks
  wrong in light mode; prefer theme-aware variables.

## Data Visualization

Visual taste never overrides the pinned data and chart semantics in
`.claude/CLAUDE.md`.

- Color encodes model identity, state, or magnitude; it is not decoration.
- Tooltips, legends, bars, and tables must use the same definitions.
- Charts should prioritize comparison and exact reading over spectacle.
- Avoid unnecessary grid lines, heavy axes, merged tooltips, 3D effects, and
  ornamental animation.
- Keep the existing per-time-and-model granularity and all chart prohibitions
  documented in `.claude/CLAUDE.md`.

## Implementation Discipline

- Scope CSS to the component being changed. Shared class names such as
  `.gran-btn` require a parent selector so unrelated controls do not drift.
- Reuse existing tokens and patterns before adding new ones.
- Keep pure UI state helpers in focused ES modules and test them with the Node
  test runner.
- Keep DOM wiring inside the existing module structure; do not add a frontend
  framework or build tool.
- Update cache-busting query versions whenever embedded CSS or root modules
  change.
- Preserve URL state, keyboard behavior, ARIA state, and responsive behavior
  when changing visual styling.
- Do not combine a visual refinement with unrelated data, API, or database
  changes.

## Verification Standard

A frontend change is not finished when the code compiles.

1. Run focused frontend tests and the full static test suite.
2. Run the relevant Go tests because static assets are embedded in the binary.
3. Rebuild and restart the development instance on ports 14317 and 18899.
4. Inspect the real page with a cache-busting query string.
5. Check dark and light themes, hover, pressed, keyboard focus, active state,
   responsive wrapping, and URL restoration where relevant.
6. Capture at least one screenshot that includes the changed component and its
   surrounding context.
7. Deploy the same verified binary rather than rebuilding from different
   source state.

## Review Questions

Before approving a visual change, ask:

- Does it look like the same product as the surrounding interface?
- Is graphite doing the work that an unnecessary bright color was doing?
- Are related controls grouped and different concepts separated?
- Does the active state remain obvious without becoming loud?
- Does it still work in light mode, with a keyboard, and at narrow widths?
- Did the change preserve data meaning and URL/application state?
- Is every new border, shadow, color, and animation earning its place?

If the answer to any of these is no, the frontend work is not finished.
