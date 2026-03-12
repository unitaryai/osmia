// Package sessionstore provides backends for persisting Claude Code session
// state between agent pod restarts. When enabled, both the ~/.claude directory
// (conversation history) and the /workspace/repo directory are written to
// durable storage so that retry pods can resume via `claude --resume <id>`
// instead of starting a fresh session.
//
// Session persistence is opt-in. When disabled, the controller falls back to
// the git-based continuation strategy (clone prior branch + prompt context).
//
// The SessionStore interface is defined in pkg/engine so that public packages
// (e.g. pkg/engine/claudecode) can reference it. The implementations in this
// package satisfy engine.SessionStore.
package sessionstore
