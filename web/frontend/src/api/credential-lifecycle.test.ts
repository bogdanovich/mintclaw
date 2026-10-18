import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { updateModel } from "@/api/models"
import { getOAuthFlow, loginOAuth, pollOAuthFlow } from "@/api/oauth"
import {
  EMPTY_MODEL_FORM,
  projectModelForm,
} from "@/components/models/model-form"

const fetchFixture = vi.fn<typeof fetch>()
const assign = vi.fn()

beforeEach(() => {
  fetchFixture.mockReset()
  assign.mockReset()
  vi.stubGlobal("fetch", fetchFixture)
  vi.stubGlobal("location", {
    pathname: "/credentials",
    href: "https://mintclaw.test/credentials",
    assign,
  })
})

afterEach(() => vi.unstubAllGlobals())

describe("credential request contracts", () => {
  it("sends a token only in the same-origin login body", async () => {
    fetchFixture.mockResolvedValueOnce(Response.json({ status: "ok" }))
    await loginOAuth({
      provider: "openai",
      method: "token",
      token: "private-token",
    })
    const [url, options] = fetchFixture.mock.calls[0]
    expect(url).toBe("/api/oauth/login")
    expect(options).toMatchObject({
      method: "POST",
      credentials: "same-origin",
    })
    expect(JSON.parse(options!.body as string)).toEqual({
      provider: "openai",
      method: "token",
      token: "private-token",
    })
  })

  it("encodes flow identifiers and distinguishes status reads from device polling", async () => {
    fetchFixture.mockImplementation(async () =>
      Response.json({ status: "pending" }),
    )
    const flow = "flow/with?query#fragment"
    await getOAuthFlow(flow)
    await pollOAuthFlow(flow)
    expect(fetchFixture.mock.calls[0]).toEqual([
      "/api/oauth/flows/flow%2Fwith%3Fquery%23fragment",
      { credentials: "same-origin" },
    ])
    expect(fetchFixture.mock.calls[1]).toEqual([
      "/api/oauth/flows/flow%2Fwith%3Fquery%23fragment/poll",
      { credentials: "same-origin", method: "POST" },
    ])
  })

  it("redirects an expired launcher session without automatically replaying a credential mutation", async () => {
    fetchFixture.mockResolvedValueOnce(
      Response.json({ error: "unauthorized" }, { status: 401 }),
    )
    await expect(
      loginOAuth({
        provider: "anthropic",
        method: "token",
        token: "private-token",
      }),
    ).rejects.toThrow("unauthorized")
    expect(assign).toHaveBeenCalledExactlyOnceWith("/launcher-login")
    expect(fetchFixture).toHaveBeenCalledOnce()
  })

  it.each(["", "new-private-key"])(
    "preserves an omitted saved model secret or sends an explicit replacement (%s)",
    async (apiKey) => {
      const projection = projectModelForm(
        { ...EMPTY_MODEL_FORM, provider: "openai", modelId: "model", apiKey },
        [],
        true,
      )
      expect(projection.ok).toBe(true)
      if (!projection.ok) throw new Error("invalid fixture")
      fetchFixture.mockResolvedValueOnce(Response.json({ status: "ok" }))
      await updateModel(2, {
        model_name: "primary",
        enabled: true,
        ...projection.value,
      })
      const [url, options] = fetchFixture.mock.calls[0]
      expect(url).toBe("/api/models/2")
      expect(options).toMatchObject({
        method: "PUT",
        credentials: "same-origin",
      })
      const body = JSON.parse(options!.body as string)
      if (apiKey) expect(body.api_key).toBe(apiKey)
      else expect(body).not.toHaveProperty("api_key")
    },
  )
})
