export type AssistantDetailVisibility =
  | "none"
  | "thought"
  | "tool_calls"
  | "all"

export type AssistantDetailMessageKind =
  | "normal"
  | "thought"
  | "tool_calls"
  | undefined

interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export const ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY =
  "mintclaw:chat-assistant-detail-visibility"
export const DEFAULT_ASSISTANT_DETAIL_VISIBILITY: AssistantDetailVisibility =
  "all"

function getSafeLocalStorage(): StorageLike | undefined {
  try {
    return globalThis.localStorage
  } catch {
    return undefined
  }
}

function serializeAssistantDetailVisibility(
  value: AssistantDetailVisibility,
): string {
  return JSON.stringify(value)
}

function parseStoredValue(rawValue: string | null): unknown {
  if (rawValue === null) {
    return undefined
  }

  try {
    return JSON.parse(rawValue)
  } catch {
    return rawValue.trim()
  }
}

function parseAssistantDetailVisibility(
  rawValue: unknown,
): AssistantDetailVisibility | undefined {
  if (typeof rawValue !== "string") {
    return undefined
  }

  const normalized = rawValue.trim().toLowerCase()
  if (
    normalized === "none" ||
    normalized === "thought" ||
    normalized === "tool_calls" ||
    normalized === "all"
  ) {
    return normalized
  }

  return undefined
}

function syncAssistantDetailVisibilityStorage(
  storage?: StorageLike,
): AssistantDetailVisibility {
  const resolvedStorage = storage ?? getSafeLocalStorage()
  if (!resolvedStorage) {
    return DEFAULT_ASSISTANT_DETAIL_VISIBILITY
  }

  let storedValue: string | null
  try {
    storedValue = resolvedStorage.getItem(
      ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
    )
  } catch {
    return DEFAULT_ASSISTANT_DETAIL_VISIBILITY
  }

  const value = parseAssistantDetailVisibility(parseStoredValue(storedValue))
  if (value) {
    if (storedValue === serializeAssistantDetailVisibility(value)) {
      return value
    }
    try {
      resolvedStorage.setItem(
        ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
        serializeAssistantDetailVisibility(value),
      )
    } catch {
      // Ignore storage write failures and keep the parsed preference value.
    }
    return value
  }

  if (storedValue !== null) {
    try {
      resolvedStorage.removeItem(ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY)
    } catch {
      // Ignore cleanup failures and keep the parsed preference value.
    }
  }

  return DEFAULT_ASSISTANT_DETAIL_VISIBILITY
}

export const assistantDetailVisibilityStorage = {
  getItem(): AssistantDetailVisibility {
    return syncAssistantDetailVisibilityStorage()
  },
  setItem(key: string, newValue: AssistantDetailVisibility) {
    const storage = getSafeLocalStorage()
    if (!storage) {
      return
    }

    try {
      storage.setItem(key, serializeAssistantDetailVisibility(newValue))
    } catch {
      // Ignore storage write failures and keep the in-memory atom state.
    }
  },
  removeItem(key: string) {
    const storage = getSafeLocalStorage()
    if (!storage) {
      return
    }

    try {
      storage.removeItem(key)
    } catch {
      // Ignore storage write failures and keep the in-memory atom state.
    }
  },
  subscribe(key: string, callback: (value: AssistantDetailVisibility) => void) {
    if (
      typeof window === "undefined" ||
      typeof window.addEventListener !== "function"
    ) {
      return undefined
    }

    const handleStorage = (event: StorageEvent) => {
      const storage = getSafeLocalStorage()
      if (!storage || event.storageArea !== storage || event.key !== key) {
        return
      }

      callback(syncAssistantDetailVisibilityStorage(storage))
    }

    window.addEventListener("storage", handleStorage)
    return () => window.removeEventListener("storage", handleStorage)
  },
}

export function shouldShowAssistantMessage(
  visibility: AssistantDetailVisibility,
  kind: AssistantDetailMessageKind,
): boolean {
  if (kind !== "thought" && kind !== "tool_calls") {
    return true
  }

  if (visibility === "all") {
    return true
  }

  if (visibility === "none") {
    return false
  }

  return visibility === kind
}
