package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const (
	playwrightDiagnosticsInitMarker   = "MINTCLAW_DIAGNOSTICS_INIT_V1"
	playwrightDiagnosticsResultMarker = "MINTCLAW_DIAGNOSTICS_RESULT_V1"
)

const playwrightDiagnosticsInitTemplate = `async (page) => {
  const key = Symbol.for("mintclaw.browser.diagnostics.v1");
  if (page[key]) return "MINTCLAW_DIAGNOSTICS_INIT_V1|ok";
	const diagnosticURLBytes = MINTCLAW_MAX_DIAGNOSTIC_URL_BYTES;
  const utf8 = value => {
    const bytes = [];
    for (const character of String(value || "")) {
      const point = character.codePointAt(0);
      if (point <= 0x7f) bytes.push(point);
      else if (point <= 0x7ff) bytes.push(0xc0 | point >>> 6, 0x80 | point & 0x3f);
      else if (point <= 0xffff) bytes.push(0xe0 | point >>> 12, 0x80 | point >>> 6 & 0x3f, 0x80 | point & 0x3f);
      else bytes.push(0xf0 | point >>> 18, 0x80 | point >>> 12 & 0x3f, 0x80 | point >>> 6 & 0x3f, 0x80 | point & 0x3f);
    }
    return bytes;
  };
  const sha256 = value => {
    const bytes = utf8(value);
    const bitLength = bytes.length * 8;
    bytes.push(0x80);
    while (bytes.length % 64 !== 56) bytes.push(0);
    const high = Math.floor(bitLength / 0x100000000);
    const low = bitLength >>> 0;
    for (let shift = 24; shift >= 0; shift -= 8) bytes.push(high >>> shift & 0xff);
    for (let shift = 24; shift >= 0; shift -= 8) bytes.push(low >>> shift & 0xff);
    const constants = [
      0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
      0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
      0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
      0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
      0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
      0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
      0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
      0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
    ];
    const state = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
    const rotate = (value, count) => value >>> count | value << 32 - count;
    const words = new Array(64);
    for (let offset = 0; offset < bytes.length; offset += 64) {
      for (let index = 0; index < 16; index++) {
        const at = offset + index * 4;
        words[index] = (bytes[at] << 24 | bytes[at + 1] << 16 | bytes[at + 2] << 8 | bytes[at + 3]) >>> 0;
      }
      for (let index = 16; index < 64; index++) {
        const s0 = rotate(words[index - 15], 7) ^ rotate(words[index - 15], 18) ^ words[index - 15] >>> 3;
        const s1 = rotate(words[index - 2], 17) ^ rotate(words[index - 2], 19) ^ words[index - 2] >>> 10;
        words[index] = (words[index - 16] + s0 + words[index - 7] + s1) >>> 0;
      }
      let [a,b,c,d,e,f,g,h] = state;
      for (let index = 0; index < 64; index++) {
        const s1 = rotate(e, 6) ^ rotate(e, 11) ^ rotate(e, 25);
        const choose = e & f ^ ~e & g;
        const first = (h + s1 + choose + constants[index] + words[index]) >>> 0;
        const s0 = rotate(a, 2) ^ rotate(a, 13) ^ rotate(a, 22);
        const majority = a & b ^ a & c ^ b & c;
        const second = (s0 + majority) >>> 0;
        h = g; g = f; f = e; e = (d + first) >>> 0; d = c; c = b; b = a; a = (first + second) >>> 0;
      }
      const next = [a,b,c,d,e,f,g,h];
      for (let index = 0; index < 8; index++) state[index] = (state[index] + next[index]) >>> 0;
    }
    return state.map(value => value.toString(16).padStart(8, "0")).join("");
  };
  const truncateUTF8 = (value, maximum) => {
    let result = "", bytes = 0;
    for (const character of String(value || "")) {
      const size = utf8(character).length;
      if (bytes + size > maximum) break;
      result += character; bytes += size;
    }
    return result;
  };
  const state = {
    cdp: await page.context().newCDPSession(page),
    categories: {
      console_errors: { count: 0, bytes: 0, entries: [] },
      failed_requests: { count: 0, bytes: 0, entries: [] },
      page_crashes: { count: 0, bytes: 0, entries: [] }
    },
    requests: new Map()
  };
  const hash = (domain, value) => {
    const raw = String(value || "");
    const bounded = truncateUTF8(raw, 65536);
    return sha256(domain + "\0" + bounded);
  };
	const consoleProjection = args => {
		const result = [];
		let remaining = 8192;
		const field = (value, maximum) => {
			if (remaining <= 0 || value === undefined || value === null) return "";
			const type = typeof value;
			const text = type === "string" || type === "number" || type === "boolean" || type === "bigint"
				? String(value) : "";
			const bounded = truncateUTF8(text, Math.min(maximum, remaining));
			remaining -= utf8(bounded).length;
			return bounded;
		};
		for (const arg of (Array.isArray(args) ? args.slice(0, 16) : [])) {
			result.push({
				type: field(arg && arg.type, 32), subtype: field(arg && arg.subtype, 32),
				value: field(arg && arg.value, 1024), description: field(arg && arg.description, 1024)
			});
			if (remaining <= 0) break;
		}
		return result;
	};
  const safeURL = raw => {
    try {
      const value = new URL(String(raw || ""));
      if ((value.protocol !== "http:" && value.protocol !== "https:") || value.username || value.password) return {};
			const origin = truncateUTF8(value.origin.toLowerCase(), diagnosticURLBytes);
			const path = truncateUTF8(value.pathname || "/", diagnosticURLBytes);
      return { origin, path };
    } catch (_) { return {}; }
  };
  const resourceClass = value => {
    const normalized = String(value || "other").toLowerCase();
    return ["document","stylesheet","image","media","font","script","texttrack","xhr","fetch","eventsource","websocket","manifest","other"].includes(normalized) ? normalized : "other";
  };
  const push = (category, entry) => {
    const bucket = state.categories[category];
    bucket.count++;
    const bytes = utf8(JSON.stringify(entry)).length;
    if (bucket.entries.length < 32 && bucket.bytes + bytes <= 15360) {
      bucket.entries.push(entry);
      bucket.bytes += bytes;
    }
  };
  state.cdp.on("Runtime.consoleAPICalled", event => {
    const severity = String(event && event.type || "").toLowerCase();
    if (severity !== "error" && severity !== "assert" && severity !== "warning") return;
    const frames = event && event.stackTrace && event.stackTrace.callFrames || [];
    const frame = frames[0] || {};
    const location = safeURL(frame.url);
    const values = consoleProjection(event && event.args);
    push("console_errors", {
      timestamp: Math.floor(Date.now() / 1000), severity: severity === "assert" ? "error" : severity,
      origin: location.origin || "", path: location.path || "",
      line: Number.isSafeInteger(frame.lineNumber) && frame.lineNumber >= 0 ? frame.lineNumber + 1 : 0,
      message_hash: hash("mintclaw.browser.console.v1", JSON.stringify(values))
    });
  });
  state.cdp.on("Runtime.exceptionThrown", event => {
    const details = event && event.exceptionDetails || {};
    const frame = details.stackTrace && details.stackTrace.callFrames && details.stackTrace.callFrames[0] || {};
    const location = safeURL(details.url || frame.url);
    const raw = details.exception && (details.exception.description || details.exception.value) || details.text || "exception";
    push("console_errors", {
      timestamp: Math.floor(Date.now() / 1000), severity: "error",
      origin: location.origin || "", path: location.path || "",
      line: Number.isSafeInteger(details.lineNumber) && details.lineNumber >= 0 ? details.lineNumber + 1 : 0,
      message_hash: hash("mintclaw.browser.exception.v1", raw)
    });
  });
  state.cdp.on("Network.requestWillBeSent", event => {
    if (!event || !event.requestId) return;
    if (state.requests.size >= 256) state.requests.delete(state.requests.keys().next().value);
    state.requests.set(event.requestId, { ...safeURL(event.request && event.request.url), resource_class: resourceClass(event.type), reported: false });
  });
  state.cdp.on("Network.responseReceived", event => {
    if (!event || !event.requestId || !event.response || Number(event.response.status) < 400) return;
    const request = state.requests.get(event.requestId);
    if (request) request.reported = true;
		const location = request || { ...safeURL(event.response.url), resource_class: resourceClass(event.type) };
    push("failed_requests", {
      timestamp: Math.floor(Date.now() / 1000), resource_class: location.resource_class || resourceClass(event.type),
      failure_code: "http_error", origin: location.origin || "", path: location.path || "",
      message_hash: hash("mintclaw.browser.request-failure.v1", "http_status_" + String(event.response.status))
    });
  });
  state.cdp.on("Network.loadingFinished", event => { if (event) state.requests.delete(event.requestId); });
  state.cdp.on("Network.loadingFailed", event => {
    if (!event) return;
    const request = state.requests.get(event.requestId) || {};
    state.requests.delete(event.requestId);
    if (request.reported) return;
    let failure = "network_failed";
    if (event.canceled) failure = "canceled";
    else if (event.blockedReason) failure = "blocked";
    push("failed_requests", {
      timestamp: Math.floor(Date.now() / 1000), resource_class: request.resource_class || resourceClass(event.type),
      failure_code: failure, origin: request.origin || "", path: request.path || "",
      message_hash: hash("mintclaw.browser.request-failure.v1", event.errorText || failure)
    });
  });
  state.cdp.on("Inspector.targetCrashed", () => {
    push("page_crashes", { timestamp: Math.floor(Date.now() / 1000), failure_code: "page_crashed" });
  });
  await state.cdp.send("Runtime.enable");
  await state.cdp.send("Network.enable");
  await state.cdp.send("Inspector.enable");
  Object.defineProperty(page, key, { value: state, configurable: true });
  page.once("close", () => { delete page[key]; state.cdp.detach().catch(() => {}); });
  return "MINTCLAW_DIAGNOSTICS_INIT_V1|ok";
}`

