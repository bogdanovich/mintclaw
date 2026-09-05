import { IconDownload, IconPlugConnected } from "@tabler/icons-react"
import type { ChangeEvent, RefObject } from "react"
import { useTranslation } from "react-i18next"

import type { ModelProviderOption } from "@/api/models"
import {
  AdvancedSection,
  Field,
  KeyInput,
  SwitchCardField,
} from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

import type { ModelFormState } from "./model-form"
import type { FieldValidation } from "./model-validation"
import { ProviderCombobox } from "./provider-combobox"
import {
  getCanonicalProviderKey,
  getProviderCatalogMap,
  getProviderDefaultAPIBase,
  getProviderDefaultAuthMethod,
  isProviderAuthMethodLocked,
  providerSupportsFetch,
} from "./provider-registry"

interface ModelFormFieldsProps {
  form: ModelFormState
  providerOptions?: ModelProviderOption[]
  scrollContainerRef: RefObject<HTMLDivElement | null>
  modelValidation: FieldValidation | null
  catalogModels: string[]
  fetchedModels: string[]
  providerError?: string
  modelIdError?: string
  modelIdInvalid?: boolean
  apiKeyPlaceholder: string
  apiKeyHint?: string
  setAsDefault: boolean
  filterCreateAllowed?: boolean
  showSelectProviderFirst?: boolean
  hideDefaultUnsupportedUntilProvider?: boolean
  testDisabled: boolean
  error?: string
  onFieldChange: (
    field: keyof ModelFormState,
    value: ModelFormState[keyof ModelFormState],
  ) => void
  onModelChange: (value: string) => void
  onProviderChange: (provider: string) => void
  onApplyModelFix: () => void
  onModelSelect: (model: string) => void
  onFetchModels: () => void
  onTestModel: () => void
  onSetAsDefault: (checked: boolean) => void
}

