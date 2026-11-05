package controller

import "context"

// lifetimeContext wraps Run's context so the atomic.Value holds one type.
type lifetimeContext struct {
	ctx context.Context
}

// backgroundContext returns the context for work that outlives its caller, such
// as stream readers. It must not come from the caller: a webhook's request
// context ends when the response is written. Before Run starts, it returns a
// context that Run cancels when it returns.
func (r *Reconciler) backgroundContext() context.Context {
	if lc, ok := r.lifetime.Load().(lifetimeContext); ok {
		return lc.ctx
	}
	return context.Background()
}
