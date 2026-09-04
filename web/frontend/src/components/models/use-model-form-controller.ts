import { useCallback, useEffect, useRef, useState } from "react"

import {
  type CatalogEntry,
  type ModelProviderOption,
  getCatalogs,
} from "@/api/models"

import { type ModelFormState, transitionModelProvider } from "./model-form"
import { getEffectiveAPIBase } from "./model-provider-form-shared"
import { type FieldValidation, validateModelField } from "./model-validation"
import {
  getCanonicalProviderKey,
  getProviderCatalogEntry,
  getProviderDefaultAuthMethod,
  isProviderAuthMethodLocked,
} from "./provider-registry"

interface UseModelFormControllerOptions {
  initialForm: ModelFormState
  initialDefault: boolean
  active: boolean
  resetKey: string | number | boolean | null | undefined
  providerOptions?: ModelProviderOption[]
  onFieldChange?: (field: keyof ModelFormState) => void
}

export function useModelFormController({
  initialForm,
  initialDefault,
  active,
  resetKey,
  providerOptions,
  onFieldChange,
}: UseModelFormControllerOptions) {
  const [form, setForm] = useState(initialForm)
  const [setAsDefault, setSetAsDefault] = useState(initialDefault)
  const [modelValidation, setModelValidation] =
    useState<FieldValidation | null>(null)
  const [testOpen, setTestOpen] = useState(false)
  const [fetchOpen, setFetchOpen] = useState(false)
  const [fetchedModels, setFetchedModels] = useState<string[]>([])
  const [catalogModels, setCatalogModels] = useState<string[]>([])
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const scrollContainerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!active) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    setForm(initialForm)
    setSetAsDefault(initialDefault)
    setModelValidation(null)
    setFetchedModels([])
    setCatalogModels([])
    setTestOpen(false)
    setFetchOpen(false)
  }, [active, initialDefault, initialForm, resetKey])

  useEffect(() => {
    if (!active || !form.provider.trim()) {
      setCatalogModels([])
      return
    }

    const provider = getCanonicalProviderKey(form.provider, providerOptions)
    const apiBase = getEffectiveAPIBase(
      form.provider,
      form.apiBase,
      providerOptions,
    )
    setCatalogModels([])
    let cancelled = false
    getCatalogs()
      .then((response) => {
        if (cancelled) return
        setCatalogModels(
          selectCatalogModelIds(
            response.entries || [],
            provider,
            apiBase,
            providerOptions,
          ),
        )
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [active, form.apiBase, form.provider, providerOptions])

  useEffect(
    () => () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    },
    [],
  )

  const validateModel = useCallback(
    (value: string, provider: string) => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        setModelValidation(
          validateModelField(value, provider || undefined, providerOptions),
        )
      }, 300)
    },
    [providerOptions],
  )

  const setField = (
    field: keyof ModelFormState,
    value: ModelFormState[keyof ModelFormState],
  ) => {
    setForm((current) => ({ ...current, [field]: value }))
    onFieldChange?.(field)
  }

  const changeModel = (modelId: string) => {
    setField("modelId", modelId)
    validateModel(modelId, form.provider)
  }

  const changeProvider = (provider: string) => {
    setForm((current) =>
      transitionModelProvider(current, provider, providerOptions),
    )
    if (form.modelId) validateModel(form.modelId, provider)
    const defaultAllowed =
      getProviderCatalogEntry(provider, providerOptions)?.defaultModelAllowed ??
      false
    if (!defaultAllowed) setSetAsDefault(false)
    onFieldChange?.("provider")
  }

  const applyModelFix = () => {
    if (!modelValidation?.fix) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    setField("modelId", modelValidation.fix)
    setModelValidation(null)
  }

  const selectModel = (modelId: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    setField("modelId", modelId)
    setModelValidation(null)
  }

  const fillFetchedModels = (models: string[]) => {
    setFetchedModels(models)
    if (models.length > 0) selectModel(models[0])
  }

  const canonicalProvider = getCanonicalProviderKey(
    form.provider,
    providerOptions,
  )
  const effectiveAuthMethod = (
    isProviderAuthMethodLocked(form.provider, providerOptions)
      ? getProviderDefaultAuthMethod(form.provider, providerOptions)
      : form.authMethod
  )
    .trim()
    .toLowerCase()
  const effectiveApiBase = getEffectiveAPIBase(
    form.provider,
    form.apiBase,
    providerOptions,
  )

  return {
    form,
    setAsDefault,
    setSetAsDefault,
    modelValidation,
    fetchOpen,
    setFetchOpen,
    testOpen,
    setTestOpen,
    fetchedModels,
    catalogModels,
    scrollContainerRef,
    setField,
    changeModel,
    changeProvider,
    applyModelFix,
    selectModel,
    fillFetchedModels,
    canonicalProvider,
    effectiveAuthMethod,
    effectiveApiBase,
  }
}

export function selectCatalogModelIds(
  entries: CatalogEntry[],
  canonicalProvider: string,
  normalizedApiBase: string,
  providerOptions?: ModelProviderOption[],
): string[] {
  const modelIds = entries
    .filter((entry) => {
      const provider = getCanonicalProviderKey(entry.provider, providerOptions)
      const apiBase = (entry.api_base ?? "").trim().replace(/\/+$/, "")
      return provider === canonicalProvider && apiBase === normalizedApiBase
    })
    .flatMap((entry) => entry.models.map((model) => model.id))
  return [...new Set(modelIds)]
}
