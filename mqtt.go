package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
	"github.com/suprememoocow/victron-exporter/pkg/parser"
)

const serialTopic = "N/+/system/0/Serial"
const statsTopic = "N/#"
const pollTopicTemplate = "R/%s/system/0/Serial"
const pollPeriod = 10 * time.Second

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
	return &tls.Config{
		// The certificate is self signed, so no point in trying to verify against a know CA.
		InsecureSkipVerify: true, //nolint:gosec
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
	serial chan string
	client mqtt.Client
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func connectWait(client mqtt.Client) error {
	err := tokenToErr(client.Connect())
	if err != nil {
		return fmt.Errorf("failed to connect to mqtt: %w", err)
	}

	return nil
}

func NewVictronClient(clientID string, config mqttConnectionConfig) (*victronClient, error) {
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
			log.WithError(err).Error("failed to connect to mqtt")
		}
		err = tokenToErr(client.Subscribe(statsTopic, 0, mqttSubscriptionHandler))
		if err != nil {
			log.WithError(err).Error("failed to connect to mqtt")
		}
	}

	client := mqtt.NewClient(createClientOptions(clientID, config, onConnect))
	ctx, cancel := context.WithCancel(context.Background())
	vc := &victronClient{
		serial: serialChan,
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}
	vc.wg.Add(1)
	go vc.serialReader()

	return vc, connectWait(client)
}

func (v *victronClient) Close() error {
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
	opts.SetWriteTimeout(5 * time.Second)
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

	c, err := parser.ParseComponent(msg.Topic(), msg.Payload())
	if err != nil {
		subscriptionsUpdatesIgnoredTotal.Inc()
		if !errors.Is(err, parser.ErrTopicLengthTooShort) {
			fields := logrus.Fields{
				"topic":   msg.Topic(),
				"payload": string(msg.Payload()),
			}
			logrus.WithError(err).WithFields(fields).Error("Could not parse topic")
		}
		return
	}

	o, ok := suffixTopicMap[c.ComponentPath]
	if !ok {
		subscriptionsUpdatesIgnoredTotal.Inc()
		return
	}
	o(c.ComponentType, c.ComponentID, c.AsMetric())
}

func mqttSerialSubscriptionHandler(c chan string) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		subscriptionsUpdatesTotal.Inc()

		value, err := parser.ParsePayloadAsString(msg.Payload())
		if err != nil {
			fields := logrus.Fields{
				"topic":   msg.Topic(),
				"payload": string(msg.Payload()),
			}
			logrus.WithError(err).WithFields(fields).Error("Could not parse serial")
			subscriptionsUpdatesIgnoredTotal.Inc()

			return
		}

		if value != "" {
			c <- value
		}
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
				logrus.WithField("serial", systemSerialID).Info("Device serial found, starting monitoring")

				go v.keepAliver(&wg, systemSerialID)
				pollerRunning = true
				err := tokenToErr(v.client.Unsubscribe(serialTopic))
				if err != nil {
					log.WithError(err).Error("mqtt unsubscribe failed")
				}
			}
		case <-v.ctx.Done():
			wg.Wait()
			return
		}
	}
}

func (v *victronClient) keepAliver(wg *sync.WaitGroup, systemSerialID string) {
	defer wg.Done()
	t := time.NewTicker(pollPeriod)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			// err := tokenToErrContext(v.ctx,
			// 	v.client.Publish(fmt.Sprintf(pollTopicTemplate, systemSerialID), 1, false, ""))
			// if err != nil {
			// 	log.WithError(err).Error("mqtt publish failed")
			// }
			logrus.Info("Fake keep alive")
		case <-v.ctx.Done():
			return
		}
	}
}
