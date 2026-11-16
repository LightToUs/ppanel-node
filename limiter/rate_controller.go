package limiter

import (
	"sync/atomic"
	"time"

	"github.com/juju/ratelimit"
)

// RateController keeps a stable limiter reference for active connections while
// allowing the effective rate to be replaced atomically.
type RateController struct {
	bucket atomic.Pointer[ratelimit.Bucket]
	rate   atomic.Int64
}

func newRateController(bytesPerSecond int64) *RateController {
	controller := &RateController{}
	controller.Update(bytesPerSecond)
	return controller
}

func (c *RateController) Update(bytesPerSecond int64) {
	if c.rate.Swap(bytesPerSecond) == bytesPerSecond {
		return
	}
	if bytesPerSecond <= 0 {
		c.bucket.Store(nil)
		return
	}
	c.bucket.Store(ratelimit.NewBucketWithQuantum(
		time.Second,
		bytesPerSecond,
		bytesPerSecond,
	))
}

func (c *RateController) Wait(count int64) {
	if c == nil || count <= 0 {
		return
	}
	if bucket := c.bucket.Load(); bucket != nil {
		bucket.Wait(count)
	}
}
