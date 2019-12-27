package astilibav

import (
	"sync"

	"github.com/asticode/go-astiencoder"
	"github.com/asticode/go-astikit"
	"github.com/asticode/goav/avutil"
)

// FrameHandler represents a node that can handle a frame
type FrameHandler interface {
	astiencoder.Node
	HandleFrame(p *FrameHandlerPayload)
}

// FrameHandlerConnector represents an object that can connect/disconnect with a frame handler
type FrameHandlerConnector interface {
	Connect(next FrameHandler)
	Disconnect(next FrameHandler)
}

// FrameHandlerPayload represents a FrameHandler payload
type FrameHandlerPayload struct {
	Descriptor Descriptor
	Frame      *avutil.Frame
	Node       astiencoder.Node
}

type frameDispatcher struct {
	c            *astikit.Closer
	eh           *astiencoder.EventHandler
	hs           map[string]FrameHandler
	m            *sync.Mutex
	n            astiencoder.Node
	p            *framePool
	statDispatch *astikit.DurationPercentageStat
	wg           *sync.WaitGroup
}

func newFrameDispatcher(n astiencoder.Node, eh *astiencoder.EventHandler, c *astikit.Closer) *frameDispatcher {
	return &frameDispatcher{
		c:            c,
		eh:           eh,
		hs:           make(map[string]FrameHandler),
		m:            &sync.Mutex{},
		n:            n,
		p:            newFramePool(c),
		statDispatch: astikit.NewDurationPercentageStat(),
		wg:           &sync.WaitGroup{},
	}
}

func (d *frameDispatcher) addHandler(h FrameHandler) {
	d.m.Lock()
	defer d.m.Unlock()
	d.hs[h.Metadata().Name] = h
}

func (d *frameDispatcher) delHandler(h FrameHandler) {
	d.m.Lock()
	defer d.m.Unlock()
	delete(d.hs, h.Metadata().Name)
}

func (d *frameDispatcher) dispatch(f *avutil.Frame, descriptor Descriptor) {
	// Copy handlers
	d.m.Lock()
	var hs []FrameHandler
	for _, h := range d.hs {
		hs = append(hs, h)
	}
	d.m.Unlock()

	// No handlers
	if len(hs) == 0 {
		return
	}

	// Wait for all previous subprocesses to be done
	// In case a brave soul tries to update this logic so that several packet can be sent to handlers in parallel, bare
	// in mind that packets must be sent in order whereas sending packets in goroutines doesn't keep this order
	d.statDispatch.Begin()
	d.wait()
	d.statDispatch.End()

	// Add subprocesses
	d.wg.Add(len(hs))

	// Loop through handlers
	for _, h := range hs {
		// Copy frame
		hF := d.p.get()
		if ret := avutil.AvFrameRef(hF, f); ret < 0 {
			emitAvError(d, d.eh, ret, "avutil.AvFrameRef failed")
			d.wg.Done()
			continue
		}

		// Handle frame
		go func(h FrameHandler) {
			defer d.wg.Done()
			defer d.p.put(hF)
			h.HandleFrame(&FrameHandlerPayload{
				Descriptor: descriptor,
				Frame:      hF,
				Node:       d.n,
			})
		}(h)
	}
}

func (d *frameDispatcher) wait() {
	d.wg.Wait()
}

func (d *frameDispatcher) addStats(s *astikit.Stater) {
	// Add wait time
	s.AddStat(astikit.StatMetadata{
		Description: "Percentage of time spent waiting for first child to finish processing dispatched frame",
		Label:       "Dispatch ratio",
		Unit:        "%",
	}, d.statDispatch)
}

type framePool struct {
	c *astikit.Closer
	m *sync.Mutex
	p []*avutil.Frame
}

func newFramePool(c *astikit.Closer) *framePool {
	return &framePool{
		c: c,
		m: &sync.Mutex{},
	}
}

func (p *framePool) get() (f *avutil.Frame) {
	p.m.Lock()
	defer p.m.Unlock()
	if len(p.p) == 0 {
		f = avutil.AvFrameAlloc()
		p.c.Add(func() error {
			avutil.AvFrameFree(f)
			return nil
		})
		return
	}
	f = p.p[0]
	p.p = p.p[1:]
	return
}

func (p *framePool) put(f *avutil.Frame) {
	p.m.Lock()
	defer p.m.Unlock()
	avutil.AvFrameUnref(f)
	p.p = append(p.p, f)
}
