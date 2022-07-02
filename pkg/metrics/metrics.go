package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "victron"

var (
	connectionStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "mqtt_connection_state",
		Help:      "0=Disconnected; 1=Connected",
	}, []string{"client_id"})

	connectionStatusSinceTimeSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "mqtt_connection_state_since_time_seconds",
		Help:      "Time since last change to mqtt_connection_state",
	}, []string{"client_id"})

	subscriptionsUpdatesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "mqtt_subscription_updates_total",
		Help:      "MQTT subscriptions updated received",
	}, []string{"client_id"})

	subscriptionsUpdatesIgnoredTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "mqtt_subscription_updates_ignored_total",
		Help:      "MQTT subscription updates ignored",
	}, []string{"client_id"})
)

type Metrics struct {
	clientID string
}

func New(clientID string) *Metrics {
	prometheus.MustRegister(connectionStatus)
	prometheus.MustRegister(connectionStatusSinceTimeSeconds)
	prometheus.MustRegister(subscriptionsUpdatesTotal)
	prometheus.MustRegister(subscriptionsUpdatesIgnoredTotal)

	return &Metrics{
		clientID: clientID,
	}
}

func (m *Metrics) SetConnectionStatus(connected bool) {
	status := float64(0)
	if connected {
		status = 1
	}
	connectionStatus.WithLabelValues(m.clientID).Set(status)
	connectionStatusSinceTimeSeconds.WithLabelValues(m.clientID).Set(float64(time.Now().Unix()))
}

func (m *Metrics) SubscriptionReceived() {
	subscriptionsUpdatesTotal.WithLabelValues(m.clientID).Inc()
}

func (m *Metrics) SubscriptionIgnored() {
	subscriptionsUpdatesIgnoredTotal.WithLabelValues(m.clientID).Inc()
}

func (m *Metrics) LogComponentMetric(componentPath, componentType, componentID string, value float64) {
	o, ok := suffixTopicMap[componentPath]
	if !ok {
		m.SubscriptionIgnored()
		return
	}

	o(componentType, componentID, value)
}
