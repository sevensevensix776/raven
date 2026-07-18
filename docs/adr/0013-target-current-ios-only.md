# ADR 0013 — Target only the current phone's iOS (iOS 26)

## Status

Accepted (2026-07-18).

## Context

Raven's iPhone app runs on exactly one device: Asif's iPhone 17 on iOS 26.5.
There is no App Store distribution, no second user, and no fleet of older
devices to support. The Xcode project had inherited a deployment target of iOS
17, which forced backward-compatibility conditionals — e.g. `onScrollGeometryChange`
(the scroll-to-bottom detection) is iOS 18+, and using it under a 17 target
required either an `if #available` wrapper or a target bump.

## Decision

Set `IPHONEOS_DEPLOYMENT_TARGET = 26.0`. Build features for the current phone's
iOS only. Use the newest available APIs directly, without availability guards or
fallback code paths for iOS versions this device will never run.

## Consequences

- Newest SwiftUI/AVFoundation APIs are available unconditionally; no
  `if #available` clutter or dead fallback branches.
- The app will not install on a device older than iOS 26 — acceptable, since it
  targets exactly one iOS 26 device.
- When the phone's iOS advances, bump the target to match. The target should
  track the actual device, not lag behind it.

## Alternatives considered

- **Keep a low target (17/18) and guard new APIs with `if #available`.** Rejected:
  pure overhead for a single-device app — dead code paths for OS versions that
  will never run here.
- **Target the absolute latest beta.** Not chosen; track the shipping iOS on the
  actual device (26.x), not a beta the phone isn't on.
