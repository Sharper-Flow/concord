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

The SVG wordmarks remain editable text. Every asset that draws the wordmark embeds a subset of the Jost variable font as a base64 `@font-face`, because a browser renders an SVG referenced by `<img>` or `<picture>` in a sandbox that blocks every external subresource. A remote font link never loads in that context, so the wordmark would fall back to a system sans. Each subset carries only the glyphs that asset draws: `Ccdnor` for the logo lockups, and the full tagline alphabet for the `github/` assets.

The icons and marks carry no text and need no font.

Jost is licensed under the SIL Open Font License 1.1 by The Jost Project Authors. The license is in [`fonts/Jost-OFL.txt`](fonts/Jost-OFL.txt) and covers the embedded subset.

To rebuild the subset after changing the text in a `github/` asset:

```sh
python3 -m fontTools.subset Jost[wght].ttf --text='<every character the asset draws>' \
  --flavor=woff2 --layout-features='kern,liga,calt' --output-file=jost-subset.woff2
```

## Color

The primary identity is dominated by near-black aubergine and deep purple. Indigo is a supporting undertone, not the main color. Use the CSS or JSON tokens in `tokens/`.

## Do not

- Re-round the shallow engine cut
- Use bright violet, blue, or magenta gradients
- Stretch, shear, or rotate the mark
- Add shadows, glows, aircraft details, or texture
- Reduce the wordmark tracking
- Place the dark logo on a dark background or the reversed logo on white
