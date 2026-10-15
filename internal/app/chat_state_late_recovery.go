package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/appstate"
)

const lateAppStateRecoveryMaxWait = 24 * time.Hour

// A capture buffers events under the caller's reserved persistence ticket,
// then forwards later events at their dispatch boundary. Public completion
// events cannot attribute a snapshot to our request or retire older debt.
type appStateRecoveryCapture struct {
	app        *App
	collection appstate.WAPatchName
	handlerID  uint32
	cancel     context.CancelFunc
	mu         sync.Mutex
	captured   []any
	forward    bool
	stopped    bool
}

func (a *App) newAppStateRecoveryCapture(collection appstate.WAPatchName) (*appStateRecoveryCapture, error) {
	a.appStateRecoveryMu.Lock()
	defer a.appStateRecoveryMu.Unlock()
	if a.appStateRecoveryClosing {
		return nil, fmt.Errorf("application is closing")
	}
	if previous := a.lateRecoveries[collection]; previous != nil {
		previous.cancel()
		previous.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	capture := &appStateRecoveryCapture{app: a, collection: collection, cancel: cancel}
	capture.handlerID = a.wa.AddEventHandler(capture.handle)
	if a.lateRecoveries == nil {
		a.lateRecoveries = make(map[appstate.WAPatchName]*appStateRecoveryCapture)
	}
	a.lateRecoveries[collection] = capture
	a.lateRecoveryWorkers.Go(func() {
		timer := time.NewTimer(lateAppStateRecoveryMaxWait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		capture.stop()
		a.appStateRecoveryMu.Lock()
		if a.lateRecoveries[collection] == capture {
			delete(a.lateRecoveries, collection)
		}
		a.appStateRecoveryMu.Unlock()
	})
	return capture, nil
}

func (c *appStateRecoveryCapture) handle(evt any) {
	if !slices.Contains(appStateCollectionsForEvent(evt), c.collection) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	if !c.forward {
		c.captured = append(c.captured, evt)
		return
	}
	// Reserve and submit immediately, so later live mutations cannot be
	// overwritten by a snapshot replayed by a sleeping continuation.
	c.app.handleAppStatePersistenceEvent(context.Background(), evt, nil)
}

func (c *appStateRecoveryCapture) releaseBuffered() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forward = true
	captured := c.captured
	c.captured = nil
	return captured
}

func (c *appStateRecoveryCapture) stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.app.wa.RemoveEventHandler(c.handlerID)
}
