# Third-Party Notices

MintClaw includes the following components in its portable PDF runtime:

| File | Component | Version / revision | License SHA-256 |
| --- | --- | --- | --- |
| `go-pdfium.txt` | `github.com/klippa-app/go-pdfium` | `v1.20.0` | `fd871478ba874c3e1736c691e3ca89a350ab769db6c7aac9818024f912afb488` |
| `wazero.txt` | `github.com/tetratelabs/wazero` | `v1.12.0` | `c46f033d017a5af71a1de0105ec56c41bd47f81a0bbdf779fffe316336dc7c1f` |
| `pdfium.txt` | PDFium | Chromium branch `8044` | `1fe9dea718fbd75cf149adaf4d8a22a4335604d964ddb76d1b45383dec8668c9` |

Each listed file is an unmodified copy of the corresponding upstream license
text. Release archives, native packages, and container images distribute this
directory with the MintClaw binaries.

## Optional private computer driver

`cua-driver.txt` preserves the MIT license of Cua AI, Inc. from
`trycua/cua` revision `0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc`, covering
the downstream AT-SPI source patch and optional private build path. The driver
is not bundled with, installed by or enabled in normal MintClaw releases.
Its private build artifact separately carries the upstream license and the
driver workspace's own third-party notices.
