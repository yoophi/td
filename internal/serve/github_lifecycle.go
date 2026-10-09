package serve

import (
	"context"
	"net/http"
	"sync"
)

// githubRequestLifecycle propagates server shutdown to each gh subprocess via
// its HTTP request context. Cancellation does not prove a remote write failed;
// mutation clients retain their uncertain-write errors and never retry.
type githubRequestLifecycle struct {
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	stopParent func() bool
	stopped    bool
}

func newGitHubRequestLifecycle() *githubRequestLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &githubRequestLifecycle{ctx: ctx, cancel: cancel}
}
func (l *githubRequestLifecycle) start(parent context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.stopParent != nil {
		return
	}
	l.stopParent = context.AfterFunc(parent, l.cancel)
	if parent.Err() != nil {
		l.cancel()
	}
}
func (l *githubRequestLifecycle) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	l.cancel()
	if l.stopParent != nil {
		l.stopParent()
		l.stopParent = nil
	}
}
func (l *githubRequestLifecycle) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.ctx.Err() != nil {
			WriteError(w, "server_stopping", "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(l.ctx, cancel)
		defer func() { stop(); cancel() }()
		// AfterFunc may run asynchronously; do not start another backend request
		// when shutdown has already reached the lifecycle context.
		if l.ctx.Err() != nil {
			cancel()
			WriteError(w, "server_stopping", "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
