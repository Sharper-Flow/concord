# Concord brand pack

This pack supports [Sharper-Flow/concord](https://github.com/Sharper-Flow/concord), Product-first agent coordination for professionals.

## Logo hierarchy

1. **Primary stacked** — documentation covers, landing pages, launch materials.
2. **Horizontal** — navigation, CLI documentation headers, sponsor rows.
3. **Mark only** — repository avatars, app chrome, badges.
4. **Responsive icon** — solid silhouette at 32–64 px; monoline mark at 128 px and above.

## Theme selection

| Background | Asset |
|---|---|
| White or light neutral | `*-light.svg` |
| Near-black or dark purple | `*-dark.svg` |
| Single-color production | `concord-primary-mono-dark.svg` or `concord-primary-mono-light.svg` |

Dark-mode assets are transparent and use a pale lavender reversal. README banners contain their own background.

## GitHub README usage

```html
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/github/concord-readme-banner-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="brand/github/concord-readme-banner-light.svg">
  <img alt="Concord — Product-first agent coordination for professionals."
       src="brand/github/concord-readme-banner-light.svg">
</picture>
```

## Clear space

Maintain clear space equal to the cap height of the wordmark’s uppercase **C** on all sides. Do not place the logo against high-detail imagery.

## Minimum sizes

- Full stacked lockup: 480 px wide
- Horizontal lockup: 280 px wide
- Mark only: 72 px wide
- Solid responsive icon: 32–64 px
- Never use the outlined mark as a 16 px favicon

## Typography

- Wordmark and display: **Jost SemiBold 600**
- Supporting headings: **Jost Medium 500**
- UI/body copy: use the project’s existing system sans or Jost Regular if adopted

The SVG wordmarks remain editable text and load Jost from Google Fonts. For offline print production, install Jost and convert the final wordmark to outlines.

## Color

The primary identity is dominated by near-black aubergine and deep purple. Indigo is a supporting undertone, not the main color. Use the CSS or JSON tokens in `tokens/`.

## Do not

- Re-round the shallow engine cut
- Use bright violet, blue, or magenta gradients
- Stretch, shear, or rotate the mark
- Add shadows, glows, aircraft details, or texture
- Reduce the wordmark tracking
- Place the dark logo on a dark background or the reversed logo on white
