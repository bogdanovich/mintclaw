import { spawnSync } from "node:child_process"
import { readFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

export function evaluateAudit(report, exceptions, now = new Date()) {
  const advisories = report?.advisories
  const counts = report?.metadata?.vulnerabilities
  const severities = ["info", "low", "moderate", "high", "critical"]
  if (
    report?.error != null ||
    !advisories ||
    Array.isArray(advisories) ||
    typeof advisories !== "object" ||
    !counts ||
    !Array.isArray(exceptions) ||
    severities.some(
      (severity) =>
        !Number.isSafeInteger(counts[severity]) || counts[severity] < 0,
    ) ||
    severities.reduce((sum, severity) => sum + counts[severity], 0) !==
      Object.keys(advisories).length
  ) {
    throw new Error("Missing or inconsistent pnpm audit evidence")
  }
  for (const exception of exceptions) {
    const expiry = new Date(`${exception.expiresOn}T00:00:00Z`)
    if (
      !/^GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}$/.test(exception.id) ||
      typeof exception.module !== "string" ||
      exception.module.trim().length === 0 ||
      typeof exception.version !== "string" ||
      exception.version.trim().length === 0 ||
      !severities.includes(exception.severity) ||
      !Array.isArray(exception.paths) ||
      exception.paths.length === 0 ||
      exception.paths.some(
        (entry) => typeof entry !== "string" || entry.length === 0,
      ) ||
      typeof exception.reason !== "string" ||
      exception.reason.trim().length === 0 ||
      !/^\d{4}-\d{2}-\d{2}$/.test(exception.expiresOn) ||
      Number.isNaN(expiry.getTime()) ||
      expiry.toISOString().slice(0, 10) !== exception.expiresOn
    ) {
      throw new Error("Invalid frontend advisory exception")
    }
  }
  const accepted = []
  const actionable = []
  for (const advisory of Object.values(advisories)) {
    if (
      !/^GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}$/.test(
        advisory.github_advisory_id,
      ) ||
      typeof advisory.module_name !== "string" ||
      advisory.module_name.trim().length === 0 ||
      !severities.includes(advisory.severity) ||
      !Array.isArray(advisory.findings) ||
      advisory.findings.length === 0
    ) {
      throw new Error("Incomplete pnpm advisory evidence")
    }
    for (const finding of advisory.findings) {
      if (
        typeof finding.version !== "string" ||
        finding.version.trim().length === 0 ||
        !Array.isArray(finding.paths) ||
        finding.paths.length === 0 ||
        finding.paths.some(
          (entry) => typeof entry !== "string" || entry.length === 0,
        )
      ) {
        throw new Error("Incomplete pnpm dependency-path evidence")
      }
      const exception = exceptions.find(
        (entry) =>
          entry.id === advisory.github_advisory_id &&
          entry.module === advisory.module_name &&
          entry.version === finding.version &&
          entry.severity === advisory.severity &&
          finding.paths.every((dependencyPath) =>
            entry.paths.includes(dependencyPath),
          ),
      )
      const covered =
        exception && now < new Date(`${exception.expiresOn}T00:00:00Z`)
      const label = `${advisory.github_advisory_id} ${advisory.module_name}@${finding.version}`
      if (covered)
        accepted.push(`${label} (exception expires ${exception.expiresOn})`)
      else actionable.push(label)
    }
  }
  return { accepted, actionable }
}

function main() {
  const frontend = fileURLToPath(new URL("..", import.meta.url))
  const pnpm = process.env.npm_execpath
  if (!pnpm || !path.basename(pnpm).includes("pnpm")) {
    throw new Error("Run this check with pnpm audit:check")
  }
  const result = spawnSync(process.execPath, [pnpm, "audit", "--json"], {
    cwd: frontend,
    encoding: "utf8",
    timeout: 45_000,
    maxBuffer: 8 * 1024 * 1024,
  })
  if (result.error || ![0, 1].includes(result.status)) {
    throw new Error(
      "pnpm audit did not complete; unavailable evidence cannot pass",
    )
  }
  const report = JSON.parse(result.stdout)
  const exceptions = JSON.parse(
    readFileSync(
      new URL("./advisory-exceptions.json", import.meta.url),
      "utf8",
    ),
  )
  const { accepted, actionable } = evaluateAudit(report, exceptions)
  for (const entry of accepted) console.log(`Accepted: ${entry}`)
  for (const entry of actionable) console.error(`Action required: ${entry}`)
  if (actionable.length > 0) process.exitCode = 1
  else
    console.log(
      `Advisory check passed (${accepted.length} documented exceptions).`,
    )
}

if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    main()
  } catch (error) {
    console.error(
      error instanceof SyntaxError ? "Invalid pnpm audit JSON" : error.message,
    )
    process.exitCode = 1
  }
}
