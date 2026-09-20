"""Validate PDF4A probe outputs and emit the machine-readable result."""

import argparse
import hashlib
import json
import platform
from pathlib import Path


def load(path: Path):
    return json.loads(path.read_text())


def load_stream(path: Path):
    decoder = json.JSONDecoder()
    source = path.read_text()
    values = []
    offset = 0
    while offset < len(source):
        while offset < len(source) and source[offset].isspace():
            offset += 1
        if offset == len(source):
            break
        value, offset = decoder.raw_decode(source, offset)
        values.append(value)
    return values


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


parser = argparse.ArgumentParser()
parser.add_argument("scratch", type=Path)
parser.add_argument("output", type=Path)
parser.add_argument("repository", type=Path)
args = parser.parse_args()

scratch = args.scratch
gate = load_stream(scratch / "gate.ndjson")
for item in gate:
    item["file"] = Path(item["file"]).name
gate_by_name = {Path(item["file"]).name: item for item in gate}
expected_gate = {
    "static.pdf": ("candidate", "pure_static_script_free_unsigned_packet_array"),
    "foreground.pdf": ("refuse", "foreground_or_rendering_authority_unknown"),
    "dynamic.pdf": ("refuse", "dynamic_or_unknown_rendering"),
    "repeating.pdf": ("refuse", "repeating_or_page_growth"),
    "scripted.pdf": ("refuse", "script_action_or_external_data"),
    "malformed.pdf": ("refuse", "malformed_or_unsafe_xml"),
    "signed-restricted.pdf": ("refuse", "signed_restricted_or_encrypted"),
    "encrypted.pdf": ("refuse", "signed_restricted_or_encrypted"),
}
for name, expected in expected_gate.items():
    actual = gate_by_name[name]
    assert (actual["decision"], actual["reason"]) == expected, (name, actual)

pdfer_safe = load(scratch / "pdfer-safe.json")
pdfer_repeat = load(scratch / "pdfer-repeat.json")
pdfer_raw = load(scratch / "pdfer-raw-negative.json")
assert pdfer_safe["source_unchanged"] is True
assert pdfer_safe["dataset_well_formed"] is True
assert pdfer_repeat["output_sha256"] == pdfer_safe["output_sha256"]
assert pdfer_raw["dataset_well_formed"] is False

before = load(scratch / "pdfjs-before.json")
after = load(scratch / "pdfjs-after.json")
assert before["isPureXfa"] is True and before["value"] == "MINTCLAW_XFA_BEFORE"
assert after["isPureXfa"] is True and after["value"] == "MINTCLAW <&> café 😀"
assert before["blockedRequests"] == [] and after["blockedRequests"] == []

pikepdf = load(scratch / "pikepdf.json")
pikepdf_render = load(scratch / "pikepdf-render.json")
xfa_tools = load(scratch / "pdf-xfa-tools.json")
xfa_tools_render = load(scratch / "pdf-xfa-tools-render.json")
for render_probe in (pikepdf_render, xfa_tools_render):
    render_probe["input"] = Path(render_probe["input"]).name
for packet_probe in (pikepdf, xfa_tools):
    assert packet_probe["source_unchanged"] is True
    assert packet_probe["dataset_value_updated"] is True
for render_probe in (pikepdf_render, xfa_tools_render):
    assert render_probe["isPureXfa"] is True
    assert render_probe["visibleValue"] == "MINTCLAW_XFA_AFTER"

manifest = load(scratch / "fixtures" / "manifest.json")
encrypted_path = scratch / "fixtures" / "encrypted.pdf"
manifest["fixtures"].append(
    {
        "id": "encrypted",
        "file": "encrypted.pdf",
        "sha256": digest(encrypted_path),
        "construction": "existing_pdf0b_pdfcpu_aes256_fixture_copy",
        "license": "MIT (MintClaw repository)",
        "class": "password_encrypted_pdf_envelope",
        "expected_gate": "refuse_encrypted",
    }
)

result = {
    "schema_version": "mintclaw.pdf4a.xfa_qualification_result.v1",
    "decision": "supported-subset-candidate",
    "subset": "pure static packet-array XFA with fixed layout, no scripts/actions/repeats, and no security state",
    "platform": {
        "system": platform.system(),
        "machine": platform.machine(),
        "python": platform.python_version(),
    },
    "pins": {
        "pdfer": "fda4cc14c67f72bdebfddba26dba36a5f5cba39b",
        "pikepdf": "10.13.0.post1 (910e06b7d547e4e040780748f8ecc0fb7eba7181)",
        "pdf-xfa-tools": "da2e899e7ea3520a2ad85b2a041db5841b23b1ed",
        "pdfjs": "6.3.289 (1c8020a7d4e43668ac287a3ecf9a8dbea17e4c56)",
        "playwright": "1.63.0",
        "pdfium_source_review": "4a9b3d668e1ac05625b6576c0c1862e12cbe2ed4",
    },
    "fixtures": manifest["fixtures"],
    "existing_regression_fixtures": {
        name: {
            "path": str(path.relative_to(args.repository)),
            "sha256": digest(path),
            "license": "MIT (MintClaw repository)",
        }
        for name, path in {
            "ordinary_acroform": args.repository / "pkg/document/testdata/acroform-fields.pdf",
            "hybrid_static_xfa": args.repository / "pkg/document/testdata/hybrid-xfa-static.pdf",
            "dynamic_xfa_stream": args.repository / "pkg/document/testdata/xfa-dynamic.pdf",
            "password_encrypted_pdf": args.repository
            / "pkg/document/testdata/encrypted-password-required.pdf",
        }.items()
    },
    "gate_results": gate,
    "mutation": {
        "pdfer_safe": pdfer_safe,
        "pdfer_repeat": pdfer_repeat,
        "pdfer_raw_negative_control": pdfer_raw,
    },
    "independent_render": {
        "renderer": "PDF.js 6.3.289 with enableXfa=true, isEvalSupported=false, no scripting manager",
        "before": before,
        "after": after,
        "before_png_sha256": digest(scratch / "before.png"),
        "after_png_sha256": digest(scratch / "after.png"),
    },
    "packet_level_probes": {
        "pikepdf": {"mutation": pikepdf, "render": pikepdf_render},
        "pdf-xfa-tools": {"mutation": xfa_tools, "render": xfa_tools_render},
    },
    "candidate_outcomes": {
        "pdfer": "qualified only as a pinned packet updater behind a MintClaw-owned XML escaping and policy gate",
        "pdf-xfa-tools": "rejected: packet-level only, no declared repository license, inactive since 2022",
        "pikepdf": "oracle helper only: project explicitly does not support XFA semantics, rendering, or filling",
        "pdfjs": "qualification renderer only: independently proves visible output; not a production mutator",
        "pdfium": "deferred: XFA is disabled by default and an XFA/V8 build is too heavy for this subset",
    },
    "security": {
        "external_requests_observed": False,
        "scripting_manager_instantiated": False,
        "raw_unescaped_mutation_rejected_by_evidence": True,
        "dynamic_scripted_foreground_repeating_malformed_signed_encrypted_refused": True,
    },
    "baseline": {
        "command": "CGO_ENABLED=0 go test ./pkg/document",
        "status": "passed",
        "production_runtime_changed": False,
    },
}

args.output.mkdir(parents=True, exist_ok=True)
(args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
(args.output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
(args.output / "before.png").write_bytes((scratch / "before.png").read_bytes())
(args.output / "after.png").write_bytes((scratch / "after.png").read_bytes())
