package rate

import (
	"errors"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

var ErrQuotaExhausted = errors.New("traffic quota exhausted")

type QuotaConsumer interface {
	Consume(upload bool, count int64) bool
}

type QuotaWriter struct {
	writer   buf.Writer
	consumer QuotaConsumer
	upload   bool
}

func NewQuotaWriter(writer buf.Writer, consumer QuotaConsumer, upload bool) buf.Writer {
	return &QuotaWriter{writer: writer, consumer: consumer, upload: upload}
}

func (w *QuotaWriter) Close() error {
	return common.Close(w.writer)
}

func (w *QuotaWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if !w.consumer.Consume(w.upload, int64(mb.Len())) {
		buf.ReleaseMulti(mb)
		return ErrQuotaExhausted
	}
	return w.writer.WriteMultiBuffer(mb)
}

type QuotaReader struct {
	reader   buf.TimeoutReader
	consumer QuotaConsumer
	upload   bool
}

func NewQuotaReader(
	reader buf.TimeoutReader,
	consumer QuotaConsumer,
	upload bool,
) buf.TimeoutReader {
	return &QuotaReader{reader: reader, consumer: consumer, upload: upload}
}

func (r *QuotaReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBuffer()
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 && !r.consumer.Consume(r.upload, int64(mb.Len())) {
		buf.ReleaseMulti(mb)
		return nil, ErrQuotaExhausted
	}
	return mb, nil
}

func (r *QuotaReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBufferTimeout(timeout)
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 && !r.consumer.Consume(r.upload, int64(mb.Len())) {
		buf.ReleaseMulti(mb)
		return nil, ErrQuotaExhausted
	}
	return mb, nil
}
