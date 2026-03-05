/**
 * Example: custom notification channel plugin for RoboDev.
 *
 * This skeleton shows how to implement a NotificationChannel plugin in
 * TypeScript. The plugin runs as a standalone gRPC server; the RoboDev
 * controller connects to it over the hashicorp/go-plugin transport.
 *
 * Prerequisites:
 *   1. Run `make sdk-gen` from the repo root to generate
 *      `sdk/typescript/src/proto/` stubs.
 *   2. Run `npm install && npm run build` inside `sdk/typescript/`.
 *
 * Run the plugin:
 *   node dist/examples/notifications/index.js
 */

import { PluginBase } from "@unitaryai/robodev-plugin-sdk";
import type { ConnectRouter } from "@connectrpc/connect";

// Generated stubs — run `make sdk-gen` first.
// import { NotificationChannelService } from "../../src/proto/notifications_pb.js";

/** Current interface version — must match the controller's expectation. */
const INTERFACE_VERSION = 1;

/**
 * MyNotificationPlugin is a minimal custom notification channel.
 *
 * Replace the stub implementations with your actual delivery logic
 * (e.g. HTTP calls to a webhook, Microsoft Teams, PagerDuty, etc.).
 */
class MyNotificationPlugin extends PluginBase {
  protected registerRoutes(router: ConnectRouter): void {
    // Uncomment once stubs are generated:
    // router.service(NotificationChannelService, {
    //   handshake: (_req) => ({
    //     interfaceVersion: INTERFACE_VERSION,
    //     pluginName: "my-notifications",
    //     pluginVersion: "0.1.0",
    //   }),
    //   notify: async (req) => {
    //     console.log("notify", req.message);
    //     return {};
    //   },
    //   notifyStart: async (req) => {
    //     console.log("notifyStart", req.ticket?.id);
    //     return {};
    //   },
    //   notifyComplete: async (req) => {
    //     console.log("notifyComplete", req.ticket?.id);
    //     return {};
    //   },
    // });
    void router;
    void INTERFACE_VERSION;
    throw new Error("registerRoutes: uncomment after running make sdk-gen");
  }
}

// Start the plugin server.
new MyNotificationPlugin().serve();
