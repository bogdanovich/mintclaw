"use strict";
(() => {
  const element = id => document.getElementById(id);
  const status = text => { element("status").textContent = text; };
  const lifetime = new AbortController();
  addEventListener("hashchange", () => {
    lifetime.abort();
    element("action").disabled = true;
    element("revoke").disabled = true;
    location.reload();
  });
  const original = location.hash;
  const pagePath = location.pathname;
  history.replaceState(null, "", pagePath);
  const match = /^#(request|enroll|revoke)=([a-f0-9]{64})$/.exec(original);
  if (!match && original !== "") return;
  let mode = match ? match[1] : "verify", reference = match ? match[2] : "", ceremony, grantID = "";
  const busy = value => { element("action").disabled = value; element("revoke").disabled = value; };
  const encode = buffer => btoa(String.fromCharCode(...new Uint8Array(buffer))).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  const decode = value => Uint8Array.from(atob(value.replaceAll("-", "+").replaceAll("_", "/")), char => char.charCodeAt(0));
  const post = async (path, body) => {
    const response = await fetch(pagePath + path.slice(1), {method: "POST", credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer",
      headers: {"Content-Type": "application/json"}, body: JSON.stringify(body), signal: lifetime.signal});
    if (!response.ok) throw new Error("unavailable");
    return await response.json();
  };
  const options = (raw, registration) => {
    if (registration && PublicKeyCredential.parseCreationOptionsFromJSON) return PublicKeyCredential.parseCreationOptionsFromJSON(raw);
    if (!registration && PublicKeyCredential.parseRequestOptionsFromJSON) return PublicKeyCredential.parseRequestOptionsFromJSON(raw);
    const result = structuredClone(raw);
    result.challenge = decode(result.challenge);
    if (registration) result.user.id = decode(result.user.id);
    for (const key of ["allowCredentials", "excludeCredentials"]) if (result[key]) result[key] = result[key].map(item => ({...item, id: decode(item.id)}));
    return result;
  };
  const credentialJSON = credential => {
    const result = {id: credential.id, rawId: encode(credential.rawId), type: credential.type,
      clientExtensionResults: credential.getClientExtensionResults(), response: {clientDataJSON: encode(credential.response.clientDataJSON)}};
    if (credential.authenticatorAttachment) result.authenticatorAttachment = credential.authenticatorAttachment;
    for (const field of ["attestationObject", "authenticatorData", "signature", "userHandle"])
      if (credential.response[field]) result.response[field] = encode(credential.response[field]);
    if (credential.response.getTransports) result.response.transports = credential.response.getTransports();
    return result;
  };
  const showScope = scope => {
    element("accounts").replaceChildren(...scope.accounts.map(alias => { const item = document.createElement("li"); item.textContent = alias; return item; }));
    if (scope.vault_login_access) {
      const item = document.createElement("li");
      item.textContent = "All website-matching Login items in the dedicated 1Password vault, including stored one-time codes.";
      element("accounts").append(item);
    }
    for (const [id, key] of [["agent", "agent_id"], ["host", "host_id"], ["actor", "actor_id"], ["owner", "owner_id"]]) element(id).textContent = scope.principal[key];
    const hours = scope.duration_seconds / 3600;
    element("duration").textContent = hours >= 1 ? `${hours} hours` : `${scope.duration_seconds / 60} minutes`;
    element("scope").hidden = false;
  };
  const begin = async () => {
    element("action").hidden = true;
    const path = mode === "request" ? "approve" : mode;
    ceremony = await post(`/api/${path}/begin`, mode === "enroll" ? {token: reference} : mode === "verify" ? {} : {id: reference});
    if (!ceremony.id || !ceremony.options?.publicKey) throw new Error("unavailable");
    element("title").textContent = mode === "enroll" ? "Set up phone approval" : mode === "verify" ? "Verify phone approval" : mode === "revoke" ? "Revoke account access" : "Approve account access";
    element("intro").textContent = mode === "enroll" ? "Create a passkey to approve MintClaw access from your phone. Save it in the password manager you use on Android." :
      mode === "verify" ? "Use your existing approval passkey on this device to finish or check setup. This does not grant access to any account." :
      mode === "revoke" ? "Verify your passkey to stop this grant. Its browser sessions will lose access." : "Review the accounts and agent before approving with your passkey.";
    if (mode === "request" || mode === "revoke") showScope(ceremony.scope);
    element("action").textContent = mode === "enroll" ? "Create approval passkey" : mode === "verify" ? "Verify approval passkey" : mode === "revoke" ? "Revoke with passkey" : "Approve with passkey";
    element("action").hidden = false;
  };
  element("action").addEventListener("click", async () => {
    busy(true); status("Waiting for your passkey…");
    try {
      // The page may have stayed open longer than the short challenge lifetime.
      // Refresh an unattempted challenge without extending the approval link.
      if (mode === "request") await begin();
      const registration = mode === "enroll";
      const publicKey = options(ceremony.options.publicKey, registration);
      const credential = registration ? await navigator.credentials.create({publicKey, signal: lifetime.signal}) :
        await navigator.credentials.get({publicKey, signal: lifetime.signal});
      if (!credential) throw new Error("unavailable");
      const path = mode === "request" ? "approve" : mode;
      const body = {id: ceremony.id, credential: credentialJSON(credential)};
      if (registration) body.token = reference;
      const result = await post(`/api/${path}/finish`, body);
      element("action").hidden = true;
      if (result.status === "approved") {
        grantID = result.grant_id; reference = ""; element("revoke").hidden = false;
        status(`Approved until ${new Date(result.expires_at).toLocaleString()}. You can revoke access now.`);
      } else if (result.status === "enrolled") {
        reference = ""; mode = "verify"; await begin();
        status("Passkey enrolled. Verify it to complete setup. You can also open this page on your phone to verify it there.");
      }
      else if (result.status === "verified") { reference = ""; status("Passkey verified. Phone approval setup is complete. No account access was granted."); }
      else if (result.status === "revoked") { grantID = ""; reference = ""; status("Access revoked."); }
      else throw new Error("unavailable");
    } catch {
      element("action").hidden = true;
      status("This attempt did not complete. Open the approval link again, or request a fresh approval if it has expired.");
    } finally { busy(false); }
  });
  element("revoke").addEventListener("click", async () => {
    element("revoke").hidden = true; mode = "revoke"; reference = grantID; status(""); busy(true);
    try { await begin(); } catch { status("This grant is no longer available. Request a fresh approval if needed."); }
    finally { busy(false); }
  });
  if (!isSecureContext || !window.PublicKeyCredential || !navigator.credentials) { status("Open this secure page in a browser that supports passkeys."); return; }
  begin().catch(() => status("This link is unavailable or expired. Request a fresh approval or private setup link."));
})();
