package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/unitaryai/osmia/internal/taskrun"
)

// Given a stream reader started from a webhook request,
// when the request's context is cancelled while the agent is still running,
// then the reader keeps reading and the agent's result reaches the task run.
//
// A real HTTP client is used because the fake clientset ignores context
// cancellation, so it cannot show this bug.
func TestStreamReaderOutlivesCallerContext(t *testing.T) {
	logOpen, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods"):
			fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"agent-pod"},`+
				`"status":{"containerStatuses":[{"name":"agent","state":{"running":{}}}]}}]}`)
		case strings.HasSuffix(r.URL.Path, "/agent-pod/log"):
			flusher := w.(http.Flusher)
			fmt.Fprintln(w, `{"type":"content","content":"working","role":"assistant","timestamp":"2026-01-01T00:00:00Z"}`)
			flusher.Flush()
			close(logOpen)
			<-release
			fmt.Fprintln(w, `{"type":"result","success":true,"summary":"done","timestamp":"2026-01-01T00:00:01Z"}`)
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := NewReconciler(testConfig(), testLogger(),
		WithEngine(&mockEngine{name: "claude-code"}),
		WithK8sClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL})),
		WithNamespace("test-ns"),
	)
	// Release the handler and stop the reader before the server closes.
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		r.stopEarly()
	}()

	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"

	requestCtx, endRequest := context.WithCancel(context.Background())
	r.startStreamReader(requestCtx, tr)

	select {
	case <-logOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("stream reader never opened the pod log")
	}
	endRequest()
	releaseOnce.Do(func() { close(release) })

	require.Eventually(t, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return tr.Result != nil && tr.Result.Summary == "done"
	}, 10*time.Second, 50*time.Millisecond, "result was lost when the request context ended")
}

// Given work started before Run, such as a reader for a webhook that arrived
// early,
// when Run returns,
// then that work's context is cancelled too.
func TestBackgroundContextBeforeRunStopsWhenRunReturns(t *testing.T) {
	r := NewReconciler(testConfig(), testLogger())
	early := r.backgroundContext()
	require.NoError(t, early.Err())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.Run(ctx, time.Hour)

	require.Error(t, early.Err(), "work started before Run must stop when Run returns")
}
