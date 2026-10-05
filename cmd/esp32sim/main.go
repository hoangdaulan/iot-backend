// Command esp32sim simulates the ESP32 over MQTT: it publishes temperature, humidity and light
// readings and answers LED commands, so the backend can be exercised without hardware.
//
//	go run ./cmd/esp32sim                      # broker localhost:1883, a reading every 5s
//	go run ./cmd/esp32sim -interval 1s -fail 0.2
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	var (
		broker      = flag.String("broker", "localhost:1883", "MQTT broker host:port")
		username    = flag.String("username", os.Getenv("MQTT_USERNAME"), "MQTT username")
		password    = flag.String("password", os.Getenv("MQTT_PASSWORD"), "MQTT password")
		sensorTopic = flag.String("sensor-topic", "myiot/esp32/sensor-data", "topic to publish readings on")
		controlTop  = flag.String("control-topic", "myiot/esp32/device-control", "topic to receive commands on")
		respTopic   = flag.String("response-topic", "myiot/esp32/device-response", "topic to answer commands on")
		interval    = flag.Duration("interval", 5*time.Second, "time between sensor readings")
		delay       = flag.Duration("delay", 300*time.Millisecond, "time the LED takes to react to a command")
		failRate    = flag.Float64("fail", 0, "probability (0-1) that a command is answered FAILED")
		silent      = flag.Bool("silent", false, "never answer commands (backend reports TIMEOUT)")
	)
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	led := "OFF"
	opts := mqtt.NewClientOptions().
		AddBroker("tcp://" + *broker).
		SetClientID(fmt.Sprintf("esp32-sim-%d", os.Getpid())).
		SetUsername(*username).SetPassword(*password).
		SetAutoReconnect(true).SetCleanSession(true)

	opts.OnConnect = func(c mqtt.Client) {
		logger.Info("connected", "broker", *broker)
		token := c.Subscribe(*controlTop, 1, func(c mqtt.Client, m mqtt.Message) {
			go handleCommand(c, m.Payload(), &led, *respTopic, *delay, *failRate, *silent, logger)
		})
		if token.Wait() && token.Error() != nil {
			logger.Error("subscribe", "error", token.Error())
		}
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) { logger.Warn("connection lost", "error", err) }

	client := mqtt.NewClient(opts)
	if t := client.Connect(); t.Wait() && t.Error() != nil {
		logger.Error("connect", "error", t.Error())
		os.Exit(1)
	}
	defer client.Disconnect(250)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	publishReading(client, *sensorTopic, logger)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			publishReading(client, *sensorTopic, logger)
		case <-ctx.Done():
			logger.Info("stopping")
			return
		}
	}
}

// publishReading sends a plausible reading: slow sine drifts plus noise.
func publishReading(c mqtt.Client, topic string, logger *slog.Logger) {
	phase := float64(time.Now().Unix()) / 600 * 2 * math.Pi
	msg := map[string]any{
		"temperature": round(27 + 4*math.Sin(phase) + rand.NormFloat64()*0.2),
		"humidity":    round(math.Min(100, math.Max(0, 60+10*math.Cos(phase)+rand.NormFloat64()*0.5))),
		"light":       math.Max(0, math.Round(400+300*math.Sin(phase/2)+rand.NormFloat64()*10)),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	payload, _ := json.Marshal(msg)
	if t := c.Publish(topic, 1, false, payload); t.Wait() && t.Error() != nil {
		logger.Error("publish reading", "error", t.Error())
		return
	}
	logger.Info("sensor-data", "payload", string(payload))
}

func handleCommand(c mqtt.Client, raw []byte, led *string, topic string, delay time.Duration,
	failRate float64, silent bool, logger *slog.Logger) {
	var cmd struct {
		ActionID int64  `json:"actionId"`
		Command  string `json:"command"`
	}
	if err := json.Unmarshal(raw, &cmd); err != nil || (cmd.Command != "ON" && cmd.Command != "OFF") {
		logger.Warn("ignoring bad command", "payload", string(raw))
		return
	}
	logger.Info("device-control", "actionId", cmd.ActionID, "command", cmd.Command)
	if silent {
		return
	}
	time.Sleep(delay)

	resp := map[string]any{"actionId": cmd.ActionID, "status": "SUCCESS", "state": cmd.Command,
		"message": "LED " + cmd.Command}
	if rand.Float64() < failRate {
		resp = map[string]any{"actionId": cmd.ActionID, "status": "FAILED", "state": *led,
			"message": "Simulated LED fault"}
	} else {
		*led = cmd.Command
	}
	payload, _ := json.Marshal(resp)
	if t := c.Publish(topic, 1, false, payload); t.Wait() && t.Error() != nil {
		logger.Error("publish response", "error", t.Error())
		return
	}
	logger.Info("device-response", "payload", string(payload))
}

func round(v float64) float64 { return math.Round(v*10) / 10 }