export function ModelFormFields({
  form,
  providerOptions,
  scrollContainerRef,
  modelValidation,
  catalogModels,
  fetchedModels,
  providerError,
  modelIdError,
  modelIdInvalid = false,
  apiKeyPlaceholder,
  apiKeyHint,
  setAsDefault,
  filterCreateAllowed = false,
  showSelectProviderFirst = false,
  hideDefaultUnsupportedUntilProvider = false,
  testDisabled,
  error,
  onFieldChange,
  onModelChange,
  onProviderChange,
  onApplyModelFix,
  onModelSelect,
  onFetchModels,
  onTestModel,
  onSetAsDefault,
}: ModelFormFieldsProps) {
  const { t } = useTranslation()
  const canonicalProvider = getCanonicalProviderKey(
    form.provider,
    providerOptions,
  )
  const providerDef = canonicalProvider
    ? getProviderCatalogMap(providerOptions).get(canonicalProvider)
    : undefined
  const commonModels = providerDef?.commonModels || []
  const authMethodLocked = isProviderAuthMethodLocked(
    form.provider,
    providerOptions,
  )
  const defaultAuthMethod = getProviderDefaultAuthMethod(
    form.provider,
    providerOptions,
  )
  const effectiveAuthMethod = (
    authMethodLocked ? defaultAuthMethod : form.authMethod
  )
    .trim()
    .toLowerCase()
  const isOAuth = effectiveAuthMethod === "oauth"
  const defaultModelAllowed = providerDef?.defaultModelAllowed === true
  const apiBasePlaceholder =
    getProviderDefaultAPIBase(form.provider, providerOptions) ||
    "https://api.example.com/v1"
  const setField =
    (field: keyof ModelFormState) =>
    (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
      onFieldChange(field, event.target.value)

  return (
    <>
      <Field
        label={t("models.field.provider")}
        hint={t("models.field.providerHint")}
        error={providerError}
        required
      >
        <ProviderCombobox
          value={form.provider}
          onChange={onProviderChange}
          placeholder={t("models.field.providerPlaceholder")}
          backendOptions={providerOptions}
          filterCreateAllowed={filterCreateAllowed}
          containerRef={scrollContainerRef}
        />
      </Field>

      <Field label={t("models.add.modelId")} hint={t("models.add.modelIdHint")}>
        <Input
          value={form.modelId}
          onChange={(event) => onModelChange(event.target.value)}
          placeholder={
            providerDef
              ? `${commonModels[0] || "model-name"}`
              : t("models.add.modelIdPlaceholder")
          }
          className="font-mono text-sm"
          aria-invalid={
            modelIdInvalid ||
            Boolean(modelIdError) ||
            modelValidation?.level === "error"
          }
        />
        {modelValidation && modelValidation.messageKey && (
          <div
            className={`flex items-center gap-2 text-xs ${
              modelValidation.level === "error"
                ? "text-destructive"
                : modelValidation.level === "warning"
                  ? "text-yellow-600 dark:text-yellow-500"
                  : "text-green-600 dark:text-green-500"
            }`}
          >
            <span>
              {t(modelValidation.messageKey, modelValidation.messageParams)}
            </span>
            {modelValidation.fix && (
              <button
                type="button"
                onClick={onApplyModelFix}
                className="text-primary underline hover:no-underline"
              >
                {t("common.fix")}
              </button>
            )}
          </div>
        )}
        {modelIdError && !modelValidation && (
          <p className="text-destructive text-xs">{modelIdError}</p>
        )}
        {commonModels.length > 0 && (
          <ModelBadges
            models={commonModels}
            selected={form.modelId}
            suggested
            onSelect={onModelSelect}
          />
        )}
        {catalogModels.length > 0 && (
          <ModelBadges
            models={catalogModels}
            selected={form.modelId}
            onSelect={onModelSelect}
          />
        )}
        {fetchedModels.length > 0 && (
          <ModelBadges
            models={fetchedModels}
            selected={form.modelId}
            onSelect={onModelSelect}
          />
        )}
        <div className="flex items-center gap-2">
          {providerSupportsFetch(form.provider, providerOptions) && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-xs"
              onClick={onFetchModels}
            >
              <IconDownload className="size-3" />
              {t("models.fetch.title")}
            </Button>
          )}
          {showSelectProviderFirst && !form.provider && (
            <span className="text-muted-foreground text-xs">
              {t("models.field.selectProviderFirst")}
            </span>
          )}
        </div>
      </Field>

      {!isOAuth && (
        <Field label={t("models.field.apiKey")} hint={apiKeyHint}>
          <KeyInput
            value={form.apiKey}
            onChange={(value) => onFieldChange("apiKey", value)}
            placeholder={apiKeyPlaceholder}
          />
        </Field>
      )}

      <Field
        label={t("models.field.apiBase")}
        hint={isOAuth ? t("models.edit.oauthNote") : undefined}
      >
        <Input
          value={form.apiBase}
          onChange={setField("apiBase")}
          placeholder={apiBasePlaceholder}
          disabled={isOAuth}
        />
      </Field>

      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={onTestModel}
          disabled={testDisabled}
        >
          <IconPlugConnected className="size-4" />
          {t("models.test.testConnection")}
        </Button>
      </div>

      <SwitchCardField
        label={t("models.defaultOnSave.label")}
        hint={
          !defaultModelAllowed &&
          (!hideDefaultUnsupportedUntilProvider || form.provider)
            ? t("models.defaultOnSave.unsupportedProvider")
            : t("models.defaultOnSave.description")
        }
        checked={setAsDefault}
        onCheckedChange={onSetAsDefault}
        disabled={!defaultModelAllowed}
      />

      <AdvancedSection>
        <Field
          label={t("models.field.proxy")}
          hint={t("models.field.proxyHint")}
        >
          <Input
            value={form.proxy}
            onChange={setField("proxy")}
            placeholder="http://127.0.0.1:7890"
          />
        </Field>

        <Field
          label={t("models.field.authMethod")}
          hint={
            authMethodLocked
              ? t("models.field.authMethodManagedHint")
              : t("models.field.authMethodHint")
          }
        >
          <Input
            value={authMethodLocked ? defaultAuthMethod : form.authMethod}
            onChange={setField("authMethod")}
            placeholder="oauth"
            disabled={authMethodLocked}
          />
        </Field>

        <Field
          label={t("models.field.workspace")}
          hint={t("models.field.workspaceHint")}
        >
          <Input
            value={form.workspace}
            onChange={setField("workspace")}
            placeholder="/path/to/workspace"
          />
        </Field>

        <Field
          label={t("models.field.requestTimeout")}
          hint={t("models.field.requestTimeoutHint")}
        >
          <Input
            value={form.requestTimeout}
            onChange={setField("requestTimeout")}
            placeholder="60"
            type="number"
            min={0}
          />
        </Field>

        <Field label={t("models.field.rpm")} hint={t("models.field.rpmHint")}>
          <Input
            value={form.rpm}
            onChange={setField("rpm")}
            placeholder="60"
            type="number"
            min={0}
          />
        </Field>

        <Field
          label={t("models.field.thinkingLevel")}
          hint={t("models.field.thinkingLevelHint")}
        >
          <Input
            value={form.thinkingLevel}
            onChange={setField("thinkingLevel")}
            placeholder={t("models.field.providerDefault")}
          />
        </Field>

        <Field
          label={t("models.field.maxTokensField")}
          hint={t("models.field.maxTokensFieldHint")}
        >
          <Input
            value={form.maxTokensField}
            onChange={setField("maxTokensField")}
            placeholder="max_completion_tokens"
          />
        </Field>

        <Field
          label={t("models.field.toolSchemaTransform")}
          hint={t("models.field.toolSchemaTransformHint")}
        >
          <Input
            value={form.toolSchemaTransform}
            onChange={setField("toolSchemaTransform")}
            placeholder="google"
          />
        </Field>

        <SwitchCardField
          label={t("models.field.streamingEnabled")}
          hint={t("models.field.streamingEnabledHint")}
          checked={form.streamingEnabled}
          onCheckedChange={(checked) =>
            onFieldChange("streamingEnabled", checked)
          }
          ariaLabel={t("models.field.streamingEnabled")}
        />

        <Field
          label={t("models.field.extraBody")}
          hint={t("models.field.extraBodyHint")}
        >
          <Textarea
            value={form.extraBody}
            onChange={setField("extraBody")}
            placeholder='{"key": "value"}'
            rows={3}
          />
        </Field>

        <Field
          label={t("models.field.customHeaders")}
          hint={t("models.field.customHeadersHint")}
        >
          <Textarea
            value={form.customHeaders}
            onChange={setField("customHeaders")}
            placeholder='{"X-Source": "coding-plan"}'
            rows={3}
          />
        </Field>
      </AdvancedSection>

      {error && (
        <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
          {error}
        </p>
      )}
    </>
  )
}

function ModelBadges({
  models,
  selected,
  suggested = false,
  onSelect,
}: {
  models: string[]
  selected: string
  suggested?: boolean
  onSelect: (model: string) => void
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {models.map((model) => (
        <Badge
          key={model}
          variant={
            suggested ? "secondary" : selected === model ? "default" : "outline"
          }
          className={
            suggested
              ? "hover:bg-secondary/80 cursor-pointer font-mono text-xs"
              : "cursor-pointer font-mono text-xs"
          }
          onClick={() => onSelect(model)}
        >
          {model}
        </Badge>
      ))}
    </div>
  )
}
