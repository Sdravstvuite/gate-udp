package lite

import (
    "sync/atomic"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/metric"
)

var (
    meter = otel.Meter("java/lite/udp")

    udpDatagramsIn  metric.Int64Counter
    udpDatagramsOut metric.Int64Counter
    udpBytesIn      metric.Int64Counter
    udpBytesOut     metric.Int64Counter
    udpActive       metric.Int64UpDownCounter

    initMetricsOnce atomic.Bool
)

func ensureMetrics() {
    if initMetricsOnce.Load() {
        return
    }
    // Best-effort: ignore errors (no-op provider by default)
    if c, err := meter.Int64Counter("gate.lite.udp.datagrams.in"); err == nil {
        udpDatagramsIn = c
    }
    if c, err := meter.Int64Counter("gate.lite.udp.datagrams.out"); err == nil {
        udpDatagramsOut = c
    }
    if c, err := meter.Int64Counter("gate.lite.udp.bytes.in"); err == nil {
        udpBytesIn = c
    }
    if c, err := meter.Int64Counter("gate.lite.udp.bytes.out"); err == nil {
        udpBytesOut = c
    }
    if c, err := meter.Int64UpDownCounter("gate.lite.udp.active_clients"); err == nil {
        udpActive = c
    }
    initMetricsOnce.Store(true)
}

func addDatagramsIn(n int64)  { ensureMetrics(); if udpDatagramsIn != nil { udpDatagramsIn.Add(nil, n) } }
func addDatagramsOut(n int64) { ensureMetrics(); if udpDatagramsOut != nil { udpDatagramsOut.Add(nil, n) } }
func addBytesIn(n int64)      { ensureMetrics(); if udpBytesIn != nil { udpBytesIn.Add(nil, n) } }
func addBytesOut(n int64)     { ensureMetrics(); if udpBytesOut != nil { udpBytesOut.Add(nil, n) } }
func addActiveClients(n int64){ ensureMetrics(); if udpActive != nil { udpActive.Add(nil, n) } }

// drops counter for limit exceed or errors that drop a packet before mapping
var udpDropsLimit metric.Int64Counter

func addDropsLimit(n int64) { ensureMetrics(); if udpDropsLimit == nil { if c, err := meter.Int64Counter("gate.lite.udp.drops.limit"); err == nil { udpDropsLimit = c } }; if udpDropsLimit != nil { udpDropsLimit.Add(nil, n) } }

