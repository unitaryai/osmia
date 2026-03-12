/**
 * Base class helpers for authoring Osmia gRPC plugins in TypeScript.
 *
 * Every Osmia plugin is an out-of-process gRPC server. The controller
 * spawns the plugin binary as a subprocess and negotiates a connection
 * over the hashicorp/go-plugin transport (stdin/stdout handshake followed
 * by a local gRPC channel).
 *
 * Usage:
 * ```typescript
 * import { PluginBase } from "@unitaryai/osmia-plugin-sdk";
 *
 * class MyPlugin extends PluginBase {
 *   // Override serve() or add your gRPC service registration here.
 * }
 *
 * new MyPlugin().serve();
 * ```
 *
 * @module plugin
 */

import { createServer } from "@connectrpc/connect-node";
import type { ConnectRouter } from "@connectrpc/connect";
import * as http2 from "node:http2";

/** Default port used when OSMIA_PLUGIN_PORT is not set (0 = ephemeral). */
const DEFAULT_PORT = parseInt(process.env["OSMIA_PLUGIN_PORT"] ?? "0", 10);

/**
 * PluginBase is the abstract base class for all Osmia TypeScript plugins.
 *
 * Extend this class and implement {@link registerRoutes} to add your gRPC
 * service handlers, then call {@link serve} to start the server.
 */
export abstract class PluginBase {
  /**
   * Register gRPC service routes on the ConnectRPC router.
   *
   * Override this method in your plugin subclass and add routes using
   * `router.service(MyService, implementation)`.
   *
   * @param router - The ConnectRPC router to register services on.
   */
  protected abstract registerRoutes(router: ConnectRouter): void;

  /**
   * Start a blocking gRPC server hosting the routes registered in
   * {@link registerRoutes}.
   *
   * The listening port is taken from the `OSMIA_PLUGIN_PORT` environment
   * variable. When not set the server binds to an ephemeral port on
   * `127.0.0.1`.
   *
   * @param port - Optional TCP port override. Defaults to `OSMIA_PLUGIN_PORT`
   *               or 0 (ephemeral).
   */
  serve(port: number = DEFAULT_PORT): void {
    const routes = (router: ConnectRouter) => this.registerRoutes(router);
    const server = http2.createServer(
      createServer({ routes }).nodeHandler,
    );

    server.listen(port, "127.0.0.1", () => {
      const addr = server.address();
      const actualPort = typeof addr === "object" && addr ? addr.port : port;
      console.log(`Osmia plugin listening on 127.0.0.1:${actualPort}`);
    });
  }
}
