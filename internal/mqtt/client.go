// Package mqtt connects the backend to the broker: it ingests ESP32 sensor data, publishes LED
// commands and receives the ESP32's command responses.
package mqtt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"iot-backend/internal/config"
	"iot-backend/internal/service"
)

// Handlers process incoming messages. They must not block for long.
type Handlers struct {
	SensorData     func(SensorPayload)
	DeviceResponse func(service.DeviceResponse)
}

type Client struct {
	cfg      config.MQTT
	client   paho.Client
	handlers Handlers
	logger   *slog.Logger

	// subscribed counts acknowledged subscriptions of the current connection.
	subscribed atomic.Int32
}

const publishTimeout = 5 * time.Second

// New configures a client that reconnects automatically and re-subscribes after every
// (re)connection. Call Connect to start it.
func New(cfg config.MQTT, handlers Handlers, logger *slog.Logger) *Client {
	c := &Client{cfg: cfg, handlers: handlers, logger: logger}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.Broker, cfg.Port)).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			logger.Warn("mqtt connection lost; reconnecting", "error", err)
		})
	c.client = paho.NewClient(opts)
	return c
}

// Connect starts connecting. It waits up to wait for the first connection; afterwards the
// client keeps retrying in the background, so the API stays available while the broker is down.
func (c *Client) Connect(wait time.Duration) {
	token := c.client.Connect()
	if !token.WaitTimeout(wait) {
		c.logger.Warn("mqtt broker not reachable yet; retrying in background",
			"broker", c.cfg.Broker, "port", c.cfg.Port)
		return
	}
	if err := token.Error(); err != nil {
		c.logger.Warn("mqtt connect failed; retrying in background", "error", err)
	}
}

func (c *Client) onConnect(client paho.Client) {
	c.logger.Info("mqtt connected", "broker", c.cfg.Broker, "port", c.cfg.Port)
	c.subscribed.Store(0)
	subscriptions := map[string]paho.MessageHandler{
		c.cfg.SensorTopic:   c.onSensorData,
		c.cfg.ResponseTopic: c.onDeviceResponse,
	}
	for topic, handler := range subscriptions {
		token := client.Subscribe(topic, c.cfg.QoS, handler)
		go func() {
			if token.WaitTimeout(publishTimeout) && token.Error() == nil {
				c.subscribed.Add(1)
				c.logger.Info("mqtt subscribed", "topic", topic)
				return
			}
			c.logger.Error("mqtt subscribe failed", "topic", topic, "error", token.Error())
		}()
	}
}

func (c *Client) onSensorData(_ paho.Client, msg paho.Message) {
	payload, err := ParseSensorPayload(msg.Payload())
	if err != nil {
		c.logger.Warn("dropping invalid sensor payload", "error", err, "payload", string(msg.Payload()))
		return
	}
	if len(payload.Rejected) > 0 {
		c.logger.Warn("ignoring invalid sensor fields", "fields", payload.Rejected)
	}
	c.handlers.SensorData(payload)
}

func (c *Client) onDeviceResponse(_ paho.Client, msg paho.Message) {
	resp, err := ParseDeviceResponse(msg.Payload())
	if err != nil {
		c.logger.Warn("dropping invalid device response", "error", err, "payload", string(msg.Payload()))
		return
	}
	c.handlers.DeviceResponse(resp)
}

// Ready reports whether the client is connected and subscribed to both inbound topics.
func (c *Client) Ready() bool {
	return c.client.IsConnectionOpen() && c.subscribed.Load() == 2
}

// PublishControl publishes an LED command on the device-control topic.
func (c *Client) PublishControl(ctx context.Context, payload []byte) error {
	if !c.client.IsConnectionOpen() {
		return errors.New("mqtt broker not connected")
	}
	token := c.client.Publish(c.cfg.ControlTopic, c.cfg.QoS, false, payload)
	select {
	case <-token.Done():
		return token.Error()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(publishTimeout):
		return errors.New("mqtt publish timed out")
	}
}

// Close disconnects, letting in-flight work finish for up to 250 ms.
func (c *Client) Close() {
	c.client.Disconnect(250)
}

// Disabled is the publisher used when MQTT_ENABLED=false: every command fails fast.
type Disabled struct{}

func (Disabled) PublishControl(context.Context, []byte) error {
	return errors.New("mqtt is disabled")
}
