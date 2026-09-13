# Local Coding Agent TUI VF.3 Exit Record

Roadmap packet:
[VF.3 — Source-backed semantic Markdown cells](local-coding-agent-tui-visual-followup-roadmap.md#vf3--source-backed-semantic-markdown-cells).

The merge containing this record closes VF.3. Assistant commentary and final
answers now render as readable terminal-native Markdown without replacing the
canonical transcript text with ANSI or a renderer-specific representation.

## Shipped behavior

- Paragraphs, headings, unordered and ordered lists, nested lists, emphasis,
  strong text, combined emphasis, strikethrough, inline and fenced code,
  blockquotes, thematic rules, links, and tables have semantic presentation.
- Ordinary table layouts allocate widths from visible content and column
  alignment. Tables that cannot fit, including tables with more than six
  columns, become complete labeled records instead of clipping or introducing
  horizontal scrolling. If repeating long headings would exceed the 16 KiB
  label budget, one complete numbered heading legend is rendered and rows use
  short numeric labels.
- Mixed-style text wraps by terminal grapheme width. CJK, emoji, combining
  characters, and one- or two-column terminals cannot force horizontal
  overflow.
- Incomplete streaming Markdown remains visible. A later revision replaces
  only its active cell; committed cells keep their identity and cached output.
- User messages, typed plans, commands, tools, diffs, and lifecycle evidence
  retain their existing literal or typed renderers. Markdown interpretation is
  limited to assistant commentary and final answers.

## Source, reflow, and resume ownership

The persisted transcript message remains authoritative. A presentation cell
sanitizes that source before parsing and may retain one derived Markdown syntax
tree for its immutable revision and at most four width/theme/mode documents
under the existing renderer-cache bound. Resize reuses the syntax tree and
recomputes terminal lines; a changed streaming revision creates a new cell and
parse. Resume builds a fresh cell from the persisted source and produces the
same plain semantic document.

Plain mode removes all style roles after semantic layout, so transcript copy
and search receive deterministic, complete text without ANSI or OSC controls.
Links show their label and, when useful, a sanitized `http`, `https`, `mailto`,
or relative destination. No terminal hyperlink escape sequence is emitted.

## Safety and bounded degradation

Terminal controls, OSC payloads, carriage returns, invalid directional
controls, and unsafe link schemes are removed before parsing or display. Raw
HTML and math have inert text presentation rather than executable terminal
effects. The frontend's existing 64 KiB per-message projection bound limits
parser input, and oversized tables use the record projection. If malformed or
excessively nested input yields no usable semantic blocks, the safe source is
shown as bounded wrapped text instead of disappearing.

The stacked-table label budget prevents a bounded source from multiplying one
long heading across every row. Its numbered-legend fallback retains every
heading and value while keeping generated labeling overhead bounded.

The renderer deliberately does not fetch link or image destinations, execute
HTML, perform syntax highlighting, or persist an AST. Image Markdown is a text
label; durable coding attachments remain owned by the existing attachment
pipeline.

## Performance evidence

The TUI.15 budgets are 100 ms / 8 MiB for first paint, 10 ms / 1 MiB for one
streaming update, and 100 ms / 8 MiB for resize. Representative post-change
measurements on the documented 256-message fixture were:

| Operation | Time/op | Bytes/op | Budget |
| --- | ---: | ---: | ---: |
| First paint | 3.73 ms | 3.19 MiB | 100 ms / 8 MiB |
| Output update | 385 us | 222 KiB | 10 ms / 1 MiB |
| Resize/reflow | 3.52 ms | 2.23 MiB | 100 ms / 8 MiB |

The fixture treats every completed assistant message as Markdown, so it
exercises the new parser on first paint. The per-revision syntax-tree cache
avoids reparsing those messages during later width and capability changes.

## Regression evidence

Focused tests cover 40-, 80-, and 120-column semantic output; light and dark
themes; truecolor, 256-color, ANSI-16, and no-color modes; wide and stacked
tables; style-preserving Unicode wrapping; partial streaming; revision-local
replacement; resume reconstruction; bounded caches; plain-mode output;
terminal-control and unsafe-link sanitization; deep nesting; oversized tables;
long-header/many-row amplification; and malformed fuzz seeds. A semantic
golden records representative heading, paragraph, table, list, and code
output.

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/tui
go test -race -count=1 ./pkg/coding/tui
go test -run '^$' -fuzz '^FuzzAssistantMarkdownIsBoundedAndControlFree$' \
  -fuzztime=10s ./pkg/coding/tui
go test -run '^$' \
  -bench 'Benchmark(PresentationFirstPaint|PresentationOutputUpdate|PresentationResizeReflow)$' \
  -benchmem -count=3 ./pkg/coding/tui
scripts/pre-push-lint.sh --changed
make lint-docs
git diff --check
```

## Exit-gate decision

VF.3's semantic coverage, narrow-table adaptation, authoritative-source
reflow/resume, partial-stream stability, copy/search safety, hostile-input
handling, cache bounds, and performance criteria are satisfied. Coding-only
response concision and truthful reasoning status remain VF.4. The integrated
visual, PTY, documentation, deployment, and production smoke closeout remains
VF.5.
