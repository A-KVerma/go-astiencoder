package astiencoder

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/asticode/go-astikit"
)

// Errors
var (
	ErrWorkflowNotFound = errors.New("astiencoder: workflow.not.found")
)

// WorkflowPool represents a workflow pool
type WorkflowPool struct {
	m  *sync.Mutex
	ws map[string]*Workflow
}

// NewWorkflowPool creates a new workflow pool
func NewWorkflowPool() *WorkflowPool {
	return &WorkflowPool{
		m:  &sync.Mutex{},
		ws: make(map[string]*Workflow),
	}
}

// AddWorkflow adds a new workflow
func (wp *WorkflowPool) AddWorkflow(w *Workflow) {
	wp.m.Lock()
	defer wp.m.Unlock()
	wp.ws[w.name] = w
}

// Workflow retrieves a workflow from the pool
func (wp *WorkflowPool) Workflow(name string) (w *Workflow, err error) {
	wp.m.Lock()
	defer wp.m.Unlock()
	var ok bool
	if w, ok = wp.ws[name]; !ok {
		err = ErrWorkflowNotFound
		return
	}
	return
}

// Workflows returns all the workflows
func (wp *WorkflowPool) Workflows() (ws []*Workflow) {
	wp.m.Lock()
	defer wp.m.Unlock()
	ws = []*Workflow{}
	for _, w := range wp.ws {
		ws = append(ws, w)
	}
	return
}

// Serve spawns the workflow pool server
func (wp *WorkflowPool) Serve(eh *EventHandler, pathWeb string, l astikit.StdLogger, fn func(http.Handler)) (err error) {
	// Create server
	var s *workflowPoolServer
	if s, err = newWorkflowPoolServer(wp, pathWeb, l); err != nil {
		err = fmt.Errorf("astiencoder: creating workflow pool server failed: %w", err)
		return
	}

	// Adapt event handler
	s.adaptEventHandler(eh)

	// Serve
	fn(s.handler())
	return
}
