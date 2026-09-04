import { IconLoader2 } from "@tabler/icons-react"
import { type ChangeEvent, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelProviderOption,
  addModel,
  setDefaultModel,
} from "@/api/models"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import { Field } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
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
  type ModelFormState,
  projectModelForm,
  validateModelForm,
} from "./model-form"
import { ModelFormFields } from "./model-form-fields"
import { TestModelDialog } from "./test-model-dialog"
import { useModelFormController } from "./use-model-form-controller"

interface AddModelSheetProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  existingModelNames: string[]
  providerOptions?: ModelProviderOption[]
}

export function AddModelSheet({
  open,
  onClose,
  onSaved,
  existingModelNames,
  providerOptions,
}: AddModelSheetProps) {
  const { t } = useTranslation()
  const [modelName, setModelName] = useState("")
  const [saving, setSaving] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<keyof ModelFormState | "modelName", string>>
  >({})
  const [serverError, setServerError] = useState("")
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
    initialForm: EMPTY_MODEL_FORM,
    initialDefault: false,
    active: open,
    resetKey: open,
    providerOptions,
    onFieldChange: (field) => {
      setFieldErrors((current) =>
        current[field] ? { ...current, [field]: undefined } : current,
      )
    },
  })
  const apiKeyPlaceholder = maskedSecretPlaceholder(
    form.apiKey,
    t("models.field.apiKeyPlaceholder"),
  )
  const isDirty =
    modelName !== "" ||
    JSON.stringify(form) !== JSON.stringify(EMPTY_MODEL_FORM) ||
    setAsDefault

  useEffect(() => {
    if (!open) return
    setModelName("")
    setFieldErrors({})
    setServerError("")
  }, [open])

  const validate = (): boolean => {
    const errors: Partial<Record<keyof ModelFormState | "modelName", string>> =
      {}
    const shared = validateModelForm(form, providerOptions, modelValidation)
    const trimmedModelName = modelName.trim()
    if (!trimmedModelName) {
      errors.modelName = t("models.add.errorRequired")
    } else if (
      existingModelNames.some((name) => name.trim() === trimmedModelName)
    ) {
      errors.modelName = t("models.add.errorDuplicateModelName")
    }
    if (shared.provider) {
      errors.provider = t("models.field.providerInvalid")
    }
    if (shared.modelId === "required") {
      errors.modelId = t("models.add.errorRequired")
    } else if (shared.modelId === "invalid" && modelValidation) {
      errors.modelId = t(
        modelValidation.messageKey,
        modelValidation.messageParams,
      )
    }
    setFieldErrors(errors)
    return Object.keys(errors).length === 0
  }

  const handleModelNameChange = (event: ChangeEvent<HTMLInputElement>) => {
    setModelName(event.target.value)
    if (fieldErrors.modelName) {
      setFieldErrors((current) => ({ ...current, modelName: undefined }))
    }
  }

  const handleSave = async () => {
    if (!validate()) return

    const projection = projectModelForm(form, providerOptions)
    if (!projection.ok) {
      const label =
        projection.field === "extraBody"
          ? t("models.field.extraBody")
          : t("models.field.customHeaders")
      setServerError(label + ": " + t("models.field.invalidJson"))
      return
    }

    setSaving(true)
    setServerError("")
    try {
      const savedModelName = modelName.trim()
      await addModel({
        model_name: savedModelName,
        enabled: true,
        ...projection.value,
      })
      if (setAsDefault) await setDefaultModel(savedModelName)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.add.saveSuccess"),
        savedModelName,
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (error) {
      setServerError(
        error instanceof Error ? error.message : t("models.add.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(value) => !value && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">{t("models.add.title")}</SheetTitle>
          <SheetDescription className="text-xs">
            {t("models.add.description")}
          </SheetDescription>
        </SheetHeader>

        <div
          className="min-h-0 flex-1 overflow-y-auto"
          ref={scrollContainerRef}
        >
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("models.add.modelName")}
              hint={t("models.add.modelNameHint")}
            >
              <Input
                value={modelName}
                onChange={handleModelNameChange}
                placeholder={t("models.add.modelNamePlaceholder")}
                aria-invalid={Boolean(fieldErrors.modelName)}
              />
              {fieldErrors.modelName && (
                <p className="text-destructive text-xs">
                  {fieldErrors.modelName}
                </p>
              )}
            </Field>

            <ModelFormFields
              form={form}
              providerOptions={providerOptions}
              scrollContainerRef={scrollContainerRef}
              modelValidation={modelValidation}
              catalogModels={catalogModels}
              fetchedModels={fetchedModels}
              providerError={fieldErrors.provider}
              modelIdError={fieldErrors.modelId}
              apiKeyPlaceholder={apiKeyPlaceholder}
              setAsDefault={setAsDefault}
              filterCreateAllowed
              showSelectProviderFirst
              hideDefaultUnsupportedUntilProvider
              testDisabled={!form.provider || !form.modelId}
              error={serverError}
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
            disabled={!isDirty || saving || modelValidation?.level === "error"}
          >
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("models.add.confirm")}
          </Button>
        </SheetFooter>
      </SheetContent>

      <FetchModelsDialog
        open={fetchOpen}
        onClose={() => setFetchOpen(false)}
        onFill={fillFetchedModels}
        provider={canonicalProvider}
        apiKey={form.apiKey}
        apiBase={effectiveApiBase}
        backendOptions={providerOptions}
      />

      <TestModelDialog
        model={null}
        open={testOpen}
        onClose={() => setTestOpen(false)}
        inlineParams={{
          provider: canonicalProvider,
          model: form.modelId,
          apiBase: effectiveApiBase,
          apiKey: form.apiKey,
          authMethod: effectiveAuthMethod,
        }}
      />
    </Sheet>
  )
}
