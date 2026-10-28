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
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	ops.EnableOpenTelemetry("test")
	defer ops.SetParentContext(context.Background())

	parentCtx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	ops.SetParentContext(parentCtx)
	root := ops.Begin("root")
	child := root.Begin("child")
	child.End()
	root.End()
	parent.End()

	byName := map[string]tracetest.SpanStub{}
	for _, span := range exporter.GetSpans() {
		byName[span.Name] = span
	}
	assert.Equal(t, byName["parent"].SpanContext.TraceID(), byName["root"].SpanContext.TraceID())
	assert.Equal(t, byName["parent"].SpanContext.SpanID(), byName["root"].Parent.SpanID())
	assert.Equal(t, byName["root"].SpanContext.SpanID(), byName["child"].Parent.SpanID())
}
