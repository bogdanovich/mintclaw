import { toast } from "sonner"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { loadSessionMessages } from "@/features/chat/history"
import {
  clearStoredSessionId,
  readStoredSessionId,
} from "@/features/chat/state"
import { type ChatMessage, getChatState, updateChatStore } from "@/store/chat"
import { updateGatewayStore } from "@/store/gateway"

import {
  connectChat,
  disconnectChat,
  hydrateActiveSession,
  initializeChatStore,
  newChatSession,
  sendChatMessage,
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
  readStoredSessionId: vi.fn(() => ""),
  writeStoredSessionId: vi.fn(),
  clearStoredSessionId: vi.fn(),
  generateSessionId: () => "new-session",
  normalizeUnixTimestamp: (timestamp: number) => timestamp,
}))

class SocketFixture {
  static readonly OPEN = 1
  static readonly CONNECTING = 0
  static instances: SocketFixture[] = []
  readyState = SocketFixture.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  send = vi.fn()
  close = vi.fn(() => {
    this.readyState = 3
  })

  readonly url: string

  constructor(url: string) {
    this.url = url
    SocketFixture.instances.push(this)
  }

  open() {
    this.readyState = SocketFixture.OPEN
    this.onopen?.()
  }

  loseConnection() {
    this.readyState = 3
    this.onclose?.()
  }
}

function startGateway() {
  updateGatewayStore({ status: "running" })
  return SocketFixture.instances.at(-1)!
}

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
  vi.useFakeTimers()
  vi.mocked(readStoredSessionId).mockReturnValue("")
  SocketFixture.instances = []
  vi.stubGlobal("WebSocket", SocketFixture)
  vi.stubGlobal("window", {
    location: { protocol: "https:", host: "mintclaw.test" },
    setTimeout: globalThis.setTimeout,
    clearTimeout: globalThis.clearTimeout,
  })
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
  vi.unstubAllGlobals()
  vi.useRealTimers()
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

describe("chat connection lifecycle", () => {
  it("does not open duplicate sockets while connecting or connected", async () => {
    const socket = startGateway()
    await connectChat()
    socket.open()
    await connectChat()
    expect(SocketFixture.instances).toHaveLength(1)
    expect(socket.url).toBe(
      "wss://mintclaw.test/mintclaw/ws?session_id=initial",
    )
    expect(getChatState().connectionState).toBe("connected")
  })

  it("backs off reconnects and resets the delay after a successful connection", async () => {
    startGateway().loseConnection()
    await vi.advanceTimersByTimeAsync(999)
    expect(SocketFixture.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    SocketFixture.instances[1].loseConnection()
    await vi.advanceTimersByTimeAsync(1999)
    expect(SocketFixture.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    SocketFixture.instances[2].open()
    SocketFixture.instances[2].loseConnection()
    await vi.advanceTimersByTimeAsync(1000)
    expect(SocketFixture.instances).toHaveLength(4)
  })

  it.each(["stopped", "error"] as const)(
    "cancels a pending retry when the gateway is %s",
    async (status) => {
      startGateway().loseConnection()
      updateGatewayStore({ status })
      await vi.advanceTimersByTimeAsync(10000)
      expect(SocketFixture.instances).toHaveLength(1)
      expect(getChatState().connectionState).toBe("disconnected")
    },
  )

  it("cancels retries on an explicit disconnect and teardown", async () => {
    startGateway().loseConnection()
    disconnectChat()
    await vi.advanceTimersByTimeAsync(10000)
    expect(SocketFixture.instances).toHaveLength(1)
    await connectChat()
    SocketFixture.instances[1].loseConnection()
    teardownChatStore()
    await vi.advanceTimersByTimeAsync(10000)
    expect(SocketFixture.instances).toHaveLength(2)
  })

  it("ignores queued events from the old socket after a session switch", async () => {
    const old = startGateway()
    const stale = {
      open: old.onopen!,
      message: old.onmessage!,
      close: old.onclose!,
      error: old.onerror!,
    }
    vi.mocked(loadSessionMessages).mockResolvedValueOnce(messages("selected"))
    await switchChatSession("selected")
    const current = SocketFixture.instances[1]
    current.open()
    stale.open()
    stale.message({
      data: JSON.stringify({
        type: "message.create",
        payload: { content: "stale" },
      }),
    })
    stale.error()
    stale.close()
    await vi.advanceTimersByTimeAsync(10000)
    expect(getChatState().activeSessionId).toBe("selected")
    expect(getChatState().messages).toEqual(messages("selected"))
    expect(getChatState().connectionState).toBe("connected")
    expect(SocketFixture.instances).toHaveLength(2)
    expect(old.close).toHaveBeenCalledOnce()
  })

  it("rolls back a failed send without removing existing messages or retrying the send", async () => {
    const socket = startGateway()
    socket.open()
    socket.send.mockImplementationOnce(() => {
      throw new Error("closed during send")
    })
    expect(sendChatMessage({ content: "hello" })).toBe(false)
    await vi.advanceTimersByTimeAsync(10000)
    expect(getChatState().messages).toEqual(messages("initial"))
    expect(getChatState().isTyping).toBe(false)
    expect(socket.send).toHaveBeenCalledOnce()
  })

  it("rejects disconnected sends without adding optimistic messages", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {})
    expect(sendChatMessage({ content: "hello" })).toBe(false)
    expect(getChatState().messages).toEqual(messages("initial"))
    expect(getChatState().isTyping).toBe(false)
  })
})

