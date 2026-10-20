package api

import (
	"io"
	"sync"

	"github.com/gosuda/rukie/internal/op"
)

type DocStore struct {
	raw []byte
	mu  sync.RWMutex
}

func (d *DocStore) Set(raw []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.raw = raw
}

func (d *DocStore) Get() []byte {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.raw
}

func RegisterAll(t *op.Table, docs *DocStore) error {
	if err := registerIndexOp(t); err != nil {
		return err
	}
	return registerOpenAPIOp(t, docs)
}

func RegisterViews(v *op.Views) {
	op.PageView(v, "api.index", func(w io.Writer, c op.ViewCtx, out IndexOut) error {
		return op.RenderReference(w, c, out.Operations)
	})
}
