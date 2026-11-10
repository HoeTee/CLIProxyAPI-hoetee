package usage

import (
	"context"
	"sync"
	"time"
)

// HistoryRecorder receives one completed model request and its separate upstream token observations.
type HistoryRecorder interface {
	RecordRequest(HistoryResult)
	RecordTokens(id string, at time.Time, record Record)
}

type HistoryResult struct {
	ID, ClientID, Provider, Model, Outcome string
	At                                     time.Time
	StatusCode                             int
}

type historyContextKey struct{}

type historyContext struct {
	request         *HistoryRequest
	observeProvider bool
}

type HistoryRequest struct {
	recorder HistoryRecorder
	result   HistoryResult
	mu       sync.Mutex
	once     sync.Once
}

// BeginHistoryRequest correlates retries and nested executor calls with their top-level request.
func BeginHistoryRequest(ctx context.Context, recorder HistoryRecorder, result HistoryResult) (context.Context, *HistoryRequest) {
	if ctx == nil || recorder == nil {
		return ctx, nil
	}
	if current := historyFromContext(ctx); current.request != nil {
		return context.WithValue(ctx, historyContextKey{}, historyContext{request: current.request}), nil
	}
	if result.ClientID == "" {
		return ctx, nil
	}
	request := &HistoryRequest{recorder: recorder, result: result}
	return context.WithValue(ctx, historyContextKey{}, historyContext{request: request, observeProvider: true}), request
}

func historyFromContext(ctx context.Context) historyContext {
	if ctx != nil {
		if current, ok := ctx.Value(historyContextKey{}).(historyContext); ok {
			return current
		}
	}
	return historyContext{}
}

func ObserveHistoryProvider(ctx context.Context, provider string) {
	current := historyFromContext(ctx)
	if current.request == nil || !current.observeProvider || provider == "" {
		return
	}
	current.request.mu.Lock()
	current.request.result.Provider = provider
	current.request.mu.Unlock()
}

func (r *HistoryRequest) Complete(outcome string, statusCode int) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		result := r.result
		r.mu.Unlock()
		result.Outcome = outcome
		result.StatusCode = statusCode
		r.recorder.RecordRequest(result)
	})
}

func observeHistoryTokens(ctx context.Context, record Record) {
	if request := historyFromContext(ctx).request; request != nil {
		request.recorder.RecordTokens(request.result.ID, request.result.At, record)
	}
}

// ReportedTokenFields distinguishes explicitly reported zero counts from absent fields.
type ReportedTokenFields uint8

const (
	ReportedInput ReportedTokenFields = 1 << iota
	ReportedOutput
	ReportedCacheRead
)
