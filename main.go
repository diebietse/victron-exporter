package main

import (
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

var (
	listenAddress = flag.String("web.listen-address",
		getEnv("LISTEN_ADDR", "127.0.0.1:9226"),
		"Address on which to expose metrics and web interface.")

	clientPrefix = flag.String("mqtt.client_prefix",
		getEnv("MQTT_CLIENT_PREFIX", "victron_exporter"),
		"Prefix for MQTT clientID")

	pollInterval = flag.Duration("victron.poll_interval",
		getDurationEnv("VICTRON_POLL_INTERVAL", 10*time.Second),
		"CGX MQTT poll interval")

	host = flag.String("mqtt.host",
		getEnv("MQTT_HOST", ""),
		"CGX IP address or hostname")

	port = flag.Int("mqtt.port",
		getIntEnv("MQTT_PORT", 8883),
		"CGX MQTT PORT")

	secure = flag.Bool("mqtt.secure",
		getBoolEnv("MQTT_SECURE", true),
		"CGX SSL-enabled communication")

	username = flag.String("mqtt.username",
		getEnv("MQTT_USERNAME", ""),
		"Victron MQTT Cloud Username")

	password = flag.String("mqtt.password",
		getEnv("MQTT_PASSWORD", ""),
		"Victron MQTT Cloud Password")

	logLevel = flag.Int("log.level",
		getIntEnv("LOG_LEVEL", 2),
		"Log level: 0=debug, 1=info, 2=warn, 3=error")
)

func main() {
	flag.Parse()

	setLogLevel(*logLevel)

	log.WithField("address", *listenAddress).Info("victron_exporter listening")

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(*listenAddress, nil)
		if err != nil {
			log.WithField("address", *listenAddress).WithError(err).Fatal("failed to listen on address")
		}
	}()

	mqttOpts := mqttConnectionConfig{
		host:     *host,
		port:     *port,
		secure:   *secure,
		username: *username,
		password: *password,
	}

	client, err := newVictronClient(*clientPrefix, *pollInterval, mqttOpts)
	if err != nil {
		log.WithError(err).Fatal("failed to establish mqtt connection")
	}

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	go func() {
		time.Sleep(10 * time.Second)
		log.Fatal("graceful shutdown timed out")
	}()

	client.Close()
}
