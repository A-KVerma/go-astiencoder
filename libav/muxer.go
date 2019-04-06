package astilibav

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	astiencoder "github.com/asticode/go-astiencoder"
	astidefer "github.com/asticode/go-astitools/defer"
	astistat "github.com/asticode/go-astitools/stat"
	astisync "github.com/asticode/go-astitools/sync"
	astiworker "github.com/asticode/go-astitools/worker"
	"github.com/asticode/goav/avformat"
	"github.com/pkg/errors"
)

var countMuxer uint64

// Muxer represents an object capable of muxing packets into an output
type Muxer struct {
	*astiencoder.BaseNode
	c                *astidefer.Closer
	ctxFormat        *avformat.Context
	eh               *astiencoder.EventHandler
	o                *sync.Once
	q                *astisync.CtxQueue
	restamper        PktRestamper
	statIncomingRate *astistat.IncrementStat
	statWorkRatio    *astistat.DurationRatioStat
}

// MuxerOptions represents muxer options
type MuxerOptions struct {
	Format     *avformat.OutputFormat
	FormatName string
	Node       astiencoder.NodeOptions
	Restamper  PktRestamper
	URL        string
}

// NewMuxer creates a new muxer
func NewMuxer(o MuxerOptions, eh *astiencoder.EventHandler, c *astidefer.Closer) (m *Muxer, err error) {
	// Extend node metadata
	count := atomic.AddUint64(&countMuxer, uint64(1))
	o.Node.Metadata = o.Node.Metadata.Extend(fmt.Sprintf("muxer_%d", count), fmt.Sprintf("Muxer #%d", count), fmt.Sprintf("Muxes to %s", o.URL))

	// Create muxer
	m = &Muxer{
		c:                c,
		eh:               eh,
		o:                &sync.Once{},
		q:                astisync.NewCtxQueue(),
		restamper:        o.Restamper,
		statIncomingRate: astistat.NewIncrementStat(),
		statWorkRatio:    astistat.NewDurationRatioStat(),
	}
	m.BaseNode = astiencoder.NewBaseNode(o.Node, astiencoder.NewEventGeneratorNode(m), eh)
	m.addStats()

	// Alloc format context
	// We need to create an intermediate variable to avoid "cgo argument has Go pointer to Go pointer" errors
	var ctxFormat *avformat.Context
	if ret := avformat.AvformatAllocOutputContext2(&ctxFormat, o.Format, o.FormatName, o.URL); ret < 0 {
		err = errors.Wrapf(NewAvError(ret), "astilibav: avformat.AvformatAllocOutputContext2 on %+v failed", o)
		return
	}
	m.ctxFormat = ctxFormat

	// Make sure the format ctx is properly closed
	c.Add(func() error {
		m.ctxFormat.AvformatFreeContext()
		return nil
	})

	// This is a file
	if m.ctxFormat.Flags()&avformat.AVFMT_NOFILE == 0 {
		// Open
		var ctxAvIO *avformat.AvIOContext
		if ret := avformat.AvIOOpen(&ctxAvIO, o.URL, avformat.AVIO_FLAG_WRITE); ret < 0 {
			err = errors.Wrapf(NewAvError(ret), "astilibav: avformat.AvIOOpen on %+v failed", o)
			return
		}

		// Set pb
		m.ctxFormat.SetPb(ctxAvIO)

		// Make sure the avio ctx is properly closed
		c.Add(func() error {
			if ret := avformat.AvIOClosep(&ctxAvIO); ret < 0 {
				return errors.Wrapf(NewAvError(ret), "astilibav: avformat.AvIOClosep on %+v failed", o)
			}
			return nil
		})
	}
	return
}

func (m *Muxer) addStats() {
	// Add incoming rate
	m.Stater().AddStat(astistat.StatMetadata{
		Description: "Number of packets coming in per second",
		Label:       "Incoming rate",
		Unit:        "pps",
	}, m.statIncomingRate)

	// Add work ratio
	m.Stater().AddStat(astistat.StatMetadata{
		Description: "Percentage of time spent doing some actual work",
		Label:       "Work ratio",
		Unit:        "%",
	}, m.statWorkRatio)

	// Add queue stats
	m.q.AddStats(m.Stater())
}

// CtxFormat returns the format ctx
func (m *Muxer) CtxFormat() *avformat.Context {
	return m.ctxFormat
}

// Start starts the muxer
func (m *Muxer) Start(ctx context.Context, t astiencoder.CreateTaskFunc) {
	m.BaseNode.Start(ctx, t, func(t *astiworker.Task) {
		// Handle context
		go m.q.HandleCtx(m.Context())

		// Make sure to write header once
		var ret int
		m.o.Do(func() { ret = m.ctxFormat.AvformatWriteHeader(nil) })
		if ret < 0 {
			emitAvError(m, m.eh, ret, "m.ctxFormat.AvformatWriteHeader on %s failed", m.ctxFormat.Filename())
			return
		}

		// Write trailer once everything is done
		m.c.Add(func() error {
			if ret := m.ctxFormat.AvWriteTrailer(); ret < 0 {
				return errors.Wrapf(NewAvError(ret), "m.ctxFormat.AvWriteTrailer on %s failed", m.ctxFormat.Filename())
			}
			return nil
		})

		// Make sure to stop the queue properly
		defer m.q.Stop()

		// Start queue
		m.q.Start(func(dp interface{}) {
			// Handle pause
			defer m.HandlePause()

			// Assert payload
			p := dp.(pktHandlerPayloadRetriever)()

			// Increment incoming rate
			m.statIncomingRate.Add(1)

			// Restamp
			if m.restamper != nil {
				m.restamper.Restamp(p.Pkt)
			}

			// Write frame
			m.statWorkRatio.Add(true)
			if ret := m.ctxFormat.AvInterleavedWriteFrame((*avformat.Packet)(unsafe.Pointer(p.Pkt))); ret < 0 {
				m.statWorkRatio.Done(true)
				emitAvError(m, m.eh, ret, "m.ctxFormat.AvInterleavedWriteFrame failed")
				return
			}
			m.statWorkRatio.Done(true)
		})
	})
}

// MuxerPktHandler is an object that can handle a pkt for the muxer
type MuxerPktHandler struct {
	*Muxer
	o *avformat.Stream
}

// NewHandler creates
func (m *Muxer) NewPktHandler(o *avformat.Stream) *MuxerPktHandler {
	return &MuxerPktHandler{
		Muxer: m,
		o:     o,
	}
}

// HandlePkt implements the PktHandler interface
func (h *MuxerPktHandler) HandlePkt(p *PktHandlerPayload) {
	// Send pkt
	h.q.Send(h.pktHandlerPayloadRetriever(p))
}

func (h *MuxerPktHandler) pktHandlerPayloadRetriever(p *PktHandlerPayload) pktHandlerPayloadRetriever {
	return func() *PktHandlerPayload {
		// Rescale timestamps
		p.Pkt.AvPacketRescaleTs(p.Descriptor.TimeBase(), h.o.TimeBase())

		// Set stream index
		p.Pkt.SetStreamIndex(h.o.Index())
		return p
	}
}
