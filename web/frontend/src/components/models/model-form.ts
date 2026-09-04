import type {
  ModelInfo,
  ModelProviderOption,
  ModelSaveRequest,
} from "@/api/models"

import {
  getSubmittedAPIBase,
  normalizeApiBase,
} from "./model-provider-form-shared"
import type { FieldValidation } from "./model-validation"
import {
  getCanonicalProviderKey,
  getProviderCatalogEntry,
  getProviderDefaultAPIBase,
  getProviderDefaultAuthMethod,
  isProviderAuthMethodLocked,
} from "./provider-registry"

export interface ModelFormState {
  provider: string
  modelId: string
  apiKey: string
  apiBase: string
  proxy: string
  authMethod: string
  connectMode: string
  workspace: string
  rpm: string
  maxTokensField: string
  requestTimeout: string
  thinkingLevel: string
  toolSchemaTransform: string
  streamingEnabled: boolean
  extraBody: string
  customHeaders: string
}

export const EMPTY_MODEL_FORM: ModelFormState = {
  provider: "",
  modelId: "",
  apiKey: "",
  apiBase: "",
  proxy: "",
  authMethod: "",
  connectMode: "",
  workspace: "",
  rpm: "",
  maxTokensField: "",
  requestTimeout: "",
  thinkingLevel: "",
  toolSchemaTransform: "",
  streamingEnabled: false,
  extraBody: "",
  customHeaders: "",
}

export interface ModelFormValidation {
  provider?: "invalid"
  modelId?: "required" | "invalid"
}

export function validateModelForm(
  form: ModelFormState,
  providerOptions?: ModelProviderOption[],
  modelValidation?: FieldValidation | null,
): ModelFormValidation {
  return {
    provider: getProviderCatalogEntry(form.provider, providerOptions)
      ? undefined
      : "invalid",
    modelId: !form.modelId.trim()
      ? "required"
      : modelValidation?.level === "error"
        ? "invalid"
        : undefined,
  }
}

export function modelToForm(
  model: ModelInfo,
  providerOptions?: ModelProviderOption[],
): ModelFormState {
  return {
    provider: getCanonicalProviderKey(model.provider, providerOptions),
    modelId: model.model,
    apiKey: "",
    apiBase: model.api_base ?? "",
    proxy: model.proxy ?? "",
    authMethod: model.auth_method ?? "",
    connectMode: model.connect_mode ?? "",
    workspace: model.workspace ?? "",
    rpm: model.rpm ? String(model.rpm) : "",
    maxTokensField: model.max_tokens_field ?? "",
    requestTimeout: model.request_timeout ? String(model.request_timeout) : "",
    thinkingLevel: model.thinking_level ?? "",
    toolSchemaTransform: model.tool_schema_transform ?? "",
    streamingEnabled: model.streaming?.enabled === true,
    extraBody: model.extra_body
      ? JSON.stringify(model.extra_body, null, 2)
      : "",
    customHeaders: model.custom_headers
      ? JSON.stringify(model.custom_headers, null, 2)
      : "",
  }
}

export function transitionModelProvider<T extends ModelFormState>(
  form: T,
  provider: string,
  providerOptions?: ModelProviderOption[],
): T {
  const previousOption = getProviderCatalogEntry(form.provider, providerOptions)
  const nextOption = getProviderCatalogEntry(provider, providerOptions)
  const previousDefaultBase = normalizeApiBase(
    getProviderDefaultAPIBase(form.provider, providerOptions),
  )
  const nextDefaultBase = normalizeApiBase(
    getProviderDefaultAPIBase(provider, providerOptions),
  )
  const currentApiBase = normalizeApiBase(form.apiBase)
  let authMethod = form.authMethod
  let apiBase = form.apiBase

  if (nextOption?.authMethodLocked) {
    authMethod = nextOption.defaultAuthMethod ?? ""
  } else if (
    previousOption?.authMethodLocked &&
    form.authMethod === (previousOption.defaultAuthMethod ?? "")
  ) {
    authMethod = ""
  }
  if (
    currentApiBase &&
    previousDefaultBase &&
    currentApiBase === previousDefaultBase &&
    currentApiBase !== nextDefaultBase
  ) {
    apiBase = ""
  }

  return {
    ...form,
    provider: getCanonicalProviderKey(provider, providerOptions),
    apiBase,
    authMethod,
  }
}

type ModelFormFields = Omit<ModelSaveRequest, "model_name" | "enabled">

export type ModelFormProjection =
  | { ok: true; value: ModelFormFields }
  | { ok: false; field: "extraBody" | "customHeaders" }

export function projectModelForm(
  form: ModelFormState,
  providerOptions?: ModelProviderOption[],
  previousStreamingEnabled?: boolean,
): ModelFormProjection {
  const extraBody = parseJSONObject(form.extraBody)
  if (extraBody === undefined) {
    return { ok: false, field: "extraBody" }
  }
  const customHeaders = parseStringRecord(form.customHeaders)
  if (customHeaders === undefined) {
    return { ok: false, field: "customHeaders" }
  }

  const authMethodLocked = isProviderAuthMethodLocked(
    form.provider,
    providerOptions,
  )
  const defaultAuthMethod = getProviderDefaultAuthMethod(
    form.provider,
    providerOptions,
  )
  const streaming =
    previousStreamingEnabled === true || form.streamingEnabled
      ? { enabled: form.streamingEnabled }
      : undefined

  return {
    ok: true,
    value: {
      provider:
        getCanonicalProviderKey(form.provider, providerOptions) || undefined,
      model: form.modelId.trim(),
      api_base: getSubmittedAPIBase(form.apiBase),
      api_key: form.apiKey.trim() || undefined,
      proxy: form.proxy.trim() || undefined,
      auth_method: authMethodLocked
        ? defaultAuthMethod || undefined
        : form.authMethod.trim() || undefined,
      connect_mode: form.connectMode.trim() || undefined,
      workspace: form.workspace.trim() || undefined,
      rpm: form.rpm ? Number(form.rpm) : undefined,
      max_tokens_field: form.maxTokensField.trim() || undefined,
      request_timeout: form.requestTimeout
        ? Number(form.requestTimeout)
        : undefined,
      thinking_level: form.thinkingLevel.trim() || undefined,
      tool_schema_transform: form.toolSchemaTransform.trim() || undefined,
      streaming,
      extra_body: extraBody,
      custom_headers: customHeaders,
    },
  }
}

function parseJSONObject(value: string): Record<string, unknown> | undefined {
  if (!value.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(value)
    if (
      parsed === null ||
      Array.isArray(parsed) ||
      typeof parsed !== "object"
    ) {
      return undefined
    }
    return parsed as Record<string, unknown>
  } catch {
    return undefined
  }
}

function parseStringRecord(value: string): Record<string, string> | undefined {
  const parsed = parseJSONObject(value)
  if (
    parsed === undefined ||
    Object.values(parsed).some((entry) => typeof entry !== "string")
  ) {
    return undefined
  }
  return parsed as Record<string, string>
}
