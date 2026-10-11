# Steel browser resilience roadmap

Goal: let MintClaw recover from a confirmed browser anti-bot challenge using an
explicitly configured Steel target, preserve successful provider profiles, and
bound paid browser use. Password onboarding is a later task.

## Delivery sequence

1. **Steel controls and spending admission.** Add opt-in residential proxy and
   CAPTCHA solving settings. Keep the existing provider timeout and profile
   persistence contract. Add durable daily session/time limits and a USD cost
   stop threshold for paid sessions. Admission must survive gateway restarts;
   failures must not silently reset the allowance.
2. **Automatic navigation recovery.** Recognize specific challenge evidence,
   then retry one successful, read-only top-level navigation on an authorized
   Steel target. A 404, a generic 403/429, missing inventory, an ordinary login
   page, or a lost worker is not sufficient evidence. Never replay clicks,
   form submissions, uploads, downloads, or privileged execution.
3. **Session and domain reuse.** Keep one live provider session and its egress
   throughout the task. Reuse the existing opaque Steel profile for cookies
   across sessions. Remember a successful target per exact hostname and owner,
   with bounded retention, and invalidate it when policy/profile changes.
4. **Qualification and rollout.** Test configuration, provider requests,
   restart-safe limits, challenge classification, recovery authorization,
   stale references, and profile reuse. Publish the implementation for review.
   After merge, configure the deployed main browser with the user's Steel
   account and run a local-success / confirmed-block / Steel-recovery exercise.

## Spending semantics

Session count and reserved browser seconds are hard admission limits. Reserve
the full allowed session duration before calling Steel; failed or interrupted
creation consumes its reservation because the billable outcome may be unknown.

Steel meters browser time, proxy bandwidth, and CAPTCHA solves separately. Its
session creation API does not offer a hard USD, bandwidth, or CAPTCHA-count cap.
A cost stop threshold therefore stops a session when reported cost reaches the
threshold, with possible overshoot while usage and billing reports propagate.
Document that limitation explicitly. Use a small prepaid account balance with
automatic top-up disabled to bound account exposure during the initial trial.

## Acceptance exercise

- An ordinary site opens locally without creating a paid session.
- A known challenge causes at most one authorized Steel retry and returns new
  session, context, and element identities.
- Conditional proxies try direct Steel first, then admit one proxied Steel
  session only on fresh challenge evidence. Both modes share the same profile
  and spending allowance; ordinary HTTP errors do not enable proxies.
- An unavailable provider or exhausted allowance returns a clear bounded
  failure; it does not create an unbounded retry loop.
- A successful Steel domain is reused on a subsequent safe navigation.
- Closing and reopening the same Steel profile preserves cookies. No browser
  cookies or API keys appear in tool results, public config, or logs.
- A session keeps its provider egress; a new Steel session may receive a new IP.

## Scope after this milestone

Scrapfly and Bright Data remain possible later providers; neither is required
for the first Steel trial. Human login and password/session handoff will be
designed after this milestone, using the existing profile ownership boundaries.

Provider references: [proxies](https://docs.steel.dev/overview/stealth/proxies),
[CAPTCHA solving](https://docs.steel.dev/overview/stealth/captcha-solving), and
[pricing and limits](https://docs.steel.dev/overview/pricinglimits).
