import { describe, expect, it } from "vitest"

import type { ModelProviderOption } from "@/api/models"

import {
  getEffectiveAPIBase,
  getSubmittedAPIBase,
  normalizeApiBase,
} from "./model-provider-form-shared"

const providers: ModelProviderOption[] = [
  {
    id: "openai",
    default_api_base: "https://api.openai.com/v1/",
    empty_api_key_allowed: false,
    create_allowed: true,
    default_model_allowed: true,
    aliases: ["open-ai"],
  },
]

describe("model provider form projection", () => {
  it("normalizes an explicitly entered API base", () => {
    expect(normalizeApiBase("  https://example.test/v1///  ")).toBe(
      "https://example.test/v1",
    )
  })

  it("projects the provider default through an alias", () => {
    expect(getEffectiveAPIBase(" Open-AI ", "", providers)).toBe(
      "https://api.openai.com/v1",
    )
  })

  it("omits empty submitted values", () => {
    expect(getSubmittedAPIBase("   ")).toBeUndefined()
    expect(getSubmittedAPIBase(" https://example.test/v1/ ")).toBe(
      "https://example.test/v1",
    )
  })
})
