package state

import (
	"log/slog"
	"sync"
	"time"

	"github.com/trymwestin/nanit/internal/core/transport/pb"
)

// SensorType identifies a sensor kind.
type SensorType string

const (
	SensorSound       SensorType = "sound"
	SensorMotion      SensorType = "motion"
	SensorTemperature SensorType = "temperature"
	SensorHumidity    SensorType = "humidity"
	SensorLight       SensorType = "light"
	SensorNight       SensorType = "night"
)

// SensorValue holds a sensor reading.
type SensorValue struct {
	Type      SensorType `json:"type"`
	Value     float64    `json:"value"`
	RawValue  int32      `json:"raw_value"`
	IsAlert   bool       `json:"is_alert"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// CameraSettings holds the latest known camera settings.
type CameraSettings struct {
	NightVision   *bool  `json:"night_vision,omitempty"`
	SleepMode     *bool  `json:"sleep_mode,omitempty"`
	StatusLightOn *bool  `json:"status_light_on,omitempty"`
	Volume        *int32 `json:"volume,omitempty"`
	MicMuteOn     *bool  `json:"mic_mute_on,omitempty"`
}

// CameraControl holds the latest known camera control state.
type CameraControl struct {
	NightLight *string `json:"night_light,omitempty"` // "on" or "off"
}

// CameraStatus holds the latest camera status info.
type CameraStatus struct {
	Connected       bool   `json:"connected"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	HardwareVersion string `json:"hardware_version,omitempty"`
}

// StreamInfo holds the active stream info.
type StreamInfo struct {
	Active  bool   `json:"active"`
	RTMPUrl string `json:"rtmp_url,omitempty"`
}

// State is a snapshot of all camera state.
type State struct {
	Sensors  map[SensorType]SensorValue `json:"sensors"`
	Settings CameraSettings             `json:"settings"`
	Control  CameraControl              `json:"control"`
	Status   CameraStatus               `json:"status"`
	Stream   StreamInfo                 `json:"stream"`
}

// EventType identifies event categories.
type EventType string

const (
	EventSensorUpdate   EventType = "sensor_update"
	EventSettingsUpdate EventType = "settings_update"
	EventControlUpdate  EventType = "control_update"
	EventStatusUpdate   EventType = "status_update"
	EventStreamUpdate   EventType = "stream_update"
	EventConnected      EventType = "connected"
	EventDisconnected   EventType = "disconnected"
)

