# Vendored xterm assets

HomeCTL embeds these browser assets directly in the Server binary. Runtime does not require npm, jsDelivr, or any other CDN.

| Package | Version | Runtime file |
|---|---:|---|
| `@xterm/xterm` | `6.0.0` | `xterm-6.0.0.mjs`, `xterm-6.0.0.css` |
| `@xterm/addon-fit` | `0.11.0` | `addon-fit-0.11.0.mjs` |

The files were copied from the official npm package archives and retain their upstream license headers. Package integrity values verified during vendoring:

```text
@xterm/xterm@6.0.0
sha512-TQwDdQGtwwDt+2cgKDLn0IRaSxYu1tSUjgKarSDkUM0ZNiSRXFpjxEsvc/Zgc5kq5omJ+V0a8/kIM2WD3sMOYg==

@xterm/addon-fit@0.11.0
sha512-jYcgT6xtVYhnhgxh3QgYDnnNMYTcf8ElbxxFzX0IZo+vabQqSPAjC3c1wJrKB5E19VwQei89QCiZZP86DCPF7g==
```

Both packages are MIT licensed; the corresponding license files are included beside the assets.
