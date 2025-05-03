package caddynotifier

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var caddyNotifierMetrics = struct {
	once             sync.Once
	eventSent        *prometheus.CounterVec
	subscribeRequest *prometheus.CounterVec
	activeConnection *prometheus.GaugeVec
	channelCount     *prometheus.GaugeVec
	upstreamStatus   *prometheus.GaugeVec
}{}

func initCaddyNotifierMetrics(registry *prometheus.Registry) {
	const ns, sub = "caddy", "websocket_notifier"

	labels := []string{"upstream"}
	caddyNotifierMetrics.once.Do(func() {
		caddyNotifierMetrics.eventSent = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "event_sent",
			Help:      "The number of event sent to websocket subscribers",
		}, labels)
		caddyNotifierMetrics.subscribeRequest = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "subscribe_requested",
			Help:      "The number of channel subscribe received from websocket subscribers",
		}, labels)
		caddyNotifierMetrics.activeConnection = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "active_connection",
			Help:      "The number of active websocket connection from websocket subscriber",
		}, labels)
		caddyNotifierMetrics.channelCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "channel_count",
			Help:      "The number of active channels subscribed by websocket subscriber",
		}, labels)
		caddyNotifierMetrics.upstreamStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "upstream_healthy",
			Help:      "Healthy status of upstream websocket connection",
		}, labels)
	})
	for _, c := range []prometheus.Collector{
		caddyNotifierMetrics.eventSent, caddyNotifierMetrics.subscribeRequest,
		caddyNotifierMetrics.activeConnection, caddyNotifierMetrics.channelCount,
		caddyNotifierMetrics.upstreamStatus} {
		if err := registry.Register(c); err != nil && !errors.Is(err, prometheus.AlreadyRegisteredError{
			ExistingCollector: c,
			NewCollector:      c,
		}) {
			panic(err)
		}
	}
}
