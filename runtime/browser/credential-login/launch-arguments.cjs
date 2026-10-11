'use strict';

// Preserve the disabled features of the pinned Playwright 1.63.0 launcher when
// adding NetworkTimeServiceQuerying and AimEnabled. Chromium uses the final
// --disable-features switch, so a second switch containing only our additions
// would replace those defaults. A dependency upgrade requires requalification.
const defaults = Object.freeze(['AvoidUnnecessaryBeforeUnloadCheckSync', 'DestroyProfileOnBrowserClose',
  'DialMediaRouteProvider', 'GlobalMediaControls', 'HttpsUpgrades', 'LensOverlay', 'MediaRouter', 'PaintHolding',
  'ThirdPartyStoragePartitioning', 'BlockOriginHeaderModificationOnRedirect', 'Translate', 'AutoDeElevate',
  'OptimizationHints', 'msForceBrowserSignIn', 'msEdgeUpdateLaunchServicesPreferredVersion']);

function privateArguments(pin) {
  if (require('../playwright-library/node_modules/playwright-core/package.json').version !== '1.63.0' ||
      typeof pin !== 'string' || pin.length !== 44 || Buffer.from(pin, 'base64').length !== 32 ||
      Buffer.from(pin, 'base64').toString('base64') !== pin) throw new Error('credential browser unavailable');
  return ['--disable-quic', '--force-webrtc-ip-handling-policy=disable_non_proxied_udp',
    `--disable-features=${[...defaults, 'NetworkTimeServiceQuerying', 'AimEnabled'].join(',')}`,
    `--ignore-certificate-errors-spki-list=${pin}`];
}

module.exports = { privateArguments };
