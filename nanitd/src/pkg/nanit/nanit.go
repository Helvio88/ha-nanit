// Package nanit provides a public facade re-exporting core types
// for external consumers of this module.
package nanit

import (
	"github.com/trymwestin/nanit/internal/core/auth"
	"github.com/trymwestin/nanit/internal/core/camera"
	"github.com/trymwestin/nanit/internal/core/state"
	"github.com/trymwestin/nanit/internal/core/transport"
)

// Re-export core types for external use.
type (
	// Token holds authentication credentials.
	Token = auth.Token
	// Baby represents a baby profile.
	Baby = auth.Baby
	// SensorType identifies a sensor kind.
	SensorType = state.SensorType
	// SensorValue holds a sensor reading.
	SensorValue = state.SensorValue
	// State is a snapshot of all camera state.
	State = state.State
	// Event represents a state change event.
	Event = state.Event
	// EventType identifies event categories.
	EventType = state.EventType
	// CameraClient manages the WebSocket connection to a Nanit camera.
	CameraClient = camera.Client
	// Dialer creates WebSocket connections to cameras.
	Dialer = transport.Dialer
	// Conn represents a WebSocket connection.
	Conn = transport.Conn
)

// Sensor type constants.
const (
	SensorSound       = state.SensorSound
	SensorMotion      = state.SensorMotion
	SensorTemperature = state.SensorTemperature
	SensorHumidity    = state.SensorHumidity
	SensorLight       = state.SensorLight
	SensorNight       = state.SensorNight
)

// Event type constants.
const (
	EventSensorUpdate   = state.EventSensorUpdate
	EventSettingsUpdate = state.EventSettingsUpdate
	EventControlUpdate  = state.EventControlUpdate
	EventStatusUpdate   = state.EventStatusUpdate
	EventStreamUpdate   = state.EventStreamUpdate
	EventConnected      = state.EventConnected
	EventDisconnected   = state.EventDisconnected
)
