import { describe, expect, it } from "vitest"

import type { CatalogEntry, ModelProviderOption } from "@/api/models"

import { selectCatalogModelIds } from "./use-model-form-controller"

const providers: ModelProviderOption[] = [
  {
    id: "openai",
    default_api_base: "https://api.openai.com/v1",
    empty_api_key_allowed: false,
    create_allowed: true,
    default_model_allowed: true,
    aliases: ["open-ai"],
  },
]

function catalog(
  id: string,
  provider: string,
  apiBase: string,
  models: string[],
): CatalogEntry {
  return {
    id,
    provider,
    api_base: apiBase,
    api_key_mask: "",
    models: models.map((model) => ({ id: model })),
    fetched_at: "",
  }
}

describe("model form controller helpers", () => {
  it("selects matching catalog models through aliases and removes duplicates", () => {
    const entries = [
      catalog("one", "open-ai", "https://api.openai.com/v1/", [
        "gpt-one",
        "gpt-two",
      ]),
      catalog("two", "openai", "https://api.openai.com/v1", ["gpt-two"]),
      catalog("other-base", "openai", "https://other.test/v1", ["ignored"]),
      catalog("other-provider", "custom", "https://api.openai.com/v1", [
        "ignored",
      ]),
    ]

    expect(
      selectCatalogModelIds(
        entries,
        "openai",
        "https://api.openai.com/v1",
        providers,
      ),
    ).toEqual(["gpt-one", "gpt-two"])
  })
})