var playwrightDiagnosticsInitCode = strings.Replace(
	playwrightDiagnosticsInitTemplate,
	"MINTCLAW_MAX_DIAGNOSTIC_URL_BYTES",
	strconv.Itoa(MaxURLBytes),
	1,
)

func playwrightDiagnosticsReadCode(categories []DiagnosticCategory) string {
	encoded, _ := json.Marshal(categories)
	return `async (page) => {
  const state = page[Symbol.for("mintclaw.browser.diagnostics.v1")];
  if (!state) return "MINTCLAW_DIAGNOSTICS_RESULT_V1|not_initialized";
  const requested = ` + string(encoded) + `;
  const categories = requested.map(category => {
    const bucket = state.categories[category];
    const entries = bucket ? bucket.entries.map(entry => ({ ...entry })) : [];
    const count = bucket ? bucket.count : 0;
    const omitted_count = Math.max(0, count - entries.length);
    return { category, count, omitted_count, truncated: omitted_count > 0, entries };
  });
  const result = { categories, truncated: categories.some(category => category.truncated) };
  return "MINTCLAW_DIAGNOSTICS_RESULT_V1|ok|" + encodeURIComponent(JSON.stringify(result));
}`
}

func (worker *playwrightWorker) initializeDiagnostics(ctx context.Context) error {
	text, err := worker.callDiagnosticsCode(ctx, playwrightDiagnosticsInitCode)
	if err != nil {
		return err
	}
	status, _, err := parsePlaywrightDiagnosticsResult(text, playwrightDiagnosticsInitMarker)
	if err != nil || status != "ok" {
		return ErrDriverIncompatible
	}
	return nil
}

