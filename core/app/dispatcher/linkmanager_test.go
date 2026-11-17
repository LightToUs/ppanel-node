package dispatcher

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/common/buf"
)

type linkTestWriter struct {
	closed atomic.Bool
}

func (w *linkTestWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	return nil
}

func (w *linkTestWriter) Close() error {
	w.closed.Store(true)
	return nil
}

type linkTestReader struct {
	interrupted atomic.Bool
}

func (r *linkTestReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return nil, io.EOF
}

func (r *linkTestReader) Interrupt() {
	r.interrupted.Store(true)
}

func TestLinkManagerSealClosesOrRejectsConcurrentLinks(t *testing.T) {
	const links = 100
	manager := NewLinkManager()
	start := make(chan struct{})
	writers := make([]*linkTestWriter, links)
	readers := make([]*linkTestReader, links)
	var callbacks atomic.Int64
	var wg sync.WaitGroup

	for index := range links {
		writers[index] = &linkTestWriter{}
		readers[index] = &linkTestReader{}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			managed := &ManagedWriter{
				writer:  writers[index],
				manager: manager,
				onClose: func() {
					callbacks.Add(1)
				},
			}
			if !manager.AddLink(managed, readers[index]) {
				_ = managed.Close()
			}
		}(index)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		manager.CloseAll()
	}()
	close(start)
	wg.Wait()
	manager.CloseAll()

	manager.mu.Lock()
	sealed := manager.sealed
	remaining := len(manager.links)
	manager.mu.Unlock()
	if !sealed || remaining != 0 {
		t.Fatalf("manager state = sealed %v, links %d; want sealed with no links",
			sealed, remaining)
	}
	if callbacks.Load() != links {
		t.Fatalf("close callbacks = %d, want %d", callbacks.Load(), links)
	}
	for index := range links {
		if !writers[index].closed.Load() {
			t.Fatalf("writer %d remained open", index)
		}
	}

	rejected := &ManagedWriter{writer: &linkTestWriter{}, manager: manager}
	if manager.AddLink(rejected, &linkTestReader{}) {
		t.Fatal("sealed manager accepted a new link")
	}
}
