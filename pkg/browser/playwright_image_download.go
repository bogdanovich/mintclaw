package browser

// This fixed private program extends the existing download boundary to an
// observed image. It uses a disposable page in the same browser context so
// cookies, the browser's proxy, and context routing (including every redirect)
// apply normally. APIRequestContext/CDP network fetches would bypass routing.
// The source page is never clicked, navigated, closed, or replaced. Bytes are
// intercepted before the temporary document can execute or download to disk.
const playwrightImageDownloadCode = `async function captureImage(imageLocator) {
    const element = await imageLocator.elementHandle({ timeout: 1000 });
    if (!element) return "MINTCLAW_DL_V1|error|stale_target";
    let temporary = null;
    let cdp = null;
    let stream = "";
    let navigation = Promise.resolve();
    let timer;
    try {
      const source = await element.evaluate(image => image.isConnected &&
        image instanceof HTMLImageElement ? image.currentSrc || image.src : "");
      if (!/^https?:\/\//i.test(source)) return "MINTCLAW_DL_V1|error|unsupported_target";
      // Bind the original node and selected source, not another image with
      // the same accessible caption. No resource request precedes this check.
      const unchanged = await element.evaluate((image, expected) => image.isConnected &&
        image instanceof HTMLImageElement && (image.currentSrc || image.src) === expected, source);
      if (!unchanged) return "MINTCLAW_DL_V1|error|stale_target";
      temporary = await page.context().newPage();
      cdp = await page.context().newCDPSession(temporary);
      const tree = await cdp.send("Page.getFrameTree");
      const frameID = tree.frameTree.frame.id;
      let settle;
      const ready = new Promise(resolve => { settle = resolve; });
      let claimed = false;
      let contentType = "";
      cdp.on("Fetch.requestPaused", async event => {
        try {
          if (event.frameId !== frameID || event.resourceType !== "Document" ||
              event.request.method !== "GET") {
            await cdp.send("Fetch.failRequest", { requestId: event.requestId, errorReason: "BlockedByClient" });
            settle(false);
            return;
          }
          if (event.responseStatusCode >= 300 && event.responseStatusCode < 400) {
            await cdp.send("Fetch.continueResponse", { requestId: event.requestId });
            return;
          }
          const header = (event.responseHeaders || []).find(item => item.name.toLowerCase() === "content-type");
          contentType = String(header && header.value || "").split(";")[0].trim().toLowerCase();
          if (claimed || event.responseStatusCode < 200 || event.responseStatusCode >= 300 ||
              !/^image\/[a-z0-9.+-]+$/.test(contentType)) {
            await cdp.send("Fetch.failRequest", { requestId: event.requestId, errorReason: "BlockedByClient" });
            settle(false);
            return;
          }
          claimed = true;
          stream = (await cdp.send("Fetch.takeResponseBodyAsStream", { requestId: event.requestId })).stream;
          settle(true);
        } catch (_) { settle(false); }
      });
      await cdp.send("Fetch.enable", { patterns: [{ urlPattern: "*", requestStage: "Response" }] });
      navigation = temporary.goto(source, { waitUntil: "commit", timeout: 10000 }).catch(() => {
        if (!claimed) settle(false);
      });
      const available = await Promise.race([
        ready, new Promise(resolve => { timer = setTimeout(() => resolve(false), 10000); }),
      ]);
      if (!available) return "MINTCLAW_DL_V1|error|read_failed";
      const parts = [];
      let total = 0;
      for (;;) {
        const part = await cdp.send("IO.read", { handle: stream, size: 128 * 1024 });
        const data = String(part.data || "");
        let bytes;
        let encoded;
        if (part.base64Encoded) {
          bytes = Math.floor(data.length * 3 / 4) - (data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0);
          encoded = data;
        } else {
          const converted = encodeUTF8Base64(data);
          bytes = converted.bytes;
          encoded = converted.encoded;
        }
        if (total + bytes > maximumBytes) return "MINTCLAW_DL_V1|error|oversize";
        total += bytes;
        if (encoded) parts.push(encoded);
        if (part.eof) break;
      }
      if (!total) return "MINTCLAW_DL_V1|error|read_failed";
      const subtype = contentType.slice("image/".length);
      const extension = ({ "svg+xml": "svg", "jpeg": "jpg", "x-icon": "ico", "vnd.microsoft.icon": "ico" }[subtype]
        || subtype).replace(/[^a-z0-9]/g, "").slice(0, 32);
      const disposition = 'attachment; filename="image.' + extension + '"';
      return "MINTCLAW_DL_V1|complete|" + encodeURIComponent(disposition) + "|" +
        encodeURIComponent(contentType) + "|" + total + "|" + parts.join(",");
    } catch (_) {
      // This branch performed only a resource GET on a disposable page, not
      // input on the source page. Report missing bytes, never invent a file.
      return "MINTCLAW_DL_V1|error|read_failed";
    } finally {
      clearTimeout(timer);
      if (stream && cdp) await cdp.send("IO.close", { handle: stream }).catch(() => {});
      if (temporary) {
        await temporary.close({ runBeforeUnload: false });
        if (!temporary.isClosed()) throw new Error("image capture cleanup failed");
      }
      if (cdp) await cdp.detach().catch(() => {});
      await navigation;
      await element.dispose();
      if (page.isClosed()) throw new Error("image source page is unavailable");
    }
  }`
