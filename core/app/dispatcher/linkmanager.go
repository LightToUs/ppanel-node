package dispatcher

import (
	sync "sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
	onClose func()
	once    sync.Once
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	var err error
	w.once.Do(func() {
		w.manager.RemoveWriter(w)
		if w.onClose != nil {
			w.onClose()
		}
		err = common.Close(w.writer)
	})
	return err
}

type LinkManager struct {
	links  map[*ManagedWriter]buf.Reader
	sealed bool
	mu     sync.Mutex
}

func NewLinkManager() *LinkManager {
	return &LinkManager{links: make(map[*ManagedWriter]buf.Reader)}
}

func (m *LinkManager) AddLink(writer *ManagedWriter, reader buf.Reader) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealed {
		return false
	}
	m.links[writer] = reader
	return true
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, writer)
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	m.sealed = true
	links := make(map[*ManagedWriter]buf.Reader, len(m.links))
	for writer, reader := range m.links {
		links[writer] = reader
	}
	clear(m.links)
	m.mu.Unlock()
	for w, r := range links {
		common.Close(w)
		common.Interrupt(r)
	}
}
