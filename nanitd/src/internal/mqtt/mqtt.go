// Package mqtt provides MQTT publishing for Home Assistant integration.
// It defines the Publisher interface and includes both a StubPublisher (no-op)
// and a full HAPublisher that connects to an MQTT broker, publishes HA
// auto-discovery configs, relays commands to the camera, and forwards state
// updates from the EventBus.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/trymwestin/nanit/internal/core/state"
)

// ---------------------------------------------------------------------------
// Publisher interface
// ---------------------------------------------------------------------------

// Publisher sends events and state to an MQTT broker.
type Publisher interface {
	// Start begins publishing events from the event bus.
	Start(ctx context.Context) error
	// Stop shuts down the publisher.
	Stop(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// StubPublisher (no-op, used when MQTT is disabled)
// ---------------------------------------------------------------------------

// StubPublisher is a no-op publisher for when MQTT is not configured.
type StubPublisher struct {
	log *slog.Logger
}

// NewStubPublisher creates a no-op MQTT publisher.
func NewStubPublisher(log *slog.Logger) *StubPublisher {
	return &StubPublisher{log: log}
}

// Start is a no-op.
func (s *StubPublisher) Start(_ context.Context) error {
	s.log.Info("MQTT publisher disabled (stub)")
	return nil
}

// Stop is a no-op.
func (s *StubPublisher) Stop(_ context.Context) error {
	return nil
}

// Ensure StubPublisher implements Publisher.
var _ Publisher = (*StubPublisher)(nil)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// MQTTConfig holds MQTT publisher configuration.
type MQTTConfig struct {
	Broker      string `yaml:"broker"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	TopicPrefix string `yaml:"topic_prefix"`
	DeviceID    string `yaml:"device_id"`
	BabyName    string `yaml:"baby_name"`
	CameraModel string `yaml:"camera_model"`
}

// PublishConfig is the legacy config alias (kept for backward compatibility).
type PublishConfig = MQTTConfig

// ---------------------------------------------------------------------------
// CameraCommander – abstraction over camera control methods
// ---------------------------------------------------------------------------

// CameraCommander sends commands to the camera without importing the camera
// package directly.
type CameraCommander interface {
	SetNightLight(ctx context.Context, on bool) error
	SetSleepMode(ctx context.Context, enabled bool) error
	SetVolume(ctx context.Context, level int32) error
	SetMicMute(ctx context.Context, muted bool) error
	SetStatusLED(ctx context.Context, enabled bool) error
}

// ---------------------------------------------------------------------------
// HAPublisher – full Home Assistant MQTT implementation
// ---------------------------------------------------------------------------

// Ensure HAPublisher implements Publisher at compile time.
var _ Publisher = (*HAPublisher)(nil)

// HAPublisher publishes Home Assistant auto-discovery configs, subscribes to
// command topics and relays commands to the camera, and forwards state updates
// from the EventBus.
type HAPublisher struct {
	cfg   MQTTConfig
	cam   CameraCommander
	store state.StateReader
	bus   *state.EventBus
	log   *slog.Logger

	client pahomqtt.Client

	unsub func() // EventBus unsubscribe
	stopC chan struct{}
	wg    sync.WaitGroup
}

// NewHAPublisher creates a new Home Assistant MQTT publisher.
func NewHAPublisher(cfg MQTTConfig, cam CameraCommander, store state.StateReader, bus *state.EventBus, log *slog.Logger) *HAPublisher {
	return &HAPublisher{
		cfg:   cfg,
		cam:   cam,
		store: store,
		bus:   bus,
		log:   log,
		stopC: make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start connects to the MQTT broker, publishes discovery configs, subscribes
// to command topics, publishes initial state, and starts listening on the
// EventBus for real-time updates.
func (p *HAPublisher) Start(_ context.Context) error {
	availTopic := p.topic("status")

	opts := pahomqtt.NewClientOptions().
		AddBroker(p.cfg.Broker).
		SetClientID(fmt.Sprintf("nanit-%s", p.cfg.DeviceID)).
		SetUsername(p.cfg.Username).
		SetPassword(p.cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetWill(availTopic, "offline", 1, true).
		SetOnConnectHandler(func(_ pahomqtt.Client) {
			p.log.Info("MQTT connected, publishing discovery and state")
			p.onConnect()
		}).
		SetConnectionLostHandler(func(_ pahomqtt.Client, err error) {
			p.log.Warn("MQTT connection lost", "error", err)
		})

	p.client = pahomqtt.NewClient(opts)

	token := p.client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}

	// Subscribe to EventBus.
	evtCh, unsub := p.bus.Subscribe(128)
	p.unsub = unsub

	p.wg.Add(1)
	go p.eventLoop(evtCh)

	p.log.Info("MQTT publisher started", "broker", p.cfg.Broker)
	return nil
}

// Stop gracefully disconnects from the MQTT broker and stops the event loop.
func (p *HAPublisher) Stop(_ context.Context) error {
	p.log.Info("MQTT publisher stopping")

	// Signal event loop to exit.
	close(p.stopC)

	// Unsubscribe from EventBus (will close channel and drain).
	if p.unsub != nil {
		p.unsub()
	}

	p.wg.Wait()

	if p.client != nil && p.client.IsConnected() {
		// Publish offline before disconnecting.
		p.publish(p.topic("status"), "offline", true)
		p.client.Disconnect(1000)
	}
	p.log.Info("MQTT publisher stopped")
	return nil
}

// ---------------------------------------------------------------------------
// onConnect – called on every (re)connect
// ---------------------------------------------------------------------------

func (p *HAPublisher) onConnect() {
	// 1. Publish online availability (retained).
	p.publish(p.topic("status"), "online", true)

	// 2. Publish all discovery configs.
	p.publishDiscovery()

	// 3. Subscribe to command topics.
	p.subscribeCommands()

	// 4. Subscribe to HA birth topic for re-discovery.
	p.client.Subscribe("homeassistant/status", 1, func(_ pahomqtt.Client, msg pahomqtt.Message) {
		if string(msg.Payload()) == "online" {
			p.log.Info("Home Assistant came online, re-publishing discovery")
			p.publishDiscovery()
			p.publishFullState()
		}
	})

	// 5. Publish initial state snapshot.
	p.publishFullState()
}

// ---------------------------------------------------------------------------
// Discovery configs
// ---------------------------------------------------------------------------

// deviceInfo returns the shared HA device block.
func (p *HAPublisher) deviceInfo() map[string]interface{} {
	return map[string]interface{}{
		"identifiers":  []string{p.cfg.DeviceID},
		"name":         fmt.Sprintf("Nanit %s", p.cfg.BabyName),
		"manufacturer": "Nanit",
		"model":        p.cfg.CameraModel,
	}
}

// discoveryTopic builds the HA auto-discovery topic.
func discoveryTopic(component, deviceID, objectID string) string {
	return fmt.Sprintf("homeassistant/%s/%s_%s/config", component, deviceID, objectID)
}

func (p *HAPublisher) publishDiscovery() {
	dev := p.deviceInfo()
	avail := map[string]interface{}{
		"topic": p.topic("status"),
	}
	id := p.cfg.DeviceID

	// --- Sensors ---
	p.publishDiscoveryConfig("sensor", "temperature", map[string]interface{}{
		"name":                fmt.Sprintf("Nanit %s Temperature", p.cfg.BabyName),
		"unique_id":           fmt.Sprintf("%s_temperature", id),
		"state_topic":         p.topic("sensors/state"),
		"value_template":      "{{ value_json.temperature_c }}",
		"unit_of_measurement": "\u00b0C",
		"device_class":        "temperature",
		"state_class":         "measurement",
		"device":              dev,
		"availability":        avail,
	})

	p.publishDiscoveryConfig("sensor", "humidity", map[string]interface{}{
		"name":                fmt.Sprintf("Nanit %s Humidity", p.cfg.BabyName),
		"unique_id":           fmt.Sprintf("%s_humidity", id),
		"state_topic":         p.topic("sensors/state"),
		"value_template":      "{{ value_json.humidity }}",
		"unit_of_measurement": "%",
		"device_class":        "humidity",
		"state_class":         "measurement",
		"device":              dev,
		"availability":        avail,
	})

	p.publishDiscoveryConfig("sensor", "light", map[string]interface{}{
		"name":                fmt.Sprintf("Nanit %s Light", p.cfg.BabyName),
		"unique_id":           fmt.Sprintf("%s_light", id),
		"state_topic":         p.topic("sensors/state"),
		"value_template":      "{{ value_json.light }}",
		"unit_of_measurement": "lx",
		"device_class":        "illuminance",
		"state_class":         "measurement",
		"device":              dev,
		"availability":        avail,
	})

	// --- Binary Sensors ---
	p.publishDiscoveryConfig("binary_sensor", "motion", map[string]interface{}{
		"name":         fmt.Sprintf("Nanit %s Motion", p.cfg.BabyName),
		"unique_id":    fmt.Sprintf("%s_motion", id),
		"state_topic":  p.topic("motion/state"),
		"device_class": "motion",
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"device":       dev,
		"availability": avail,
	})

	p.publishDiscoveryConfig("binary_sensor", "sound", map[string]interface{}{
		"name":         fmt.Sprintf("Nanit %s Sound", p.cfg.BabyName),
		"unique_id":    fmt.Sprintf("%s_sound", id),
		"state_topic":  p.topic("sound/state"),
		"device_class": "sound",
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"device":       dev,
		"availability": avail,
	})

	p.publishDiscoveryConfig("binary_sensor", "night_mode", map[string]interface{}{
		"name":         fmt.Sprintf("Nanit %s Night Mode", p.cfg.BabyName),
		"unique_id":    fmt.Sprintf("%s_night_mode", id),
		"state_topic":  p.topic("night/state"),
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"device":       dev,
		"availability": avail,
	})

	p.publishDiscoveryConfig("binary_sensor", "connection", map[string]interface{}{
		"name":         fmt.Sprintf("Nanit %s Connection", p.cfg.BabyName),
		"unique_id":    fmt.Sprintf("%s_connection", id),
		"state_topic":  p.topic("connection/state"),
		"device_class": "connectivity",
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"device":       dev,
		"availability": avail,
	})

	// --- Switches ---
	for _, sw := range []struct {
		objectID string
		name     string
		statePfx string
	}{
		{"night_light", "Night Light", "nightlight"},
		{"sleep_mode", "Sleep Mode", "sleep"},
		{"status_led", "Status LED", "statusled"},
		{"mic_mute", "Mic Mute", "mic"},
	} {
		p.publishDiscoveryConfig("switch", sw.objectID, map[string]interface{}{
			"name":          fmt.Sprintf("Nanit %s %s", p.cfg.BabyName, sw.name),
			"unique_id":     fmt.Sprintf("%s_%s", id, sw.objectID),
			"state_topic":   p.topic(fmt.Sprintf("%s/state", sw.statePfx)),
			"command_topic": p.topic(fmt.Sprintf("%s/set", sw.statePfx)),
			"payload_on":    "ON",
			"payload_off":   "OFF",
			"device":        dev,
			"availability":  avail,
		})
	}

	// --- Number (volume) ---
	p.publishDiscoveryConfig("number", "volume", map[string]interface{}{
		"name":                fmt.Sprintf("Nanit %s Volume", p.cfg.BabyName),
		"unique_id":           fmt.Sprintf("%s_volume", id),
		"state_topic":         p.topic("volume/state"),
		"command_topic":       p.topic("volume/set"),
		"min":                 0,
		"max":                 100,
		"step":                1,
		"mode":                "slider",
		"unit_of_measurement": "%",
		"device":              dev,
		"availability":        avail,
	})
}

func (p *HAPublisher) publishDiscoveryConfig(component, objectID string, payload map[string]interface{}) {
	topic := discoveryTopic(component, p.cfg.DeviceID, objectID)
	data, err := json.Marshal(payload)
	if err != nil {
		p.log.Error("failed to marshal discovery config", "component", component, "object_id", objectID, "error", err)
		return
	}
	p.publish(topic, string(data), true)
}

// ---------------------------------------------------------------------------
// Command subscriptions
// ---------------------------------------------------------------------------

func (p *HAPublisher) subscribeCommands() {
	cmds := map[string]pahomqtt.MessageHandler{
		p.topic("nightlight/set"): p.handleNightLightCmd,
		p.topic("sleep/set"):      p.handleSleepCmd,
		p.topic("statusled/set"):  p.handleStatusLEDCmd,
		p.topic("mic/set"):        p.handleMicMuteCmd,
		p.topic("volume/set"):     p.handleVolumeCmd,
	}

	for t, h := range cmds {
		token := p.client.Subscribe(t, 1, h)
		token.Wait()
		if err := token.Error(); err != nil {
			p.log.Error("failed to subscribe to command topic", "topic", t, "error", err)
		}
	}
}

func (p *HAPublisher) handleNightLightCmd(_ pahomqtt.Client, msg pahomqtt.Message) {
	on := strings.EqualFold(strings.TrimSpace(string(msg.Payload())), "ON")
	p.log.Info("MQTT command: night_light", "on", on)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.cam.SetNightLight(ctx, on); err != nil {
		p.log.Error("failed to set night light", "error", err)
	}
}

func (p *HAPublisher) handleSleepCmd(_ pahomqtt.Client, msg pahomqtt.Message) {
	on := strings.EqualFold(strings.TrimSpace(string(msg.Payload())), "ON")
	p.log.Info("MQTT command: sleep_mode", "enabled", on)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.cam.SetSleepMode(ctx, on); err != nil {
		p.log.Error("failed to set sleep mode", "error", err)
	}
}

func (p *HAPublisher) handleStatusLEDCmd(_ pahomqtt.Client, msg pahomqtt.Message) {
	on := strings.EqualFold(strings.TrimSpace(string(msg.Payload())), "ON")
	p.log.Info("MQTT command: status_led", "enabled", on)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.cam.SetStatusLED(ctx, on); err != nil {
		p.log.Error("failed to set status LED", "error", err)
	}
}

func (p *HAPublisher) handleMicMuteCmd(_ pahomqtt.Client, msg pahomqtt.Message) {
	on := strings.EqualFold(strings.TrimSpace(string(msg.Payload())), "ON")
	p.log.Info("MQTT command: mic_mute", "muted", on)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.cam.SetMicMute(ctx, on); err != nil {
		p.log.Error("failed to set mic mute", "error", err)
	}
}

func (p *HAPublisher) handleVolumeCmd(_ pahomqtt.Client, msg pahomqtt.Message) {
	raw := strings.TrimSpace(string(msg.Payload()))
	level, err := strconv.Atoi(raw)
	if err != nil {
		p.log.Error("invalid volume value", "payload", raw, "error", err)
		return
	}
	p.log.Info("MQTT command: volume", "level", level)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.cam.SetVolume(ctx, int32(level)); err != nil {
		p.log.Error("failed to set volume", "error", err)
	}
}

// ---------------------------------------------------------------------------
// State publishing
// ---------------------------------------------------------------------------

// publishFullState publishes the complete state snapshot.
func (p *HAPublisher) publishFullState() {
	snap := p.store.Snapshot()

	// Sensors JSON
	p.publishSensorsState(snap.Sensors)

	// Binary sensors from sensor values
	if sv, ok := snap.Sensors[state.SensorMotion]; ok {
		p.publish(p.topic("motion/state"), boolToOnOff(sv.IsAlert), true)
	}
	if sv, ok := snap.Sensors[state.SensorSound]; ok {
		p.publish(p.topic("sound/state"), boolToOnOff(sv.IsAlert), true)
	}
	if sv, ok := snap.Sensors[state.SensorNight]; ok {
		p.publish(p.topic("night/state"), boolToOnOff(sv.Value > 0), true)
	}

	// Controls
	if snap.Control.NightLight != nil {
		p.publish(p.topic("nightlight/state"), boolToOnOff(*snap.Control.NightLight == "on"), true)
	}

	// Settings
	p.publishSettingsState(snap.Settings)

	// Connection
	p.publish(p.topic("connection/state"), boolToOnOff(snap.Status.Connected), true)
}

func (p *HAPublisher) publishSensorsState(sensors map[state.SensorType]state.SensorValue) {
	payload := map[string]interface{}{}

	if sv, ok := sensors[state.SensorTemperature]; ok {
		payload["temperature_c"] = roundTo2(sv.Value)
	}
	if sv, ok := sensors[state.SensorHumidity]; ok {
		payload["humidity"] = roundTo2(sv.Value)
	}
	if sv, ok := sensors[state.SensorLight]; ok {
		payload["light"] = roundTo2(sv.Value)
	}

	if len(payload) == 0 {
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		p.log.Error("failed to marshal sensor state", "error", err)
		return
	}
	p.publish(p.topic("sensors/state"), string(data), true)
}

func (p *HAPublisher) publishSettingsState(settings state.CameraSettings) {
	if settings.SleepMode != nil {
		p.publish(p.topic("sleep/state"), boolToOnOff(*settings.SleepMode), true)
	}
	if settings.StatusLightOn != nil {
		p.publish(p.topic("statusled/state"), boolToOnOff(*settings.StatusLightOn), true)
	}
	if settings.MicMuteOn != nil {
		p.publish(p.topic("mic/state"), boolToOnOff(*settings.MicMuteOn), true)
	}
	if settings.Volume != nil {
		p.publish(p.topic("volume/state"), strconv.Itoa(int(*settings.Volume)), true)
	}
}

// ---------------------------------------------------------------------------
// EventBus loop
// ---------------------------------------------------------------------------

func (p *HAPublisher) eventLoop(ch <-chan state.Event) {
	defer p.wg.Done()

	for {
		select {
		case <-p.stopC:
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			p.handleEvent(evt)
		}
	}
}

func (p *HAPublisher) handleEvent(evt state.Event) {
	switch evt.Type {
	case state.EventSensorUpdate:
		sensors, ok := evt.Data.(map[state.SensorType]state.SensorValue)
		if !ok {
			p.log.Warn("unexpected data type for sensor_update")
			return
		}
		p.publishSensorsState(sensors)

		// Also update binary sensors derived from sensor data.
		if sv, ok := sensors[state.SensorMotion]; ok {
			p.publish(p.topic("motion/state"), boolToOnOff(sv.IsAlert), true)
		}
		if sv, ok := sensors[state.SensorSound]; ok {
			p.publish(p.topic("sound/state"), boolToOnOff(sv.IsAlert), true)
		}
		if sv, ok := sensors[state.SensorNight]; ok {
			p.publish(p.topic("night/state"), boolToOnOff(sv.Value > 0), true)
		}

	case state.EventSettingsUpdate:
		settings, ok := evt.Data.(state.CameraSettings)
		if !ok {
			p.log.Warn("unexpected data type for settings_update")
			return
		}
		p.publishSettingsState(settings)

	case state.EventControlUpdate:
		ctrl, ok := evt.Data.(state.CameraControl)
		if !ok {
			p.log.Warn("unexpected data type for control_update")
			return
		}
		if ctrl.NightLight != nil {
			p.publish(p.topic("nightlight/state"), boolToOnOff(*ctrl.NightLight == "on"), true)
		}

	case state.EventConnected:
		p.publish(p.topic("connection/state"), "ON", true)

	case state.EventDisconnected:
		p.publish(p.topic("connection/state"), "OFF", true)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// topic builds a full topic path: {prefix}/{device_id}/{suffix}.
func (p *HAPublisher) topic(suffix string) string {
	return fmt.Sprintf("%s/%s/%s", p.cfg.TopicPrefix, p.cfg.DeviceID, suffix)
}

// publish is a convenience wrapper that publishes a message and logs errors.
func (p *HAPublisher) publish(topic, payload string, retained bool) {
	if p.client == nil || !p.client.IsConnected() {
		return
	}
	token := p.client.Publish(topic, 1, retained, payload)
	token.Wait()
	if err := token.Error(); err != nil {
		p.log.Error("mqtt publish failed", "topic", topic, "error", err)
	}
}

func boolToOnOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func roundTo2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
