package rate

import (
	"time"

	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*Reader)(nil)

type Reader struct {
	reader  buf.TimeoutReader
	limiter *ratelimit.Bucket
}

func NewRateLimitReader(reader buf.TimeoutReader, limiter *ratelimit.Bucket) buf.TimeoutReader {
	return &Reader{
		reader:  reader,
		limiter: limiter,
	}
}

func (r *Reader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBuffer()
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		r.limiter.Wait(int64(mb.Len()))
	}
	return mb, nil
}

func (r *Reader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBufferTimeout(timeout)
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		r.limiter.Wait(int64(mb.Len()))
	}
	return mb, nil
}