func (worker *playwrightWorker) Diagnostics(
	ctx context.Context,
	categories []DiagnosticCategory,
) (DiagnosticSummary, error) {
	normalized, err := NormalizeDiagnosticCategories(categories)
	if err != nil {
		return DiagnosticSummary{}, err
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil {
		return DiagnosticSummary{}, ErrWorkerUnavailable
	}
	code := playwrightDiagnosticsReadCode(normalized)
	text, err := worker.callDiagnosticsCode(ctx, code)
	if err != nil {
		return DiagnosticSummary{}, err
	}
	status, payload, err := parsePlaywrightDiagnosticsResult(text, playwrightDiagnosticsResultMarker)
	if err != nil {
		worker.lost = true
		return DiagnosticSummary{}, ErrDriverIncompatible
	}
	if status == "not_initialized" {
		if err = worker.initializeDiagnostics(ctx); err != nil {
			worker.lost = true
			return DiagnosticSummary{}, ErrDriverIncompatible
		}
		text, err = worker.callDiagnosticsCode(ctx, code)
		if err != nil {
			return DiagnosticSummary{}, err
		}
		status, payload, err = parsePlaywrightDiagnosticsResult(text, playwrightDiagnosticsResultMarker)
	}
	if err != nil || status != "ok" || len(payload) == 0 || len(payload) > MaxDiagnosticResultBytes {
		worker.lost = true
		return DiagnosticSummary{}, ErrDriverIncompatible
	}
	var summary DiagnosticSummary
	if err = json.Unmarshal(payload, &summary); err != nil || ValidateDiagnosticSummary(summary, normalized) != nil {
		worker.lost = true
		return DiagnosticSummary{}, ErrDriverIncompatible
	}
	return summary, nil
}

func (worker *playwrightWorker) callDiagnosticsCode(ctx context.Context, code string) (string, error) {
	result, err := worker.client.CallTool(ctx, "browser_run_code_unsafe", map[string]any{"code": code})
	if err != nil || result == nil {
		return "", ErrWorkerUnavailable
	}
	text, err := boundedPlaywrightText(result, playwrightDriverResponseBytes)
	if err != nil || result.IsError {
		return "", errors.Join(ErrDriverRejected, err)
	}
	return text, nil
}

func parsePlaywrightDiagnosticsResult(text, marker string) (string, []byte, error) {
	const resultHeader = "### Result"
	if strings.Count(text, resultHeader) != 1 {
		return "", nil, ErrDriverIncompatible
	}
	line := text[strings.Index(text, resultHeader)+len(resultHeader):]
	line = strings.TrimLeft(line, "\r\n")
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	line = strings.Trim(line, "\r\"' ")
	parts := strings.SplitN(line, "|", 3)
	if len(parts) < 2 || parts[0] != marker {
		return "", nil, ErrDriverIncompatible
	}
	if len(parts) == 2 {
		return parts[1], nil, nil
	}
	decoded, err := url.QueryUnescape(parts[2])
	if err != nil {
		return "", nil, ErrDriverIncompatible
	}
	return parts[1], []byte(decoded), nil
}
