package orchestrator

import "testing"

func TestNoopMetadataPrefetcherWaitReturnsImmediately(t *testing.T) {
	p := newNoopMetadataPrefetcher()
	// Wait must return immediately for the noop; a deadlock or panic here is
	// a bug. We rely on the test harness's default timeout to catch a hang.
	p.Wait()
}
