#!/usr/bin/env python3
"""Select PR checks; non-PR events always run the complete CI suite."""

import os
import re
import subprocess
from pathlib import Path

CHECKS = ("backend", "frontend", "pdfium", "credentials", "documents", "companion", "docker")
ROOT_DOCS = {"AGENTS.md", "CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "SECURITY.md"}


def local_dependencies(root, packages):
    """Include local Go imports from tests and every platform, plus embedded inputs.

    Scanning quoted module paths can overselect when a test contains an import
    example. It deliberately does not exclude build tags or test-only imports.
    """
    module = re.search(r"^module\s+(\S+)", (root / "go.mod").read_text(), re.MULTILINE).group(1)
    imports = re.compile(r'["`]' + re.escape(module) + r'/([^"`\s]+)["`]')
    pending = list(packages)
    dependencies = set()
    while pending:
        package = pending.pop()
        if package in dependencies:
            continue
        sources = list((root / package).glob("*.go"))
        if not sources:
            continue
        dependencies.add(package)
        for source in sources:
            pending.extend(imports.findall(source.read_text()))
    return dependencies


def documentation(path):
    return (
        path.startswith("docs/")
        or path in ROOT_DOCS
        or ("/" not in path and path.startswith(("README", "CHANGELOG")) and path.endswith(".md"))
    )


def dependency_input(path, packages):
    # A Go package does not own its child packages (pkg/env.go must not cause
    # every pkg/** change to select PDFium). Resource/testdata directories may
    # be embedded or consumed by tests, so treat them conservatively.
    if path.endswith(".go") and not any(part in path.split("/") for part in ("testdata", "fixtures")):
        return path.rpartition("/")[0] in packages
    return any(path.startswith(package + "/") for package in packages)


def select_checks(paths, root):
    selected = dict.fromkeys(CHECKS, False)
    pdf_dependencies = local_dependencies(root, ["pkg/document", "internal/qualification/pdfiumwasm"])
    for path in paths:
        if documentation(path):
            continue
        if path.startswith("web/frontend/"):
            selected["frontend"] = True
            continue
        # Dependency, build, workflow and unknown inputs receive complete coverage.
        if Path(path).name in {"go.mod", "go.sum", "Makefile"} or path.startswith(
            (".github/", ".golangci", "scripts/ci/")
        ):
            return dict.fromkeys(CHECKS, True)

        selected["backend"] = True
        pdf_input = dependency_input(path, pdf_dependencies) or path.startswith(
            ("scripts/document-", "scripts/internal/document", "internal/qualification/pdfiumwasm/")
        )
        if pdf_input:
            selected["pdfium"] = selected["documents"] = True

        if path.startswith(
            ("pkg/document/", "internal/qualification/pdfiumwasm/", "scripts/document-", "scripts/internal/document")
        ):
            selected["documents"] = True
        elif path.startswith(
            (
                "internal/credentials/",
                "pkg/credential",
                "cmd/mintclaw/internal/credentials/",
                "scripts/qualify-credential-",
            )
        ):
            selected["credentials"] = selected["companion"] = True
        elif path.startswith(("pkg/browser/", "runtime/browser/", "scripts/browser-", "scripts/internal/browser_")):
            selected["credentials"] = selected["companion"] = True
        elif path.startswith(("pkg/mcp/", "integration/")):
            selected["docker"] = True
        elif path.startswith(("pkg/", "cmd/", "internal/", "web/backend/")):
            # Shared Go code may affect any end-to-end suite. PDF qualification
            # follows the document package's local dependency closure above.
            for check in ("credentials", "documents", "companion", "docker"):
                selected[check] = True
            if path.startswith("web/backend/"):
                selected["frontend"] = True
        else:
            return dict.fromkeys(CHECKS, True)
    return selected


def changed_paths(root, base, head):
    # --no-renames preserves both the old and new path when moving code to docs.
    # NUL delimiters preserve spaces, newlines and non-ASCII filenames.
    result = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", "-z", f"{base}...{head}", "--"],
        cwd=root,
        check=True,
        stdout=subprocess.PIPE,
    )
    return [os.fsdecode(path) for path in result.stdout.split(b"\0") if path]


def main():
    root = Path(__file__).resolve().parents[2]
    if os.environ.get("GITHUB_EVENT_NAME") == "pull_request":
        base, head = os.environ["CI_BASE_SHA"], os.environ["CI_HEAD_SHA"]
        if not re.fullmatch(r"[0-9a-f]{40}", base) or not re.fullmatch(r"[0-9a-f]{40}", head):
            raise ValueError("PR comparison requires complete base and head commit SHAs")
        selected = select_checks(changed_paths(root, base, head), root)
    else:
        selected = dict.fromkeys(CHECKS, True)
    selected["integration"] = any(selected[key] for key in ("credentials", "documents", "companion", "docker"))
    output = "".join(f"{key}={str(value).lower()}\n" for key, value in selected.items())
    print(output, end="")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
            stream.write(output)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as stream:
            stream.write("## CI selection\n\n| Check | Run |\n| --- | --- |\n")
            stream.writelines(f"| {key} | {value} |\n" for key, value in selected.items())


if __name__ == "__main__":
    main()
