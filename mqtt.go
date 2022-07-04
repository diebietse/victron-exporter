package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
)

const serialTopic = "N/+/system/0/Serial"
const statsTopic = "N/#"
const pollTopicTemplate = "R/%s/system/0/Serial"

func tokenToErr(t mqtt.Token) error {
	return tokenToErrContext(context.Background(), t)
}

func tokenToErrContext(ctx context.Context, t mqtt.Token) error {
	select {
	case <-t.Done():
		return t.Error()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newTLSConfig() *tls.Config {
	// First, create the set of root certificates. For this example we only
	// have one. It's also possible to omit this in order to use the
	// default root set of the current operating system.
	roots := x509.NewCertPool()
	ok := roots.AppendCertsFromPEM([]byte(rootPEM))
	if !ok {
		panic("failed to parse root certificate")
	}

	// Create tls.Config with desired tls properties
	return &tls.Config{
		// RootCAs = certs used to verify server cert.
		RootCAs: roots,
		// ClientAuth = whether to request cert from server.
		// Since the server is set up for SSL, this happens
		// anyways.
		ClientAuth: tls.NoClientCert,
		// ClientCAs = certs used to validate client cert.
		ClientCAs: nil,
		// InsecureSkipVerify = verify that cert contents
		// match server. IP matches what is in cert etc.
		InsecureSkipVerify: true, //nolint:gosec
		// // Certificates = list of certs client sends to server.
		// Certificates: []tls.Certificate{cert},
	}
}

type mqttConnectionConfig struct {
	host     string
	port     int
	secure   bool
	username string
	password string
}

type victronClient struct {
	pollInterval time.Duration

	serial chan string
	client mqtt.Client

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newVictronClient(clientID string, pollInterval time.Duration, config mqttConnectionConfig) (*victronClient, error) {
	log.WithFields(log.Fields{
		"host": config.host,
		"port": config.port,
	}).Debug("connecting to mqtt")

	serialChan := make(chan string)

	onConnect := func(client mqtt.Client) {
		log.Info("mqtt connected, subscribing to topics...")
		// We need to subscribe after each connection
		// since mqtt does not maintain subscriptions across reconnects
		err := tokenToErr(client.Subscribe(serialTopic, 0, mqttSerialSubscriptionHandler(serialChan)))
		if err != nil {
			log.WithError(err).Error("failed to subscribe")
		}

		err = tokenToErr(client.Subscribe(statsTopic, 0, mqttSubscriptionHandler))
		if err != nil {
			log.WithError(err).Error("failed to subscribe")
		}
	}

	client := mqtt.NewClient(createClientOptions(clientID, config, onConnect))
	err := tokenToErr(client.Connect())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mqtt: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	vc := &victronClient{
		pollInterval: pollInterval,
		serial:       serialChan,
		client:       client,
		ctx:          ctx,
		cancel:       cancel,
	}
	vc.wg.Add(1)
	go vc.serialReader()

	return vc, nil
}

func (v *victronClient) Close() error {
	v.client.Disconnect(5000)
	v.cancel()
	v.wg.Wait()
	return nil
}

func newConnectionLostHandler(clientID string) mqtt.ConnectionLostHandler {
	return func(c mqtt.Client, e error) {
		log.WithFields(log.Fields{
			"client_id": clientID,
		}).WithError(e).Error("mqtt connection lost")
		connectionStatus.WithLabelValues(clientID).Set(0)
		connectionStatusSinceTimeSeconds.WithLabelValues(clientID).Set(float64(time.Now().Unix()))
	}
}

func newConnectionHandler(clientID string, wrapped mqtt.OnConnectHandler) mqtt.OnConnectHandler {
	return func(c mqtt.Client) {
		log.WithField("client_id", clientID).Info("mqtt connected")
		connectionStatus.WithLabelValues(clientID).Set(1)
		connectionStatusSinceTimeSeconds.WithLabelValues(clientID).Set(float64(time.Now().Unix()))

		if wrapped != nil {
			wrapped(c)
		}
	}
}

func createClientOptions(clientID string, config mqttConnectionConfig, onConnectionHandler mqtt.OnConnectHandler) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(1 * time.Minute)
	opts.SetWriteTimeout(30 * time.Second)
	opts.SetOrderMatters(false)
	opts.SetConnectionLostHandler(newConnectionLostHandler(clientID))
	opts.SetOnConnectHandler(newConnectionHandler(clientID, onConnectionHandler))

	if config.secure {
		opts.AddBroker(fmt.Sprintf("ssl://%s:%d", config.host, config.port))
		opts.SetTLSConfig(newTLSConfig())
	} else {
		opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.host, config.port))
	}

	if config.username != "" {
		opts.SetUsername(config.username)
	}

	if config.password != "" {
		opts.SetPassword(config.password)
	}

	opts.SetClientID(clientID)
	opts.SetCleanSession(true)

	return opts
}

type victronValue struct {
	Value *float64 `json:"value"`
}

type victronStringValue struct {
	Value string `json:"value"`
}

func mqttSubscriptionHandler(client mqtt.Client, msg mqtt.Message) {
	subscriptionsUpdatesTotal.Inc()

	topic := msg.Topic()
	topicParts := strings.Split(topic, "/")
	if len(topicParts) < 5 {
		subscriptionsUpdatesIgnoredTotal.Inc()

		return
	}
	topicInfoParts := topicParts[4:]

	componentType := topicParts[2]
	componentID := topicParts[3]

	topicString := strings.Join(topicInfoParts, "/")

	o, ok := suffixTopicMap[topicString]
	if !ok {
		subscriptionsUpdatesIgnoredTotal.Inc()
		return
	}

	var v victronValue

	err := json.Unmarshal(msg.Payload(), &v)
	if err != nil {
		log.Warn("failed to unmarshal victron mqtt payload: ", err)
		subscriptionsUpdatesIgnoredTotal.Inc()

		return
	}

	if v.Value == nil {
		o(componentType, componentID, math.NaN())
	} else {
		o(componentType, componentID, *v.Value)
	}
}

func mqttSerialSubscriptionHandler(c chan string) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		subscriptionsUpdatesTotal.Inc()

		var v victronStringValue

		err := json.Unmarshal(msg.Payload(), &v)
		if err != nil {
			log.Warn("failed to unmarshal victron serial mqtt payload: ", err)
			subscriptionsUpdatesIgnoredTotal.Inc()

			return
		}

		if v.Value != "" {
			c <- v.Value
		}

		return
	}
}

func (v *victronClient) serialReader() {
	defer v.wg.Done()
	wg := sync.WaitGroup{}
	pollerRunning := false
	for {
		var systemSerialID string
		select {
		case systemSerialID = <-v.serial:
			if !pollerRunning {
				wg.Add(1)
				go v.keepAliver(&wg, systemSerialID)
				pollerRunning = true
			}
		case <-v.ctx.Done():
			wg.Wait()
			return
		}
	}
}

func (v *victronClient) keepAliver(wg *sync.WaitGroup, systemSerialID string) {
	defer wg.Done()
	t := time.NewTicker(v.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			err := tokenToErrContext(v.ctx,
				v.client.Publish(fmt.Sprintf(pollTopicTemplate, systemSerialID), 1, false, ""))
			if err != nil {
				log.WithError(err).Error("mqtt publish failed")
			}
		case <-v.ctx.Done():
			return
		}
	}
}
