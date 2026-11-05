package controller

import "context"

// lifetimeContext wraps the context passed to Run, so that the
// Reconciler's atomic.Value always stores the same concrete type.
type lifetimeContext struct {
	ctx context.Context
}

// backgroundContext returns the context for work that outlives the call that
// starts it, such as agent stream readers and memory extraction. It is
// cancelled only when Run stops.
//
// It must not be derived from the caller's context. Webhook and Slack
// callbacks reach ProcessTicket and ResolveApproval with the HTTP request
// context, which is cancelled as soon as the response is written. A stream
// reader started from it stops at once, and the run's result, telemetry and
// merge request URL are lost.
//
// It is read without r.mu, so it is safe to call whether or not the caller
// holds the lock. Before Run starts (in tests, for example) it returns
// context.Background().
func (r *Reconciler) backgroundContext() context.Context {
	if lc, ok := r.lifetime.Load().(lifetimeContext); ok {
		return lc.ctx
	}
	return context.Background()
}
