# Vendored chart assets

These two minified JavaScript builds are vendored into the repository so that
`//go:embed assets/*.js` can inline them into the generated `report.html`.

**Why vendored instead of linked from a CDN:** the report is a share delivery
artifact and must render with **zero external requests** (offline, no CDN
dependency, no third-party request at open time). Linking would silently break
the report for any recipient without network access.

Both files are unmodified upstream release artifacts. Do not reformat, minify,
strip comments from, or otherwise edit them: any byte change invalidates the
digests recorded below.

---

## `echarts.min.js`

| Field | Value |
| --- | --- |
| Package | `echarts` |
| Version | **5.6.0** |
| License | **Apache-2.0** (upstream `package.json` `license` field; tarball ships `LICENSE` = Apache License 2.0) |
| Source URL used | `https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js` (jsdelivr resolved `echarts@5` → `5.6.0`; response header `x-jsd-version: 5.6.0`) |
| Byte size | **1034102** |
| Local sha256 | `bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1` |
| npm `dist.integrity` (sha512, SRI) | `sha512-oTbVTsXfKuEhxftHqL5xprgLoc0k7uScAwtryCgWF6hPYFLRwOUHiFmHGCBKP5NPFNkDVopOieyUqYGH8Fa3kA==` |
| npm `dist.shasum` (sha1) | `2377874dca9fb50f104051c3553544752da3c9d6` |
| Observed tail (last 80 chars) | `",t.zrUtil=St,t.zrender=jr,Object.defineProperty(t,"__esModule",{value:!0})}));` + LF |

## `echarts-wordcloud.min.js`

| Field | Value |
| --- | --- |
| Package | `echarts-wordcloud` |
| Version | **2.1.0** |
| License | **ISC** (upstream `package.json` `license` field) |
| Source URL used | `https://cdn.jsdelivr.net/npm/echarts-wordcloud@2/dist/echarts-wordcloud.min.js` (jsdelivr resolved `echarts-wordcloud@2` → `2.1.0`; response header `x-jsd-version: 2.1.0`) |
| Byte size | **16552** |
| Local sha256 | `7b6f0d55971d9de5913120c7ce6342f3551efd00b4a1df8a50f08385bb25f155` |
| npm `dist.integrity` (sha512, SRI) | `sha512-Kt1JmbcROgb+3IMI48KZECK2AP5lG6bSsOEs+AsuwaWJxQom31RTNd6NFYI01E/YaI1PFZeueaupjlmzSQasjQ==` |
| npm `dist.shasum` (sha1) | `c3de6fe267044f6c3343e4ff0e05eedb01c05096` |
| Observed tail (last 80 chars) | `e",{value:!0})},r(638)})()}));` + LF + `//# sourceMappingURL=echarts-wordcloud.min.js.map` |

> Note on the trailing `sourceMappingURL` in the wordcloud build: it is a
> **relative** path, not an absolute URL, and the `.map` file is intentionally
> not shipped. Browsers only request a source map when devtools are open; it
> never causes a network request during normal rendering, and the report's
> external-reference detector only flags `http://` / `https://` targets. The
> comment was left in place rather than stripped so the file remains a pristine,
> digest-verifiable upstream artifact.

---

## How the integrity was verified (re-checkable)

The `dist.integrity` / `dist.shasum` values above are taken from the npm registry
metadata (`https://registry.npmjs.org/echarts`, `https://registry.npmjs.org/echarts-wordcloud`),
which is an **independent** source — not a hash computed from the downloaded file.

npm's `dist.integrity` covers the **published tarball**, not the individual
`dist/*.min.js` file, so the verification was done in two steps:

1. Download the release tarball from the registry
   (`https://registry.npmjs.org/echarts/-/echarts-5.6.0.tgz`,
   `https://registry.npmjs.org/echarts-wordcloud/-/echarts-wordcloud-2.1.0.tgz`)
   and confirm its sha512 matches npm's `dist.integrity` and its sha1 matches
   `dist.shasum`. **Both matched for both packages.**
2. Extract `package/dist/*.min.js` from that verified tarball and compare it
   byte-for-byte against the jsdelivr download. `cmp` reported no differences
   and the sha256 of both copies is identical — i.e. two independent delivery
   paths (jsdelivr CDN and the npm registry tarball) produced the same bytes,
   and the tarball is anchored to the registry's published digest.

Truncation was additionally ruled out: neither file contains a NUL byte, and both
min builds terminate with `));` followed by a newline (the ECharts build ends with
`}));`, the wordcloud build with `})()}));`), which is the expected shape of an
intact UMD minified bundle. A truncated download would also have failed the
`cmp` / sha256 comparison against the tarball copy above.

To re-check a file by hand:

```sh
sha256sum echarts.min.js echarts-wordcloud.min.js
# expect:
# bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1 *echarts.min.js
# 7b6f0d55971d9de5913120c7ce6342f3551efd00b4a1df8a50f08385bb25f155 *echarts-wordcloud.min.js

wc -c echarts.min.js echarts-wordcloud.min.js
# expect: 1034102 and 16552
```