// Event represents a state change.
type Event struct {
	Type      EventType   `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

// EventHandler is a callback for events.
type EventHandler func(Event)

// StateReader provides read-only access to state.
type StateReader interface {
	Snapshot() State
	GetSensor(sensor SensorType) (SensorValue, bool)
}

// --- EventBus ---

// EventBus is a simple publish/subscribe event bus.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]chan Event
	nextID      int
	log         *slog.Logger
}

// NewEventBus creates a new event bus.
func NewEventBus(log *slog.Logger) *EventBus {
	return &EventBus{
		subscribers: make(map[int]chan Event),
		log:         log,
	}
}

// Publish sends an event to all subscribers.
func (b *EventBus) Publish(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for id, ch := range b.subscribers {
		select {
		case ch <- evt:
		default:
			b.log.Warn("event bus: subscriber buffer full, dropping event", "subscriber_id", id, "event_type", evt.Type)
		}
	}
}

// Subscribe returns a channel of events and an unsubscribe function.
func (b *EventBus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}

	ch := make(chan Event, buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = ch
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
		// drain channel
		for range ch {
		}
	}
	return ch, unsub
}

// --- StateStore ---

// StateStore holds the current camera state with thread-safe access.
type StateStore struct {
	mu       sync.RWMutex
	sensors  map[SensorType]SensorValue
	settings CameraSettings
	control  CameraControl
	status   CameraStatus
	stream   StreamInfo
	bus      *EventBus
	log      *slog.Logger
}

// NewStateStore creates a new store wired to the event bus.
func NewStateStore(bus *EventBus, log *slog.Logger) *StateStore {
	return &StateStore{
		sensors: make(map[SensorType]SensorValue),
		bus:     bus,
		log:     log,
	}
}

// Snapshot returns a copy of all state.
func (s *StateStore) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sensors := make(map[SensorType]SensorValue, len(s.sensors))
	for k, v := range s.sensors {
		sensors[k] = v
	}
	return State{
		Sensors:  sensors,
		Settings: s.settings,
		Control:  s.control,
		Status:   s.status,
		Stream:   s.stream,
	}
}

// GetSensor returns a specific sensor value.
func (s *StateStore) GetSensor(sensor SensorType) (SensorValue, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.sensors[sensor]
	return v, ok
}

// UpdateSensors updates sensor state from protobuf sensor data.
func (s *StateStore) UpdateSensors(data []*pb.SensorData) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sd := range data {
		if sd.SensorType == nil {
			continue
		}
		st := pbSensorTypeToState(sd.GetSensorType())
		val := SensorValue{
			Type:      st,
			RawValue:  sd.GetValue(),
			IsAlert:   sd.GetIsAlert(),
			UpdatedAt: time.Now(),
		}

		// Convert value based on sensor type
		switch sd.GetSensorType() {
		case pb.SensorType_TEMPERATURE:
			if sd.ValueMilli != nil {
				val.Value = float64(sd.GetValueMilli()) / 1000.0
			} else {
				val.Value = float64(sd.GetValue())
			}
		case pb.SensorType_HUMIDITY:
			if sd.ValueMilli != nil {
				val.Value = float64(sd.GetValueMilli()) / 1000.0
			} else {
				val.Value = float64(sd.GetValue())
			}
		default:
			val.Value = float64(sd.GetValue())
		}

		s.sensors[st] = val
	}

	s.bus.Publish(Event{Type: EventSensorUpdate, Data: s.sensorsSnapshot()})
}

func (s *StateStore) sensorsSnapshot() map[SensorType]SensorValue {
	cp := make(map[SensorType]SensorValue, len(s.sensors))
	for k, v := range s.sensors {
		cp[k] = v
	}
	return cp
}

// UpdateSettings updates the camera settings from protobuf.
func (s *StateStore) UpdateSettings(settings *pb.Settings) {
	if settings == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if settings.NightVision != nil {
		v := settings.GetNightVision()
		s.settings.NightVision = &v
	}
	if settings.SleepMode != nil {
		v := settings.GetSleepMode()
		s.settings.SleepMode = &v
	}
	if settings.StatusLightOn != nil {
		v := settings.GetStatusLightOn()
		s.settings.StatusLightOn = &v
	}
	if settings.Volume != nil {
		v := settings.GetVolume()
		s.settings.Volume = &v
	}
	if settings.MicMuteOn != nil {
		v := settings.GetMicMuteOn()
		s.settings.MicMuteOn = &v
	}

	s.bus.Publish(Event{Type: EventSettingsUpdate, Data: s.settings})
}

// UpdateControl updates the camera control state from protobuf.
func (s *StateStore) UpdateControl(ctrl *pb.Control) {
	if ctrl == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl.NightLight != nil {
		var v string
		if ctrl.GetNightLight() == pb.Control_LIGHT_ON {
			v = "on"
		} else {
			v = "off"
		}
		s.control.NightLight = &v
	}

	s.bus.Publish(Event{Type: EventControlUpdate, Data: s.control})
}

// UpdateStatus updates the camera status from protobuf.
func (s *StateStore) UpdateStatus(status *pb.Status) {
	if status == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if status.ConnectionToServer != nil {
		s.status.Connected = status.GetConnectionToServer() == pb.Status_CONNECTED
	}
	if status.CurrentVersion != nil {
		s.status.FirmwareVersion = status.GetCurrentVersion()
	}
	if status.HardwareVersion != nil {
		s.status.HardwareVersion = status.GetHardwareVersion()
	}

	s.bus.Publish(Event{Type: EventStatusUpdate, Data: s.status})
}

// SetConnected updates the WebSocket connection status.
func (s *StateStore) SetConnected(connected bool) {
	s.mu.Lock()
	s.status.Connected = connected
	s.mu.Unlock()

	if connected {
		s.bus.Publish(Event{Type: EventConnected})
	} else {
		s.bus.Publish(Event{Type: EventDisconnected})
	}
}

// SetStream updates the active stream info.
func (s *StateStore) SetStream(active bool, rtmpURL string) {
	s.mu.Lock()
	s.stream = StreamInfo{Active: active, RTMPUrl: rtmpURL}
	s.mu.Unlock()

	s.bus.Publish(Event{Type: EventStreamUpdate, Data: s.stream})
}

func pbSensorTypeToState(st pb.SensorType) SensorType {
	switch st {
	case pb.SensorType_SOUND:
		return SensorSound
	case pb.SensorType_MOTION:
		return SensorMotion
	case pb.SensorType_TEMPERATURE:
		return SensorTemperature
	case pb.SensorType_HUMIDITY:
		return SensorHumidity
	case pb.SensorType_LIGHT:
		return SensorLight
	case pb.SensorType_NIGHT:
		return SensorNight
	default:
		return SensorType(st.String())
	}
}
