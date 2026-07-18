# ADR 0007: Use Tailscale Without a Cloud Relay

## Status

Accepted.

## Context

The Mac performs synthesis and HLS encoding; the iPhone needs to reach it while away from the home LAN. Both devices already belong to the same Tailscale tailnet. Raven does not need public discovery, multi-tenant access, or third-party media hosting.

The current server binds plain HTTP to the Mac's Tailscale address, and the iOS app has an App Transport Security exception for that exact address.

## Decision

Serve HLS and the control/transcript API directly over Tailscale. Do not add a public cloud relay, port forwarding, or an application-level authentication service.

## Consequences

- Reply text, audio, and control traffic remain between Asif's devices on the private tailnet.
- There is no relay hosting cost or third-party media dependency.
- No public port or router configuration is required.
- Both devices must be connected to Tailscale.
- The current Mac address is fixed in the iPhone code and `Info.plist`; changing it requires a rebuild.
- The HTTP API has no application-layer authentication. The Tailscale network boundary is therefore load-bearing.

## Alternatives considered

- **Public cloud relay.** Rejected as unnecessary infrastructure, cost, authentication, and privacy exposure for a single-user system.
- **Direct public port forwarding.** Rejected because it expands the attack surface and requires network configuration that Tailscale already replaces.
- **Same-LAN-only access.** Rejected because Raven's primary use occurs while the phone is away from the Mac's local network.

See the [tailnet API](../../README.md#tailnet-api) and [ADR 0003](0003-continuous-hls-comfort-noise.md).