describe("initial chat hydration", () => {
  function restore(history: ReturnType<typeof pendingHistory>) {
    teardownChatStore()
    updateChatStore({ messages: [], hasHydratedActiveSession: false })
    vi.mocked(readStoredSessionId).mockReturnValue("initial")
    vi.mocked(loadSessionMessages).mockReturnValueOnce(history.promise)
    updateGatewayStore({ status: "running" })
    initializeChatStore()
    return hydrateActiveSession()
  }

  it("loads history once before connecting and preserves messages received during hydration", async () => {
    const history = pendingHistory()
    const hydration = restore(history)
    void hydrateActiveSession()
    expect(loadSessionMessages).toHaveBeenCalledExactlyOnceWith("initial")
    expect(SocketFixture.instances).toHaveLength(0)
    updateChatStore({ messages: messages("live") })
    history.resolve(messages("history"))
    await hydration
    expect(getChatState().messages).toEqual([
      ...messages("history"),
      ...messages("live"),
    ])
    expect(getChatState().hasHydratedActiveSession).toBe(true)
    expect(SocketFixture.instances).toHaveLength(1)
  })

  it("recovers from failed history loading without discarding live messages", async () => {
    const history = pendingHistory()
    const hydration = restore(history)
    updateChatStore({ messages: messages("live") })
    history.reject(new Error("unavailable"))
    await hydration
    expect(getChatState().messages).toEqual(messages("live"))
    expect(clearStoredSessionId).not.toHaveBeenCalled()
    expect(getChatState().hasHydratedActiveSession).toBe(true)
    expect(SocketFixture.instances).toHaveLength(1)
  })

  it.each(["success", "failure"])(
    "ignores a previous lifecycle's history %s after reinitialization",
    async (outcome) => {
      const old = pendingHistory()
      const oldHydration = restore(old)
      const current = pendingHistory()
      const currentHydration = restore(current)
      if (outcome === "success") old.resolve(messages("stale"))
      else old.reject(new Error("stale hydration failure"))
      await oldHydration
      expect(console.error).not.toHaveBeenCalled()
      expect(getChatState().messages).toEqual([])
      expect(getChatState().hasHydratedActiveSession).toBe(false)
      expect(SocketFixture.instances).toHaveLength(0)
      void hydrateActiveSession()
      expect(loadSessionMessages).toHaveBeenCalledTimes(2)
      current.resolve(messages("current"))
      await currentHydration
      expect(getChatState().messages).toEqual(messages("current"))
      expect(SocketFixture.instances).toHaveLength(1)
    },
  )
})
