import { describe, expect, it } from "vitest"

import type { ModelInfo, ModelProviderOption } from "@/api/models"

import {
  EMPTY_MODEL_FORM,
  modelToForm,
  projectModelForm,
  transitionModelProvider,
  validateModelForm,
} from "./model-form"

const providers: ModelProviderOption[] = [
  {
    id: "oauth-provider",
    default_api_base: "https://oauth.test/v1",
    default_auth_method: "oauth",
    auth_method_locked: true,
    empty_api_key_allowed: true,
    create_allowed: true,
    default_model_allowed: true,
    aliases: ["oauth-alias"],
  },
  {
    id: "api-provider",
    default_api_base: "https://api.test/v1",
    empty_api_key_allowed: false,
    create_allowed: true,
    default_model_allowed: true,
  },
]

function modelFixture(): ModelInfo {
  return {
    index: 2,
    model_name: "primary",
    provider: "oauth-alias",
    model: "remote/model",
    api_base: "https://oauth.test/v1",
    api_key: "••••saved",
    auth_method: "oauth",
    rpm: 60,
    request_timeout: 45,
    streaming: { enabled: true },
    extra_body: { reasoning: { effort: "high" } },
    custom_headers: { "X-Region": "west" },
    enabled: true,
    available: true,
    status: "available",
    is_default: true,
    is_virtual: false,
  }
}

describe("model form contract", () => {
  it("maps config into editable state without copying a secret placeholder", () => {
    const form = modelToForm(modelFixture(), providers)

    expect(form).toMatchObject({
      provider: "oauth-provider",
      modelId: "remote/model",
      apiKey: "",
      rpm: "60",
      requestTimeout: "45",
      streamingEnabled: true,
    })
    expect(JSON.parse(form.extraBody)).toEqual({
      reasoning: { effort: "high" },
    })
  })

  it("projects trimmed form values and preserves an omitted secret", () => {
    const form = {
      ...modelToForm(modelFixture(), providers),
      modelId: "  replacement/model  ",
      apiBase: " https://custom.test/v1/ ",
      extraBody: "",
      customHeaders: "",
    }

    expect(projectModelForm(form, providers, true)).toEqual({
      ok: true,
      value: expect.objectContaining({
        provider: "oauth-provider",
        model: "replacement/model",
        api_base: "https://custom.test/v1",
        api_key: undefined,
        auth_method: "oauth",
        streaming: { enabled: true },
        extra_body: {},
        custom_headers: {},
      }),
    })
  })

  it("keeps explicit streaming disablement in an edit patch", () => {
    const projection = projectModelForm(EMPTY_MODEL_FORM, providers, true)

    expect(projection).toMatchObject({
      ok: true,
      value: { streaming: { enabled: false } },
    })
  })

  it("identifies invalid JSON without discarding the form state", () => {
    const form = { ...EMPTY_MODEL_FORM, extraBody: "[1, 2]" }

    expect(projectModelForm(form, providers)).toEqual({
      ok: false,
      field: "extraBody",
    })
    expect(form.extraBody).toBe("[1, 2]")
  })

  it("requires custom header values to be strings", () => {
    const form = { ...EMPTY_MODEL_FORM, customHeaders: '{"X-Retry": 3}' }

    expect(projectModelForm(form, providers)).toEqual({
      ok: false,
      field: "customHeaders",
    })
  })

  it("changes locked provider defaults without changing the model", () => {
    const original = {
      ...EMPTY_MODEL_FORM,
      provider: "oauth-provider",
      modelId: "keep/model",
      apiBase: "https://oauth.test/v1/",
      authMethod: "oauth",
    }

    expect(
      transitionModelProvider(original, "api-provider", providers),
    ).toEqual({
      ...original,
      provider: "api-provider",
      apiBase: "",
      authMethod: "",
    })
  })

  it("validates provider and model requirements through one schema", () => {
    expect(validateModelForm(EMPTY_MODEL_FORM, providers)).toEqual({
      provider: "invalid",
      modelId: "required",
    })
    expect(
      validateModelForm(
        {
          ...EMPTY_MODEL_FORM,
          provider: "api-provider",
          modelId: "valid/model",
        },
        providers,
        { level: "error", messageKey: "models.invalid" },
      ),
    ).toEqual({ provider: undefined, modelId: "invalid" })
  })
})
