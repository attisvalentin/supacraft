package pipeline

import "time"

// InitializeBuiltins injects built-in variables into the pipeline context.
// Call this immediately after NewPipelineContext() for all contexts, including child
// contexts created by function.call. Built-ins (e.g. {{now}}) are always available
// per spec; context isolation only prevents inheriting parent-assigned variables.
func InitializeBuiltins(pCtx *PipelineContext, loc *time.Location) {
	if loc == nil {
		loc = time.UTC
	}
	pCtx.Set("now", time.Now().In(loc))
}

// PipelineContext is the shared variable store during pipeline execution.
// Each step reads from and writes to this context.
type PipelineContext struct {
	vars map[string]any
}

func NewPipelineContext() *PipelineContext {
	return &PipelineContext{vars: make(map[string]any)}
}

func (pc *PipelineContext) Set(key string, value any) {
	pc.vars[key] = value
}

func (pc *PipelineContext) Get(key string) (any, bool) {
	v, ok := pc.vars[key]
	return v, ok
}

func (pc *PipelineContext) GetAll() map[string]any {
	return pc.vars
}
