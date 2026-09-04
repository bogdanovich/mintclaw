import { IconLoader2 } from "@tabler/icons-react"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelInfo,
  type ModelProviderOption,
  setDefaultModel,
  updateModel,
} from "@/api/models"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { FetchModelsDialog } from "./fetch-models-dialog"
import {
  EMPTY_MODEL_FORM,
  modelToForm,
  projectModelForm,
  validateModelForm,
} from "./model-form"
import { ModelFormFields } from "./model-form-fields"
import { getProviderCatalogEntry } from "./provider-registry"
import { TestModelDialog } from "./test-model-dialog"
import { useModelFormController } from "./use-model-form-controller"

interface EditModelSheetProps {
  model: ModelInfo | null
  open: boolean
  onClose: () => void
  onSaved: () => void
  providerOptions?: ModelProviderOption[]
}

export function EditModelSheet({
  model,
  open,
  onClose,
  onSaved,
  providerOptions,
}: EditModelSheetProps) {
  const { t } = useTranslation()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const initialForm = useMemo(
    () => (model ? modelToForm(model, providerOptions) : EMPTY_MODEL_FORM),
    [model, providerOptions],
  )
  const {
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
  } = useModelFormController({
    initialForm,
    initialDefault: model?.is_default ?? false,
    active: open && model !== null,
    resetKey: model?.index,
    providerOptions,
    onFieldChange: () => setError(""),
  })
  const isDirty =
    model != null &&
    (JSON.stringify(form) !== JSON.stringify(initialForm) ||
      setAsDefault !== model.is_default)
  const providerError =
    form.provider && !getProviderCatalogEntry(form.provider, providerOptions)
      ? t("models.field.providerInvalid")
      : undefined
  const hasSavedAPIKey = Boolean(model?.api_key)
  const apiKeyPlaceholder = hasSavedAPIKey
    ? maskedSecretPlaceholder(
        model?.api_key ?? "",
        t("models.field.apiKeyPlaceholderSet"),
      )
    : t("models.field.apiKeyPlaceholder")

  const handleSave = async () => {
    if (!model) return
    const validation = validateModelForm(form, providerOptions, modelValidation)
    if (validation.provider) {
      setError(t("models.field.providerInvalid"))
      return
    }
    if (validation.modelId === "required") {
      setError(t("models.add.errorRequired"))
      return
    }
    if (validation.modelId === "invalid") return

    const projection = projectModelForm(
      form,
      providerOptions,
      model.streaming?.enabled,
    )
    if (!projection.ok) {
      const label =
        projection.field === "extraBody"
          ? t("models.field.extraBody")
          : t("models.field.customHeaders")
      setError(label + ": " + t("models.field.invalidJson"))
      return
    }

    setSaving(true)
    setError("")
    try {
      await updateModel(model.index, {
        model_name: model.model_name,
        enabled: model.enabled,
        ...projection.value,
      })
      if (setAsDefault && !model.is_default) {
        await setDefaultModel(model.model_name)
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.edit.saveSuccess"),
        model.model_name,
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (saveError) {
      setError(
        saveError instanceof Error
          ? saveError.message
          : t("models.edit.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <Sheet open={open} onOpenChange={(value) => !value && onClose()}>
        <SheetContent
          side="right"
          className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
        >
          <SheetHeader className="border-b-muted border-b px-6 py-5">
            <SheetTitle className="text-base">
              {t("models.edit.title", { name: model?.model_name })}
            </SheetTitle>
            <SheetDescription className="font-mono text-xs">
              {model?.model}
            </SheetDescription>
          </SheetHeader>

          <div
            className="min-h-0 flex-1 overflow-y-auto"
            ref={scrollContainerRef}
          >
            <div className="space-y-5 px-6 py-5">
              <ModelFormFields
                form={form}
                providerOptions={providerOptions}
                scrollContainerRef={scrollContainerRef}
                modelValidation={modelValidation}
                catalogModels={catalogModels}
                fetchedModels={fetchedModels}
                providerError={providerError}
                modelIdInvalid={Boolean(error)}
                apiKeyPlaceholder={apiKeyPlaceholder}
                apiKeyHint={
                  hasSavedAPIKey ? t("models.edit.apiKeyHint") : undefined
                }
                setAsDefault={setAsDefault}
                testDisabled={!model}
                error={error}
                onFieldChange={setField}
                onModelChange={changeModel}
                onProviderChange={changeProvider}
                onApplyModelFix={applyModelFix}
                onModelSelect={selectModel}
                onFetchModels={() => setFetchOpen(true)}
                onTestModel={() => setTestOpen(true)}
                onSetAsDefault={setSetAsDefault}
              />
            </div>
          </div>

          <SheetFooter className="border-t-muted border-t px-6 py-4">
            {isDirty && (
              <ConfigChangeNotice
                kind="save"
                title={t("common.saveChangesTitle")}
                description={t("models.unsavedPrompt")}
              />
            )}
            <Button variant="ghost" onClick={onClose} disabled={saving}>
              {t("common.cancel")}
            </Button>
            <Button
              onClick={handleSave}
              disabled={
                !isDirty || saving || modelValidation?.level === "error"
              }
            >
              {saving && <IconLoader2 className="size-4 animate-spin" />}
              {t("common.save")}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <TestModelDialog
        model={model}
        open={testOpen}
        onClose={() => setTestOpen(false)}
        inlineParams={{
          provider: canonicalProvider,
          model: form.modelId,
          apiBase: effectiveApiBase,
          apiKey: form.apiKey,
          authMethod: effectiveAuthMethod,
          modelIndex: model?.index,
        }}
      />

      <FetchModelsDialog
        open={fetchOpen}
        onClose={() => setFetchOpen(false)}
        onFill={fillFetchedModels}
        provider={canonicalProvider}
        apiKey={form.apiKey}
        apiBase={effectiveApiBase}
        modelIndex={model?.index}
        backendOptions={providerOptions}
      />
    </>
  )
}
