import { toast } from "sonner"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { loadSessionMessages } from "@/features/chat/history"
import { type ChatMessage, getChatState, updateChatStore } from "@/store/chat"
import { updateGatewayStore } from "@/store/gateway"

import {
  initializeChatStore,
  newChatSession,
  switchChatSession,
  teardownChatStore,
} from "./controller"

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }))
vi.mock("@/features/chat/history", () => ({
  loadSessionMessages: vi.fn(),
  mergeHistoryMessages: (history: ChatMessage[], live: ChatMessage[]) => [
    ...history,
    ...live,
  ],
}))
vi.mock("@/features/chat/state", () => ({
  getInitialActiveSessionId: () => "initial",
  readStoredSessionId: () => "",
  writeStoredSessionId: vi.fn(),
  clearStoredSessionId: vi.fn(),
  generateSessionId: () => "new-session",
  normalizeUnixTimestamp: (timestamp: number) => timestamp,
}))

function messages(id: string): ChatMessage[] {
  return [{ id, role: "assistant", content: id, timestamp: 1 }]
}

function pendingHistory() {
  let resolve!: (value: ChatMessage[]) => void
  let reject!: (error: Error) => void
  const promise = new Promise<ChatMessage[]>(
    (resolvePromise, rejectPromise) => {
      resolve = resolvePromise
      reject = rejectPromise
    },
  )
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(loadSessionMessages).mockReset()
  updateChatStore({
    activeSessionId: "initial",
    messages: messages("initial"),
    hasHydratedActiveSession: true,
    connectionState: "disconnected",
    isTyping: false,
    contextUsage: undefined,
  })
  updateGatewayStore({ status: "stopped" })
  initializeChatStore()
  vi.spyOn(console, "error").mockImplementation(() => {})
})

afterEach(() => {
  teardownChatStore()
  vi.restoreAllMocks()
})

describe("chat history selection", () => {
  it("keeps the latest selection when history responses finish in reverse order", async () => {
    const older = pendingHistory()
    const newer = pendingHistory()
    vi.mocked(loadSessionMessages)
      .mockReturnValueOnce(older.promise)
      .mockReturnValueOnce(newer.promise)

    const first = switchChatSession("older")
    const second = switchChatSession("newer")
    newer.resolve(messages("newer"))
    await second
    older.resolve(messages("older"))
    await first

    expect(getChatState().activeSessionId).toBe("newer")
    expect(getChatState().messages).toEqual(messages("newer"))
  })

  it("keeps a newly created conversation after an older history request finishes", async () => {
    const history = pendingHistory()
    vi.mocked(loadSessionMessages).mockReturnValueOnce(history.promise)

    const selection = switchChatSession("older")
    await newChatSession()
    history.resolve(messages("older"))
    await selection

    expect(getChatState().activeSessionId).toBe("new-session")
    expect(getChatState().messages).toEqual([])
  })

  it("cancels a pending selection when New Chat keeps an already empty conversation", async () => {
    updateChatStore({ messages: [] })
    const history = pendingHistory()
    vi.mocked(loadSessionMessages).mockReturnValueOnce(history.promise)

    const selection = switchChatSession("older")
    await newChatSession()
    history.resolve(messages("older"))
    await selection

    expect(getChatState().activeSessionId).toBe("initial")
    expect(getChatState().messages).toEqual([])
  })

  it("cancels a pending selection when the current conversation is selected again", async () => {
    const history = pendingHistory()
    vi.mocked(loadSessionMessages).mockReturnValueOnce(history.promise)

    const selection = switchChatSession("older")
    await switchChatSession("initial")
    history.resolve(messages("older"))
    await selection

    expect(loadSessionMessages).toHaveBeenCalledTimes(1)
    expect(getChatState().activeSessionId).toBe("initial")
    expect(getChatState().messages).toEqual(messages("initial"))
  })

  it("does not apply an old history response after teardown and reinitialization", async () => {
    const history = pendingHistory()
    vi.mocked(loadSessionMessages).mockReturnValueOnce(history.promise)

    const selection = switchChatSession("older")
    teardownChatStore()
    initializeChatStore()
    history.resolve(messages("older"))
    await selection

    expect(getChatState().activeSessionId).toBe("initial")
    expect(getChatState().messages).toEqual(messages("initial"))
  })

  it("ignores an older rejection after the latest selection succeeds", async () => {
    const older = pendingHistory()
    vi.mocked(loadSessionMessages)
      .mockReturnValueOnce(older.promise)
      .mockResolvedValueOnce(messages("newer"))

    const first = switchChatSession("older")
    await switchChatSession("newer")
    older.reject(new Error("stale history error"))
    await first

    expect(getChatState().activeSessionId).toBe("newer")
    expect(toast.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it("reports the current selection failure without accepting an older response", async () => {
    const older = pendingHistory()
    vi.mocked(loadSessionMessages)
      .mockReturnValueOnce(older.promise)
      .mockRejectedValueOnce(new Error("current history error"))

    const first = switchChatSession("older")
    await switchChatSession("newer")
    older.resolve(messages("older"))
    await first

    expect(getChatState().activeSessionId).toBe("initial")
    expect(getChatState().messages).toEqual(messages("initial"))
    expect(toast.error).toHaveBeenCalledExactlyOnceWith(
      "chat.historyOpenFailed",
    )
  })
})
