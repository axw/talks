package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// newHTTPHandler returns an instrumented net/http.Handler.
func newEcho() *echo.Echo {
	r := echo.New()
	r.Use(otelecho.Middleware("dice-server"))
	r.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (result error) {
			span := trace.SpanFromContext(c.Request().Context())
			defer func() {
				if v := recover(); v != nil {
					err := fmt.Errorf("panic: %v", v)
					span.RecordError(err, trace.WithStackTrace(true))
					span.SetStatus(codes.Error, "handler panicked")
					result = echo.ErrInternalServerError
				}
			}()
			if err := next(c); err != nil {
				span.RecordError(err, trace.WithStackTrace(true))
				return err
			}
			return nil
		}
	})

	rollCounter, err := meter.Int64Counter("dice_rolls")
	if err != nil {
		panic(err)
	}
	r.GET("/roll/:dice", func(c echo.Context) error {
		diceString := c.Param("dice")
		nString, sidesString, ok := strings.Cut(diceString, "d")
		if !ok {
			return fmt.Errorf("expected dice notation like 2d20, got %s", diceString)
		}
		n, err := strconv.ParseInt(nString, 10, 8)
		if err != nil {
			return fmt.Errorf("expected dice notation like 2d20, got %s: %w", diceString, err)
		}
		sides, err := strconv.ParseInt(sidesString, 10, 8)
		if err != nil {
			return fmt.Errorf("expected dice notation like 2d20, got %s: %w", diceString, err)
		}
		if n == 4 || sides == 4 {
			return fmt.Errorf("tetraphobic")
		}
		span := trace.SpanFromContext(c.Request().Context())
		span.AddEvent("rolling dice", trace.WithAttributes(
			attribute.Int64("n", n),
			attribute.Int64("sides", sides),
		))

		zipf := rand.NewZipf(
			rand.New(rand.NewSource(time.Now().UnixNano())), 2, 1,
			uint64(sides)-1,
		)

		var sum int64
		for range n {
			//roll := 1 + rand.Int63n(sides)
			roll := 1 + int64(zipf.Uint64()) // TODO use uniform distribution
			rollCounter.Add(c.Request().Context(), 1, metric.WithAttributes(
				// include the value as a dimension
				attribute.Int64("value", roll),
			))
			sum += roll
		}
		return c.String(http.StatusOK, strconv.FormatInt(sum, 10)+"\n")
	})
	return r
}

// BEGIN INIT METER PROVIDER OMIT

// meter is initially a no-op, hot-swapped when a global MeterProvider is
// registered by initMeterProvider.
var meter = otel.Meter("my/package/name")

func initMeterProvider() {
	// Set up a meter provider, exporting both to stdout and as OTLP.
	const interval = 10 * time.Second
	stdoutExporter, _ := stdoutmetric.New()
	otlpExporter, _ := otlpmetricgrpc.New(
		context.Background(),
		otlpmetricgrpc.WithTemporalitySelector(
			func(k sdkmetric.InstrumentKind) metricdata.Temporality {
				// Send all metrics as deltas, which are simpler
				// to deal with in Kibana.
				return metricdata.DeltaTemporality
			},
		),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(stdoutExporter, sdkmetric.WithInterval(interval)),
		),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				otlpExporter,
				sdkmetric.WithInterval(interval),
			),
		),
	)
	otel.SetMeterProvider(meterProvider)
}

// END INIT METER PROVIDER OMIT

// BEGIN INIT TRACER PROVIDER OMIT

// tracer is initially a no-op, hot-swapped when a global TracerProvider is
// registered by initTracerProvider.
var tracer = otel.Tracer("my/package/name")

// initTracerProvider registers a global TracerProvider.
func initTracerProvider() {
	// Set up propagator, for injecting trace context into and extracting
	// from HTTP headers, Kafka message headers, etc.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C Trace-Context
		propagation.Baggage{},      // W3C Baggage
	))

	// Set up a tracer provider, exporting both to stdout and as OTLP.
	stdoutExporter, _ := stdouttrace.New(stdouttrace.WithPrettyPrint())
	otlpExporter, _ := otlptracegrpc.New(context.Background())
	_ = otlpExporter
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(stdoutExporter),
		sdktrace.WithBatcher(otlpExporter),
	)
	otel.SetTracerProvider(tracerProvider)
}

// END INIT TRACER PROVIDER OMIT

func main() {
	initMeterProvider()
	initTracerProvider()

	r := newEcho()
	if err := r.Start("localhost:8080"); err != nil {
		log.Fatal(err)
	}
}
