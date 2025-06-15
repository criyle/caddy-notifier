package caddynotifier

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var caddyNotifierMetrics = struct {
	once                     sync.Once
	eventSent                *prometheus.CounterVec
	subscribeRequest         *prometheus.CounterVec
	activeConnection         *prometheus.GaugeVec
	channelCount             *prometheus.GaugeVec
	upstreamStatus           *prometheus.GaugeVec
	websocketInboundBytes    *prometheus.CounterVec
	websocketOutboundBytes   *prometheus.CounterVec
	websocketCompressedBytes *prometheus.CounterVec
	categorizedSubscriber    *prometheus.GaugeVec
}{}

func initCaddyNotifierMetrics(registry *prometheus.Registry) {
	const ns, sub = "caddy", "websocket_notifier"

	labels := []string{"upstream"}
	caddyNotifierMetrics.once.Do(func() {
		caddyNotifierMetrics.eventSent = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "event_sent_total",
			Help:      "The number of event sent to websocket subscribers",
		}, labels)
		caddyNotifierMetrics.subscribeRequest = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "subscribe_requested_total",
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
		caddyNotifierMetrics.websocketInboundBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "websocket_inbound_bytes_total",
			Help:      "Total websocket inbound message size",
		}, labels)
		caddyNotifierMetrics.websocketOutboundBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "websocket_outbound_bytes_total",
			Help:      "Total websocket outbound message size",
		}, labels)
		caddyNotifierMetrics.websocketCompressedBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "websocket_outbound_compressed_bytes_total",
			Help:      "Total websocket outbound message size after compressed by shorty",
		}, labels)
		caddyNotifierMetrics.categorizedSubscriber = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "subscriber_count",
			Help:      "The number of subscriber of channels in each category",
		}, []string{"upstream", "category"})
	})
	for _, c := range []prometheus.Collector{
		caddyNotifierMetrics.eventSent, caddyNotifierMetrics.subscribeRequest,
		caddyNotifierMetrics.activeConnection, caddyNotifierMetrics.channelCount,
		caddyNotifierMetrics.upstreamStatus, caddyNotifierMetrics.websocketInboundBytes,
		caddyNotifierMetrics.websocketOutboundBytes, caddyNotifierMetrics.websocketCompressedBytes,
		caddyNotifierMetrics.categorizedSubscriber} {
		if err := registry.Register(c); err != nil && !errors.Is(err, prometheus.AlreadyRegisteredError{
			ExistingCollector: c,
			NewCollector:      c,
		}) {
			panic(err)
		}
	}
}

func updateMetrics(hub *messageHub, upstream string) {
	caddyNotifierMetrics.eventSent.WithLabelValues(upstream).Add(float64(hub.eventSent))
	hub.eventSent = 0
	caddyNotifierMetrics.subscribeRequest.WithLabelValues(upstream).Add(float64(hub.subscribeRequested))
	hub.subscribeRequested = 0
	caddyNotifierMetrics.activeConnection.WithLabelValues(upstream).Set(float64(len(hub.websocketChannel)))
	caddyNotifierMetrics.channelCount.WithLabelValues(upstream).Set(float64(len(hub.channels)))
	inbound := websocketInboundBytes.Swap(0)
	caddyNotifierMetrics.websocketInboundBytes.WithLabelValues(upstream).Add(float64(inbound))
	outbound := websocketOutboundBytes.Swap(0)
	caddyNotifierMetrics.websocketOutboundBytes.WithLabelValues(upstream).Add(float64(outbound))
	compressed := websocketCompressedBytes.Swap(0)
	caddyNotifierMetrics.websocketCompressedBytes.WithLabelValues(upstream).Add(float64(compressed))
	updateCategorySubscriber(hub, upstream)
}

func updateCategorySubscriber(hub *messageHub, upstream string) {
	counts := make(map[string]int, len(hub.channelCategory))
	for ch, cate := range hub.channelCategoryMap {
		counts[cate] += len(hub.channels[ch])
	}
	for cate := range hub.categorySet {
		caddyNotifierMetrics.categorizedSubscriber.WithLabelValues(upstream, cate).Set(float64(counts[cate]))
	}
}
