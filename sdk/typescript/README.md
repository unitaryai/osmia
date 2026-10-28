# Osmia TypeScript Plugin SDK

Use this SDK to build external Osmia plugins in TypeScript/Node.js. Plugins
run as out-of-process gRPC servers; the controller connects to them over the
hashicorp/go-plugin transport.

## Prerequisites

- Node.js 18 or later
- `@bufbuild/protobuf ^2.0.0`
- `@connectrpc/connect ^2.0.0`
- `@connectrpc/connect-node ^2.0.0`

## Installation

> **Note:** The package is not yet published to npm. This is a scaffold.
> Install from source for now:

```bash
cd sdk/typescript
npm install
npm run build
```

Once published, installation will be:

```bash
npm install @unitaryai/osmia-plugin-sdk
```

## Generating protobuf stubs

Run this from the repository root. It regenerates all TypeScript
(and Python) stubs from the canonical `proto/` definitions:

```bash
make sdk-gen
```

Generated files are written to `sdk/typescript/src/proto/`. Do not edit
them by hand; every `make sdk-gen` run overwrites them.

## Quick start: notifications plugin

```typescript
import { PluginBase } from "@unitaryai/osmia-plugin-sdk";

// Generated stubs. Run `make sdk-gen` first.
// import { NotificationChannelService } from "./proto/notifications_pb.js";

class MyNotificationPlugin extends PluginBase {
  // Implement gRPC service methods here.
  // See examples/notifications/index.ts for a full skeleton.
}

const plugin = new MyNotificationPlugin();
plugin.serve();
```

## Further reading

Full plugin authoring guide:
<https://unitaryai.github.io/osmia/plugins/writing-a-plugin/>
