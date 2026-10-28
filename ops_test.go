package ops_test

import (
	"context"
	gerrors "errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/getlantern/errors"
	"github.com/getlantern/ops"
)

func TestSuccess(t *testing.T) {
	var reportedFailure error
	var reportedCtx map[string]interface{}
	report := func(failure error, ctx map[string]interface{}) {
		reportedFailure = failure
		reportedCtx = ctx
	}

	ops.RegisterReporter(report)
	ops.SetGlobal("g", "g1")
	op := ops.Begin("test_success").Set("a", 1).SetDynamic("b", func() interface{} { return 2 })
	defer op.End()
	innerOp := op.Begin("inside")
	innerOp.FailIf(nil)
	innerOp.End()

	assert.Nil(t, reportedFailure)
	expectedCtx := map[string]interface{}{
		"op":      "inside",
		"root_op": "test_success",
		"g":       "g1",
		"a":       1,
		"b":       2,
	}
	assert.Equal(t, expectedCtx, reportedCtx)
}

func TestFailure(t *testing.T) {
	doTestFailure(t, false)
}

func TestCancel(t *testing.T) {
	doTestFailure(t, true)
}

func doTestFailure(t *testing.T, cancel bool) {
	var reportedFailure error
	var reportedCtx map[string]interface{}
	report := func(failure error, ctx map[string]interface{}) {
		reportedFailure = failure
		reportedCtx = ctx
	}

	ops.RegisterReporter(report)
	op := ops.Begin("test_failure")
	var wg sync.WaitGroup
	wg.Add(1)
	op.Go(func() {
		op.FailIf(gerrors.New("This error should be ignored"))
		op.FailIf(errors.New("I failed").With("errorcontext", 5))
		wg.Done()
	})
	wg.Wait()
	if cancel {
		op.Cancel()
	}
	op.End()

	if cancel {
		assert.Nil(t, reportedFailure)
		assert.Nil(t, reportedCtx)
	} else {
		assert.Contains(t, reportedFailure.Error(), "I failed")
		assert.Equal(t, 5, reportedCtx["errorcontext"])
	}
}

func TestSetParentContext(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	ops.EnableOpenTelemetry("test")
	t.Cleanup(func() {
		ops.SetParentContext(nil)
		otel.SetTracerProvider(previous)
	})

	parentCtx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	ops.SetParentContext(parentCtx)
	root := ops.Begin("root")
	child := root.Begin("child")
	child.End()
	// An Op begun on a goroutine the Op started keeps the Op as its parent.
	var wg sync.WaitGroup
	wg.Add(1)
	root.Go(func() {
		defer wg.Done()
		ops.Begin("in_go").End()
	})
	wg.Wait()
	root.End()
	parent.End()

	// An explicit trace to resume takes precedence over the parent.
	other := "0af7651916cd43dd8448eb211c80319c"
	resumed := ops.Resume("resumed", other)
	resumed.End()
	// The parent is tracing state, not operation metadata.
	_, exposed := ops.AsMap(nil, true)["_parentctx"]
	assert.False(t, exposed)
	// Clearing the parent returns later ops to traces of their own.
	ops.SetParentContext(context.Background())
	alone := ops.Begin("alone")
	alone.End()
	cleared := ops.Resume("cleared", other)
	cleared.End()

	byName := map[string]tracetest.SpanStub{}
	for _, span := range exporter.GetSpans() {
		byName[span.Name] = span
	}
	assert.Equal(t, byName["parent"].SpanContext.TraceID(), byName["root"].SpanContext.TraceID())
	assert.Equal(t, byName["parent"].SpanContext.SpanID(), byName["root"].Parent.SpanID())
	assert.Equal(t, byName["root"].SpanContext.SpanID(), byName["child"].Parent.SpanID())
	assert.Equal(t, byName["root"].SpanContext.SpanID(), byName["in_go"].Parent.SpanID())
	assert.Equal(t, other, byName["resumed"].SpanContext.TraceID().String())
	assert.NotEqual(t, byName["parent"].SpanContext.TraceID(), byName["alone"].SpanContext.TraceID())
	assert.False(t, byName["alone"].Parent.IsValid())
	assert.Equal(t, other, byName["cleared"].SpanContext.TraceID().String())
}
