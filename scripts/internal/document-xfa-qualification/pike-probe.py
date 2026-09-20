"""Qualify pikepdf and pdf-xfa-tools as packet-level XFA mutators."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path

import pikepdf


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def load_xfa_tools(path: Path):
    spec = importlib.util.spec_from_file_location("qualification_xfa_tools", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


parser = argparse.ArgumentParser()
parser.add_argument("--backend", choices=("pikepdf", "pdf-xfa-tools"), required=True)
parser.add_argument("--xfa-tools", type=Path)
parser.add_argument("input", type=Path)
parser.add_argument("output", type=Path)
args = parser.parse_args()

source = args.input.read_bytes()
with pikepdf.open(args.input) as pdf:
    if args.backend == "pdf-xfa-tools":
        if args.xfa_tools is None:
            parser.error("--xfa-tools is required for pdf-xfa-tools")
        module = load_xfa_tools(args.xfa_tools)
        xfa = module.XfaObj(pdf)
        datasets = xfa["datasets"]
        xfa["datasets"] = datasets.replace(
            "MINTCLAW_XFA_BEFORE", "MINTCLAW_XFA_AFTER"
        )
    else:
        packets = pdf.Root.AcroForm.XFA
        names = [str(packets[index]) for index in range(0, len(packets), 2)]
        dataset_index = names.index("datasets") * 2 + 1
        stream = packets[dataset_index]
        datasets = stream.read_bytes().decode("utf-8")
        stream.write(
            datasets.replace(
                "MINTCLAW_XFA_BEFORE", "MINTCLAW_XFA_AFTER"
            ).encode("utf-8")
        )
    pdf.save(args.output)

with pikepdf.open(args.output) as pdf:
    packets = pdf.Root.AcroForm.XFA
    names = [str(packets[index]) for index in range(0, len(packets), 2)]
    dataset_index = names.index("datasets") * 2 + 1
    output_dataset = packets[dataset_index].read_bytes().decode("utf-8")

source_after = args.input.read_bytes()
output = args.output.read_bytes()
print(
    json.dumps(
        {
            "backend": args.backend,
            "source_sha256": digest(source),
            "source_unchanged": digest(source_after) == digest(source),
            "output_sha256": digest(output),
            "dataset_value_updated": "MINTCLAW_XFA_AFTER" in output_dataset,
        },
        indent=2,
    )
)
