package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Given background work such as a stream reader started from a webhook,
// when the webhook request ends,
// then the work keeps running until the reconciler itself stops.
func TestBackgroundContextFollowsRunNotCaller(t *testing.T) {
	r := &Reconciler{}
	assert.NoError(t, r.backgroundContext().Err(), "usable before Run starts")

	runCtx, stopRun := context.WithCancel(context.Background())
	r.lifetime.Store(lifetimeContext{ctx: runCtx})
	bg := r.backgroundContext()
	assert.NoError(t, bg.Err())

	stopRun()
	assert.Error(t, bg.Err(), "background work must stop when Run stops")
}
