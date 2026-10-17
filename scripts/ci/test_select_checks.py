import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

from select_checks import CHECKS, changed_paths, local_dependencies, select_checks

REPO = Path(__file__).resolve().parents[2]


class SelectionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "go.mod").write_text("module example.com/test\n")
        self.source("pkg/document/read.go", 'package document\nimport "example.com/test/pkg/shared"\n')
        self.source(
            "pkg/shared/state_windows_test.go", "package shared\nimport alias `example.com/test/internal/leaf`\n"
        )
        self.source("internal/leaf/state.go", "package leaf\n")

    def source(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def selected(self, *paths):
        return {key for key, enabled in select_checks(paths, self.root).items() if enabled}

    def test_documentation_only(self):
        self.assertEqual(self.selected("docs/guides/start.md", "README.zh.md", "CONTRIBUTING.md"), set())

    def test_frontend_only(self):
        self.assertEqual(
            self.selected("web/frontend/package.json", "web/frontend/src/app.ts", "README.md"), {"frontend"}
        )

    def test_document_input_skips_unrelated_integrations(self):
        self.assertEqual(self.selected("pkg/document/read.go"), {"backend", "pdfium", "documents"})

    def test_pdf_dependencies_include_transitive_platform_and_test_imports(self):
        self.assertEqual(
            local_dependencies(self.root, ["pkg/document"]), {"pkg/document", "pkg/shared", "internal/leaf"}
        )
        for path in ("pkg/shared/state_windows_test.go", "internal/leaf/state.go", "pkg/shared/fixtures/input.bin"):
            with self.subTest(path=path):
                self.assertIn("pdfium", self.selected(path))

    def test_non_package_example_does_not_select_entire_pkg_tree(self):
        self.source("pkg/document/example_test.go", 'package document\nvar example = "example.com/test/pkg"\n')
        self.assertNotIn("pdfium", self.selected("pkg/unrelated/provider.go"))

    def test_parent_package_does_not_include_independent_child_packages(self):
        self.source("pkg/document/parent.go", 'package document\nimport "example.com/test/pkg"\n')
        self.source("pkg/env.go", "package pkg\n")
        self.assertIn("pdfium", self.selected("pkg/env.go"))
        self.assertNotIn("pdfium", self.selected("pkg/unrelated/provider.go"))

    def test_import_cycles_terminate(self):
        self.source("internal/leaf/state.go", 'package leaf\nimport "example.com/test/pkg/document"\n')
        self.assertEqual(len(local_dependencies(self.root, ["pkg/document"])), 3)

    def test_shared_go_runs_all_integrations(self):
        self.assertEqual(
            self.selected("pkg/providers/example.go"), {"backend", "credentials", "documents", "companion", "docker"}
        )

    def test_browser_and_credentials(self):
        for path in (
            "runtime/browser/playwright-library/package-lock.json",
            "pkg/browser/driver.go",
            "internal/credentials/approval/ui.go",
        ):
            with self.subTest(path=path):
                self.assertEqual(self.selected(path), {"backend", "credentials", "companion"})

    def test_docker_suite_inputs(self):
        for path in ("pkg/mcp/client.go", "integration/fixtures/server/Dockerfile"):
            self.assertEqual(self.selected(path), {"backend", "docker"})

    def test_backend_api_is_also_a_frontend_input(self):
        self.assertIn("frontend", self.selected("web/backend/api/router.go"))

    def test_build_dependency_workflow_and_unknown_inputs_are_full(self):
        for path in (
            "go.mod",
            "go.sum",
            "nested/go.mod",
            "Makefile",
            ".github/workflows/pr.yml",
            ".golangci.yaml",
            "scripts/ci/select_checks.py",
            "new-runtime/fixture.bin",
        ):
            with self.subTest(path=path):
                self.assertEqual(self.selected(path), set(CHECKS))

    def test_mixed_changes_are_unioned(self):
        self.assertEqual(
            self.selected("web/frontend/src/app.ts", "pkg/document/read.go"),
            {"frontend", "backend", "pdfium", "documents"},
        )

    def test_full_events_emit_all_outputs(self):
        for event in ("push", "schedule", "workflow_dispatch"):
            with self.subTest(event=event), tempfile.TemporaryDirectory() as temp:
                output, summary = Path(temp) / "output", Path(temp) / "summary"
                subprocess.run(
                    ["python3", str(REPO / "scripts/ci/select_checks.py")],
                    check=True,
                    capture_output=True,
                    env={
                        **os.environ,
                        "GITHUB_EVENT_NAME": event,
                        "GITHUB_OUTPUT": str(output),
                        "GITHUB_STEP_SUMMARY": str(summary),
                    },
                )
                self.assertEqual(
                    set(output.read_text().splitlines()), {f"{check}=true" for check in (*CHECKS, "integration")}
                )

    def test_pr_without_valid_comparison_fails(self):
        result = subprocess.run(
            ["python3", str(REPO / "scripts/ci/select_checks.py")],
            capture_output=True,
            env={**os.environ, "GITHUB_EVENT_NAME": "pull_request", "CI_BASE_SHA": "", "CI_HEAD_SHA": ""},
        )
        self.assertNotEqual(result.returncode, 0)


class DiffTests(unittest.TestCase):
    def test_merge_base_deletion_rename_and_unusual_names(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)

            def git(*args):
                return subprocess.check_output(["git", "-C", str(root), *args]).decode().strip()

            git("init", "-q", "-b", "base")
            git("config", "user.email", "ci-test@mintclaw.invalid")
            git("config", "user.name", "CI test")
            (root / "old.go").write_text("package old\n")
            git("add", ".")
            git("commit", "-qm", "baseline")
            git("checkout", "-qb", "topic")
            (root / "old.go").rename(root / "README.md")
            unusual = "space and\nnewline-ß.go"
            (root / unusual).write_text("package other\n")
            git("add", ".")
            git("commit", "-qm", "rename and add")
            head = git("rev-parse", "HEAD")
            git("checkout", "-q", "base")
            (root / "base-only.go").write_text("package base\n")
            git("add", ".")
            git("commit", "-qm", "advance base")
            self.assertEqual(set(changed_paths(root, git("rev-parse", "HEAD"), head)), {"old.go", "README.md", unusual})


class ScriptTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.log = self.root / "go.log"
        self.go = self.root / "go"
        self.go.write_text("""#!/usr/bin/env python3
import json, os, sys
with open(os.environ["CI_TEST_LOG"], "a") as log:
    log.write(json.dumps({"args": sys.argv[1:], "target": "/".join(os.environ.get(k, "") for k in ("GOOS", "GOARCH", "GOARM")), "cgo": os.environ.get("CGO_ENABLED")}) + "\\n")
sys.exit(1 if os.environ.get("GOARCH") == os.environ.get("CI_FAIL_ARCH", "never") else 0)
""")
        self.go.chmod(0o755)
        self.env = {**os.environ, "PATH": f"{self.root}:{os.environ['PATH']}", "CI_TEST_LOG": str(self.log)}

    def records(self):
        import json

        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_compile_keeps_all_targets_and_reports_any_failure(self):
        targets = "linux/amd64 linux/arm/7 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"
        result = subprocess.run(
            ["bash", str(REPO / "scripts/ci/compile-platforms.sh")],
            capture_output=True,
            env={**self.env, "COMPILE_TARGETS": targets, "CI_FAIL_ARCH": "arm"},
        )
        self.assertEqual(result.returncode, 1)
        records = self.records()
        self.assertEqual([record["target"].rstrip("/") for record in records], targets.split())
        self.assertTrue(all(record["cgo"] == "0" and "-exec=true" in record["args"] for record in records))

    def test_compile_rejects_missing_targets(self):
        result = subprocess.run(
            ["bash", str(REPO / "scripts/ci/compile-platforms.sh")],
            capture_output=True,
            env={**self.env, "COMPILE_TARGETS": ""},
        )
        self.assertNotEqual(result.returncode, 0)

    def test_integration_groups_preserve_full_default(self):
        patterns = {}
        for suite in ("companion", "documents", "all"):
            self.log.unlink(missing_ok=True)
            subprocess.run(["bash", str(REPO / "scripts/run-go-integration-tests.sh"), suite], check=True, env=self.env)
            test = self.records()[-1]["args"]
            patterns[suite] = test[test.index("-run") + 1]
        original_tests = {
            "TestCompanionProcessAuthenticatesAndInvokesOverWSS",
            "TestCompanionProcessTransfersFilesOverAuthenticatedWSS",
            "TestNodeInvocationVerticalSliceWithApprovalAndRealCompanion",
            "TestCompanionBrowserLifecycleAndReconnectOverProductionWSS",
            "TestNodeFileTransferVerticalSliceWithApprovalAndDelivery",
            "TestNodeJobVerticalSliceWithRestartArtifactAndCancellation",
            "TestNodeServiceStatusModelToSystemdRealProcessVerticalSlice",
            "TestDocumentPDFTelegramVerticalSlice",
            "TestGatewayDocumentIntakeUsesProductionCompositionAndOrdinaryTransport",
            "TestCodingDocumentReadOnlyVerticalSlice",
            "TestCodingDocumentLocalWriteToolLinuxIntegration",
            "TestNativeMintClawWorkerStartsSteersResumesAndShutsDown",
            "TestNativeMintClawWorkerContextPrefixCorpus",
            "TestNativeMintClawWorkerProjectsAndAnswersDurableQuestion",
            "TestNativeMintClawWorkerCrashReleasesLeaseWithoutBlindReplay",
            "TestNativeMintClawWorkerDisconnectAndHardCancelAreExplicit",
            "TestNativeMintClawWorkerMutatesOwnedWorktreeAndRecoversAfterCrash",
            "TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice",
        }
        for name in original_tests:
            self.assertRegex(name, patterns["all"])
            self.assertNotEqual(
                bool(re.fullmatch(patterns["documents"], name)), bool(re.fullmatch(patterns["companion"], name))
            )
        self.assertEqual(set(re.findall(r"Test[A-Za-z0-9_]+", patterns["all"])), original_tests)
        self.assertNotRegex("TestDocumentPDFTelegramVerticalSlice", patterns["companion"])
        self.log.unlink()
        subprocess.run(["bash", str(REPO / "scripts/run-go-integration-tests.sh")], check=True, env=self.env)
        default_args = self.records()[-1]["args"]
        self.assertEqual(default_args[default_args.index("-run") + 1], patterns["all"])

    def test_unknown_integration_suite_fails_before_build(self):
        result = subprocess.run(
            ["bash", str(REPO / "scripts/run-go-integration-tests.sh"), "typo"], capture_output=True, env=self.env
        )
        self.assertEqual(result.returncode, 2)
        self.assertFalse(self.log.exists())
