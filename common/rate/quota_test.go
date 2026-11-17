package rate

import (
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

type quotaTestConsumer struct {
	allowed bool
	upload  bool
	count   int64
}

func (c *quotaTestConsumer) Consume(upload bool, count int64) bool {
	c.upload = upload
	c.count += count
	return c.allowed
}

type quotaTestWriter struct {
	calls int
}

func (w *quotaTestWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.calls++
	buf.ReleaseMulti(mb)
	return nil
}

type quotaTestReader struct {
	payload []byte
}

func (r *quotaTestReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.payload == nil {
		return nil, io.EOF
	}
	payload := append([]byte(nil), r.payload...)
	r.payload = nil
	return testMultiBuffer(payload), nil
}

func (r *quotaTestReader) ReadMultiBufferTimeout(time.Duration) (buf.MultiBuffer, error) {
	return r.ReadMultiBuffer()
}

func testMultiBuffer(payload []byte) buf.MultiBuffer {
	buffer := buf.New()
	_, _ = buffer.Write(payload)
	return buf.MultiBuffer{buffer}
}

func TestQuotaWriterChargesBeforeForwarding(t *testing.T) {
	consumer := &quotaTestConsumer{allowed: true}
	destination := &quotaTestWriter{}
	writer := NewQuotaWriter(destination, consumer, true)

	if err := writer.WriteMultiBuffer(testMultiBuffer([]byte("upload"))); err != nil {
		t.Fatalf("WriteMultiBuffer() error = %v", err)
	}
	if !consumer.upload || consumer.count != 6 || destination.calls != 1 {
		t.Fatalf("writer state = upload %v, count %d, calls %d",
			consumer.upload, consumer.count, destination.calls)
	}
}

func TestQuotaWriterRejectsBeforeForwarding(t *testing.T) {
	consumer := &quotaTestConsumer{allowed: false}
	destination := &quotaTestWriter{}
	writer := NewQuotaWriter(destination, consumer, false)

	if err := writer.WriteMultiBuffer(testMultiBuffer([]byte("download"))); err != ErrQuotaExhausted {
		t.Fatalf("WriteMultiBuffer() error = %v, want %v", err, ErrQuotaExhausted)
	}
	if consumer.upload || consumer.count != 8 || destination.calls != 0 {
		t.Fatalf("writer state = upload %v, count %d, calls %d",
			consumer.upload, consumer.count, destination.calls)
	}
}

func TestQuotaReaderChargesAndRejectsReadPayload(t *testing.T) {
	consumer := &quotaTestConsumer{allowed: false}
	reader := NewQuotaReader(&quotaTestReader{payload: []byte("upload")}, consumer, true)

	mb, err := reader.ReadMultiBufferTimeout(time.Second)
	if err != ErrQuotaExhausted {
		buf.ReleaseMulti(mb)
		t.Fatalf("ReadMultiBufferTimeout() error = %v, want %v", err, ErrQuotaExhausted)
	}
	if mb != nil {
		buf.ReleaseMulti(mb)
		t.Fatal("quota-rejected reader returned a payload")
	}
	if !consumer.upload || consumer.count != 6 {
		t.Fatalf("reader state = upload %v, count %d", consumer.upload, consumer.count)
	}
}
