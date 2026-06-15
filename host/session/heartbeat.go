package session

import (
	"context"
	"time"
)

// RunHeartbeat probes a peer to detect a vanished connection that never sent a
// clean close. On each interval it calls ping with a context bounded by timeout;
// after maxMiss consecutive failures it invokes onDead (e.g. close the session)
// and returns. A successful ping resets the miss counter. It returns when ctx is
// cancelled. The shared logic backs both the WebSocket ping and the SSH
// keepalive front doors. A nil ping, non-positive interval, or non-positive
// maxMiss makes it a no-op.
func RunHeartbeat(ctx context.Context, interval, timeout time.Duration, maxMiss int, ping func(context.Context) error, onDead func()) {
	if ping == nil || interval <= 0 || maxMiss <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, timeout)
			err := ping(pctx)
			cancel()
			if ctx.Err() != nil {
				return // session ended while we were probing
			}
			if err != nil {
				misses++
				if misses >= maxMiss {
					if onDead != nil {
						onDead()
					}
					return
				}
				continue
			}
			misses = 0
		}
	}
}
