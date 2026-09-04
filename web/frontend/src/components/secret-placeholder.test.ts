import { describe, expect, it } from "vitest"

import { maskedSecretPlaceholder } from "./secret-placeholder"

describe("masked secret placeholder", () => {
  it("uses the fallback when no saved secret exists", () => {
    expect(maskedSecretPlaceholder("", "Enter a key")).toBe("Enter a key")
    expect(maskedSecretPlaceholder(undefined, "Enter a key")).toBe(
      "Enter a key",
    )
  })

  it("masks saved secrets without returning their full value", () => {
    const secret = "sk-super-secret-value"
    const placeholder = maskedSecretPlaceholder(secret)

    expect(placeholder).toBe("sk-*****alue")
    expect(placeholder).not.toContain(secret)
  })
})
