import { IconAdjustments } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { launcherFetch } from "@/api/http"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

class ConfigConflictError extends Error {
  constructor() {
    super(
      "Configuration changed since this draft was loaded. The draft was kept.",
    )
  }
}

export function RawConfigPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editorValue, setEditorValue] = useState("")
  const [editorRevision, setEditorRevision] = useState<string | null>(null)
  const [isDirty, setIsDirty] = useState(false)

  const { data: configSnapshot, isLoading } = useQuery({
    queryKey: ["config", "raw"],
    queryFn: async () => {
      const res = await launcherFetch("/api/config")
      if (!res.ok) {
        throw new Error("Failed to fetch config")
      }
      return {
        document: (await res.json()) as Record<string, unknown>,
        revision: res.headers.get("ETag"),
      }
    },
  })

  const mutation = useMutation({
    mutationFn: async ({
      document,
      revision,
    }: {
      document: string
      revision: string | null
    }) => {
      const res = await launcherFetch("/api/config", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "If-Match": revision ?? "",
        },
        body: document,
      })
      if (res.status === 412) {
        throw new ConfigConflictError()
      }
      if (!res.ok) {
        throw new Error("Failed to save config")
      }
      return { revision: res.headers.get("ETag") }
    },
    onSuccess: ({ revision }, submitted) => {
      try {
        const savedConfig = JSON.parse(submitted.document) as Record<
          string,
          unknown
        >
        queryClient.setQueryData(["config", "raw"], {
          document: savedConfig,
          revision,
        })
        setEditorValue("")
        setEditorRevision(null)
        setIsDirty(false)
        queryClient.invalidateQueries({ queryKey: ["config"] })
      } catch {
        queryClient.invalidateQueries({ queryKey: ["config"] })
      }
      void refreshGatewayState({ force: true }).then((gateway) => {
        showSaveSuccessOrRestartToast(
          t,
          t("pages.config.save_success"),
          t("navigation.config"),
          gateway?.restartRequired === true,
        )
      })
    },
    onError: (error) => {
      if (error instanceof ConfigConflictError) {
        queryClient.invalidateQueries({ queryKey: ["config"] })
        toast.error(error.message)
        return
      }
      toast.error(t("pages.config.save_error"))
    },
  })

  const effectiveEditorValue =
    editorValue ||
    (configSnapshot ? JSON.stringify(configSnapshot.document, null, 2) : "")

  const handleSave = () => {
    try {
      JSON.parse(effectiveEditorValue)
      mutation.mutate({
        document: effectiveEditorValue,
        revision: isDirty ? editorRevision : (configSnapshot?.revision ?? null),
      })
    } catch (error) {
      toast.error(
        t(
          "pages.config.invalid_json",
          error instanceof Error ? error.message : "Invalid JSON format.",
        ),
      )
    }
  }

  const handleFormat = () => {
    try {
      const formatted = JSON.stringify(
        JSON.parse(effectiveEditorValue),
        null,
        2,
      )
      if (!isDirty) {
        setEditorRevision(configSnapshot?.revision ?? null)
      }
      setEditorValue(formatted)
      setIsDirty(true)
      toast.success(t("pages.config.format_success"))
    } catch (error) {
      toast.error(
        t(
          "pages.config.format_error",
          error instanceof Error ? error.message : "Invalid JSON format.",
        ),
      )
    }
  }

  const [showResetDialog, setShowResetDialog] = useState(false)

  const confirmReset = () => {
    setEditorValue("")
    setEditorRevision(null)
    setIsDirty(false)
    toast.info(t("pages.config.reset_success"))
    setShowResetDialog(false)
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("pages.config.raw_json_title")}>
        <Button variant="outline" asChild>
          <Link to="/config">
            <IconAdjustments className="size-4" />
            {t("pages.config.back_to_visual")}
          </Link>
        </Button>
      </PageHeader>

      <div className="flex min-h-0 flex-1 flex-col p-1 lg:p-3 lg:p-6">
        <div className="mx-auto flex h-full min-h-0 w-full max-w-[1000px] flex-col">
          {isLoading ? (
            <div className="flex flex-1 items-center justify-center">
              <p>{t("labels.loading")}</p>
            </div>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col gap-3">
              {isDirty && (
                <ConfigChangeNotice
                  kind="save"
                  title={t("common.saveChangesTitle")}
                  description={t("pages.config.unsaved_changes")}
                  className="shrink-0"
                />
              )}
              <div className="relative min-h-0 flex-1 overflow-hidden rounded-lg border shadow-sm">
                <Textarea
                  value={effectiveEditorValue}
                  onChange={(e) => {
                    if (!isDirty) {
                      setEditorRevision(configSnapshot?.revision ?? null)
                    }
                    setEditorValue(e.target.value)
                    setIsDirty(true)
                  }}
                  wrap="off"
                  className="h-full min-h-0 resize-none overflow-auto border-0 bg-transparent px-4 py-3 font-mono text-sm [overflow-wrap:normal] whitespace-pre shadow-none focus-visible:ring-0"
                  placeholder={t("pages.config.json_placeholder")}
                />
              </div>
              <div className="flex shrink-0 justify-end gap-2">
                <Button
                  variant="outline"
                  onClick={handleFormat}
                  disabled={mutation.isPending}
                >
                  {t("pages.config.format")}
                </Button>
                <AlertDialog
                  open={showResetDialog}
                  onOpenChange={setShowResetDialog}
                >
                  <AlertDialogTrigger asChild>
                    <Button
                      variant="outline"
                      disabled={!isDirty}
                      onClick={() => setShowResetDialog(true)}
                    >
                      {t("common.reset")}
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>
                        {t("pages.config.reset_confirm_title")}
                      </AlertDialogTitle>
                      <AlertDialogDescription>
                        {t("pages.config.reset_confirm_desc")}
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>
                        {t("common.cancel")}
                      </AlertDialogCancel>
                      <AlertDialogAction onClick={confirmReset}>
                        {t("common.confirm")}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
                <Button onClick={handleSave} disabled={mutation.isPending}>
                  {mutation.isPending ? t("common.saving") : t("common.save")}
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
