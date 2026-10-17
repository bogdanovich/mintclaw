import { describe, expect, it } from "vitest"

import { evaluateAudit } from "./check-advisories.mjs"

const exception = {
  id: "GHSA-aaaa-bbbb-cccc",
  module: "build-parser",
  version: "1.2.3",
  severity: "high",
  paths: [".>build-plugin>build-parser"],
  expiresOn: "2026-11-17",
  reason: "Reviewed build input only; recheck before renewal.",
}

function report() {
  return {
    metadata: {
      vulnerabilities: { info: 0, low: 0, moderate: 0, high: 1, critical: 0 },
    },
    advisories: {
      123: {
        github_advisory_id: exception.id,
        module_name: exception.module,
        severity: exception.severity,
        findings: [{ version: exception.version, paths: [...exception.paths] }],
      },
    },
  }
}

const reviewedAt = new Date("2026-10-17T12:00:00Z")

describe("frontend advisory policy", () => {
  it("accepts only the reviewed exception before its expiry", () => {
    const result = evaluateAudit(report(), [exception], reviewedAt)
    expect(result.actionable).toEqual([])
    expect(result.accepted).toHaveLength(1)
    expect(
      evaluateAudit(report(), [exception], new Date("2026-11-17T00:00:00Z"))
        .actionable,
    ).toHaveLength(1)
  })

  it.each(["id", "module", "version", "severity", "path"])(
    "rejects a changed %s rather than widening an exception",
    (changed) => {
      const evidence = report()
      const finding = evidence.advisories["123"]
      if (changed === "id") finding.github_advisory_id = "GHSA-dddd-eeee-ffff"
      if (changed === "module") finding.module_name = "runtime-parser"
      if (changed === "version") finding.findings[0].version = "1.2.4"
      if (changed === "severity") {
        finding.severity = "critical"
        evidence.metadata.vulnerabilities.high = 0
        evidence.metadata.vulnerabilities.critical = 1
      }
      if (changed === "path")
        finding.findings[0].paths.push(".>runtime>build-parser")
      expect(
        evaluateAudit(evidence, [exception], reviewedAt).actionable,
      ).toHaveLength(1)
    },
  )

  it("reports a new finding alongside accepted ones", () => {
    const evidence = report()
    evidence.advisories["123"].findings.push({
      version: "1.2.4",
      paths: [".>build-plugin>build-parser"],
    })
    const result = evaluateAudit(evidence, [exception], reviewedAt)
    expect(result.accepted).toHaveLength(1)
    expect(result.actionable).toHaveLength(1)
  })

  it("rejects missing or internally inconsistent audit evidence", () => {
    expect(() =>
      evaluateAudit({ error: "registry unavailable" }, [], reviewedAt),
    ).toThrow()
    expect(() =>
      evaluateAudit(
        { ...report(), error: "registry unavailable" },
        [exception],
        reviewedAt,
      ),
    ).toThrow()
    const evidence = report()
    evidence.advisories = {}
    expect(() => evaluateAudit(evidence, [], reviewedAt)).toThrow()
    const missingPath = report()
    missingPath.advisories["123"].findings[0].paths = []
    expect(() => evaluateAudit(missingPath, [exception], reviewedAt)).toThrow()
  })

  it("rejects an invalid exception instead of accepting it indefinitely", () => {
    expect(() =>
      evaluateAudit(
        report(),
        [{ ...exception, expiresOn: "2026-02-31" }],
        reviewedAt,
      ),
    ).toThrow()
  })
})
