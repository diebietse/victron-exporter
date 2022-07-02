package types

type Observer interface {
	SetConnectionStatus(connected bool)
	SubscriptionReceived()
	SubscriptionIgnored()
	LogComponentMetric(componentPath, componentType, componentID string, value float64)
}
