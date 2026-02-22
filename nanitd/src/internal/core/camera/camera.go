package camera

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trymwestin/nanit/internal/core/auth"
	"github.com/trymwestin/nanit/internal/core/state"
	"github.com/trymwestin/nanit/internal/core/transport"
	"github.com/trymwestin/nanit/internal/core/transport/pb"
)

// Client manages the WebSocket connection to a Nanit camera.
type Client struct {
	cameraUID string
	dialer    transport.Dialer
	tokenMgr  *auth.TokenManager
	store     *state.StateStore
	bus       *state.EventBus
	log       *slog.Logger

	conn    transport.Conn
	connMu  sync.Mutex
	reqID   atomic.Int32
	cancel  context.CancelFunc
	stopped chan struct{}
	running atomic.Bool

	// pendingResponses tracks request IDs waiting for responses
	pending   map[int32]chan *pb.Response
	pendingMu sync.Mutex
	wakeCh    chan struct{}
}

// NewClient creates a new camera client.
func NewClient(
	cameraUID string,
	dialer transport.Dialer,
	tokenMgr *auth.TokenManager,
	store *state.StateStore,
	bus *state.EventBus,
	log *slog.Logger,
) *Client {
	return &Client{
		cameraUID: cameraUID,
		dialer:    dialer,
		tokenMgr:  tokenMgr,
		store:     store,
		bus:       bus,
		log:       log,
		pending:   make(map[int32]chan *pb.Response),
		wakeCh:    make(chan struct{}, 1),
	}
}

// Start connects to the camera and begins the read/keepalive loops.
// It will reconnect with exponential backoff on failures.
func (c *Client) Start(ctx context.Context) error {
	if c.running.Load() {
		return fmt.Errorf("camera: already running")
	}

	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.stopped = make(chan struct{})
	c.running.Store(true)

	go c.runLoop(ctx)
	return nil
}

// Stop disconnects and stops all goroutines.
func (c *Client) Stop(_ context.Context) error {
	if !c.running.Load() {
		return nil
	}
	c.cancel()
	<-c.stopped
	c.running.Store(false)
	return nil
}

// SendCommand sends a protobuf request to the camera and returns the response.
func (c *Client) SendCommand(ctx context.Context, req *pb.Request) (*pb.Response, error) {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("camera: not connected")
	}

	id := c.reqID.Add(1)
	req.Id = &id

	// Create response channel
	respCh := make(chan *pb.Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	msg := &pb.Message{
		Type:    pb.Message_REQUEST.Enum(),
		Request: req,
	}

	c.log.Debug("sending command", "type", req.GetType().String(), "id", id)
	if err := conn.Send(ctx, msg); err != nil {
		c.log.Error("failed to send command", "type", req.GetType().String(), "id", id, "error", err)
		return nil, fmt.Errorf("camera: send: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("camera: response timeout for request %d", id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendCommandNoWait sends a request without waiting for a response.
func (c *Client) SendCommandNoWait(ctx context.Context, req *pb.Request) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("camera: not connected")
	}

	id := c.reqID.Add(1)
	req.Id = &id

	msg := &pb.Message{
		Type:    pb.Message_REQUEST.Enum(),
		Request: req,
	}

	c.log.Debug("sending command (no wait)", "type", req.GetType().String(), "id", id)
	return conn.Send(ctx, msg)
}

func (c *Client) sendCommandAndWait(
	ctx context.Context,
	req *pb.Request,
	op string,
) (*pb.Response, error) {
	if err := c.ensureConnected(ctx, 20*time.Second); err != nil {
		return nil, fmt.Errorf("camera: %s: %w", op, err)
	}

	resp, err := c.SendCommand(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		c.disconnect()
		c.signalWake()
		if waitErr := c.ensureConnected(ctx, 15*time.Second); waitErr != nil {
			return nil, fmt.Errorf("camera: %s: reconnect failed: %w", op, err)
		}
		resp, err = c.SendCommand(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("camera: %s: %w", op, err)
		}
	}

	if resp == nil {
		return nil, fmt.Errorf("camera: %s: empty response", op)
	}
	status := resp.GetStatusCode()
	if status != 0 && (status < 200 || status >= 300) {
		msg := resp.GetStatusMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("camera: %s failed (status %d): %s", op, status, msg)
	}
	return resp, nil
}

func (c *Client) signalWake() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Client) ensureConnected(ctx context.Context, timeout time.Duration) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn != nil {
		return nil
	}

	c.signalWake()

	deadline := time.After(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("camera: connection timeout after %s", timeout)
		case <-ticker.C:
			c.connMu.Lock()
			conn = c.conn
			c.connMu.Unlock()
			if conn != nil {
				return nil
			}
		}
	}
}

// State returns the state store for reading current state.
func (c *Client) State() *state.StateStore {
	return c.store
}

// Bus returns the event bus for subscribing to events.
func (c *Client) Bus() *state.EventBus {
	return c.bus
}

// Connected returns whether the camera is currently connected.
func (c *Client) Connected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn != nil
}

func (c *Client) runLoop(ctx context.Context) {
	defer close(c.stopped)

	backoff := time.Second
	maxBackoff := 2 * time.Minute

	for {
		select {
		case <-ctx.Done():
			c.disconnect()
			return
		default:
		}

		connected, err := c.connectAndRun(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.log.Info("camera: shutting down")
				return
			}
			c.log.Error("camera: connection error", "error", err, "retry_in", backoff, "camera_uid", c.cameraUID)
		}

		c.disconnect()

		if connected {
			backoff = time.Second
		}

		// Interruptible backoff — wake signal skips the wait
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.wakeCh:
			timer.Stop()
			select {
			case <-timer.C:
			default:
			}
			backoff = time.Second
			c.log.Info("wake signal received, reconnecting immediately")
		case <-timer.C:
		}

		backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
	}
}

func (c *Client) connectAndRun(ctx context.Context) (connected bool, err error) {
	c.log.Info("attempting camera connection", "camera_uid", c.cameraUID)
	tok, err := c.tokenMgr.Token(ctx)
	if err != nil {
		return false, fmt.Errorf("get token: %w", err)
	}

	conn, err := c.dialer.Dial(ctx, c.cameraUID, tok.AuthToken)
	if err != nil {
		c.log.Warn("initial dial failed, attempting token refresh", "error", err)
		newTok, refreshErr := c.tokenMgr.ForceRefresh(ctx)
		if refreshErr != nil {
			return false, fmt.Errorf("dial failed (%w) and refresh failed: %v", err, refreshErr)
		}
		c.log.Info("token refreshed after dial failure, retrying connection")
		conn, err = c.dialer.Dial(ctx, c.cameraUID, newTok.AuthToken)
		if err != nil {
			return false, fmt.Errorf("dial after refresh: %w", err)
		}
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	c.store.SetConnected(true)
	connected = true

	// Set initial read deadline — extended by pong handler and incoming messages
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	c.log.Info("camera connected, initializing", "camera_uid", c.cameraUID)

	if err := c.requestInitialState(ctx); err != nil {
		c.log.Error("failed to request initial state", "error", err)
	}

	keepaliveCtx, keepaliveCancel := context.WithCancel(ctx)
	defer keepaliveCancel()
	go c.keepaliveLoop(keepaliveCtx, conn)

	return connected, c.readLoop(ctx, conn)
}

func (c *Client) disconnect() {
	c.connMu.Lock()
	if c.conn != nil {
		c.log.Info("disconnecting camera", "camera_uid", c.cameraUID)
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.store.SetConnected(false)
}

func (c *Client) keepaliveLoop(ctx context.Context, conn transport.Conn) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.Ping(); err != nil {
				c.log.Warn("keepalive ping failed, triggering reconnect", "error", err)
				c.disconnect()
				return
			}
			c.log.Debug("keepalive ping sent")
		}
	}
}

func (c *Client) readLoop(ctx context.Context, conn transport.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := conn.Recv(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg *pb.Message) {
	switch msg.GetType() {
	case pb.Message_KEEPALIVE:
		c.log.Debug("received keepalive")

	case pb.Message_REQUEST:
		// Camera pushes data to us (e.g., PUT_SENSOR_DATA)
		c.handleCameraRequest(msg.GetRequest())

	case pb.Message_RESPONSE:
		c.handleResponse(msg.GetResponse())
	}
}

func (c *Client) handleCameraRequest(req *pb.Request) {
	if req == nil {
		return
	}

	c.log.Debug("received camera request", "type", req.GetType().String())

	switch req.GetType() {
	case pb.RequestType_PUT_SENSOR_DATA:
		if data := req.GetSensorData_(); len(data) > 0 {
			c.store.UpdateSensors(data)
			c.log.Info("sensor data received from camera", "count", len(data))
		}

	case pb.RequestType_PUT_STATUS:
		if status := req.GetStatus(); status != nil {
			c.store.UpdateStatus(status)
			c.log.Info("camera status update received")
		}

	case pb.RequestType_PUT_SETTINGS:
		if settings := req.GetSettings(); settings != nil {
			c.store.UpdateSettings(settings)
			c.log.Info("camera settings update received")
		}

	case pb.RequestType_PUT_CONTROL:
		if ctrl := req.GetControl(); ctrl != nil {
			c.store.UpdateControl(ctrl)
			c.log.Info("camera control update received")
		}

	default:
		c.log.Debug("unhandled camera request", "type", req.GetType().String())
	}
}

func (c *Client) handleResponse(resp *pb.Response) {
	if resp == nil {
		return
	}

	c.log.Debug("received response",
		"request_id", resp.GetRequestId(),
		"type", resp.GetRequestType().String(),
		"status", resp.GetStatusCode(),
	)

	// Update state from response data
	switch resp.GetRequestType() {
	case pb.RequestType_GET_SENSOR_DATA:
		if data := resp.GetSensorData(); len(data) > 0 {
			c.store.UpdateSensors(data)
		}

	case pb.RequestType_GET_SETTINGS:
		if settings := resp.GetSettings(); settings != nil {
			c.store.UpdateSettings(settings)
		}

	case pb.RequestType_GET_STATUS:
		if status := resp.GetStatus(); status != nil {
			c.store.UpdateStatus(status)
		}

	case pb.RequestType_GET_CONTROL:
		if ctrl := resp.GetControl(); ctrl != nil {
			c.store.UpdateControl(ctrl)
		}
	}

	// Deliver to waiting SendCommand callers
	c.pendingMu.Lock()
	ch, ok := c.pending[resp.GetRequestId()]
	c.pendingMu.Unlock()

	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

func (c *Client) requestInitialState(ctx context.Context) error {
	// Request all sensor data
	allTrue := true
	sensorReq := &pb.Request{
		Type:          pb.RequestType_GET_SENSOR_DATA.Enum(),
		GetSensorData: &pb.GetSensorData{All: &allTrue},
	}
	if err := c.SendCommandNoWait(ctx, sensorReq); err != nil {
		return fmt.Errorf("get sensor data: %w", err)
	}

	if err := c.requestSettingsState(ctx); err != nil {
		return fmt.Errorf("get settings: %w", err)
	}

	// Request status
	statusReq := &pb.Request{
		Type:       pb.RequestType_GET_STATUS.Enum(),
		GetStatus_: &pb.GetStatus{All: &allTrue},
	}
	if err := c.SendCommandNoWait(ctx, statusReq); err != nil {
		return fmt.Errorf("get status: %w", err)
	}

	if err := c.requestControlState(ctx); err != nil {
		return fmt.Errorf("get control: %w", err)
	}

	// Enable sensor data push from camera
	enableAllSensors := true
	enableReq := &pb.Request{
		Type: pb.RequestType_PUT_CONTROL.Enum(),
		Control: &pb.Control{
			SensorDataTransfer: &pb.Control_SensorDataTransfer{
				Sound:       &enableAllSensors,
				Motion:      &enableAllSensors,
				Temperature: &enableAllSensors,
				Humidity:    &enableAllSensors,
				Light:       &enableAllSensors,
				Night:       &enableAllSensors,
			},
		},
	}
	if err := c.SendCommandNoWait(ctx, enableReq); err != nil {
		return fmt.Errorf("enable sensor push: %w", err)
	}

	c.log.Info("initial state requests sent")
	return nil
}

// --- Command helpers for the HTTP API ---

// SetNightLight turns the night light on or off.
func (c *Client) SetNightLight(ctx context.Context, on bool) error {
	nl := pb.Control_LIGHT_OFF
	if on {
		nl = pb.Control_LIGHT_ON
	}
	req := &pb.Request{
		Type:    pb.RequestType_PUT_CONTROL.Enum(),
		Control: &pb.Control{NightLight: nl.Enum()},
	}
	if _, err := c.sendCommandAndWait(ctx, req, "set night light"); err != nil {
		return err
	}
	return c.requestControlState(ctx)
}

// SetSleepMode toggles sleep (standby) mode.
func (c *Client) SetSleepMode(ctx context.Context, enabled bool) error {
	req := &pb.Request{
		Type:     pb.RequestType_PUT_SETTINGS.Enum(),
		Settings: &pb.Settings{SleepMode: &enabled},
	}
	if _, err := c.sendCommandAndWait(ctx, req, "set sleep mode"); err != nil {
		return err
	}
	return c.requestSettingsState(ctx)
}

// SetVolume sets the camera speaker volume (0-100).
func (c *Client) SetVolume(ctx context.Context, level int32) error {
	req := &pb.Request{
		Type:     pb.RequestType_PUT_SETTINGS.Enum(),
		Settings: &pb.Settings{Volume: &level},
	}
	return c.SendCommandNoWait(ctx, req)
}

// SetMicMute toggles mic mute.
func (c *Client) SetMicMute(ctx context.Context, muted bool) error {
	req := &pb.Request{
		Type:     pb.RequestType_PUT_SETTINGS.Enum(),
		Settings: &pb.Settings{MicMuteOn: &muted},
	}
	return c.SendCommandNoWait(ctx, req)
}

// SetStatusLED toggles the camera status LED.
func (c *Client) SetStatusLED(ctx context.Context, enabled bool) error {
	req := &pb.Request{
		Type:     pb.RequestType_PUT_SETTINGS.Enum(),
		Settings: &pb.Settings{StatusLightOn: &enabled},
	}
	return c.SendCommandNoWait(ctx, req)
}

func (c *Client) requestControlState(ctx context.Context) error {
	nlTrue := true
	controlReq := &pb.Request{
		Type:        pb.RequestType_GET_CONTROL.Enum(),
		GetControl_: &pb.GetControl{NightLight: &nlTrue},
	}
	if err := c.SendCommandNoWait(ctx, controlReq); err != nil {
		c.log.Debug("failed to refresh control state", "error", err)
		return err
	}
	return nil
}

func (c *Client) requestSettingsState(ctx context.Context) error {
	settingsReq := &pb.Request{Type: pb.RequestType_GET_SETTINGS.Enum()}
	if err := c.SendCommandNoWait(ctx, settingsReq); err != nil {
		c.log.Debug("failed to refresh settings state", "error", err)
		return err
	}
	return nil
}

// StartStreaming tells the camera to start streaming to the given RTMP URL.
func (c *Client) StartStreaming(ctx context.Context, rtmpURL string) error {
	c.log.Info("starting camera streaming")
	req := &pb.Request{
		Type: pb.RequestType_PUT_STREAMING.Enum(),
		Streaming: &pb.Streaming{
			Id:      pb.StreamIdentifier_MOBILE.Enum(),
			Status:  pb.Streaming_STARTED.Enum(),
			RtmpUrl: &rtmpURL,
		},
	}
	if err := c.SendCommandNoWait(ctx, req); err != nil {
		return err
	}
	c.store.SetStream(true, rtmpURL)
	return nil
}

// StopStreaming tells the camera to stop streaming.
func (c *Client) StopStreaming(ctx context.Context) error {
	c.log.Info("stopping camera streaming")
	empty := ""
	req := &pb.Request{
		Type: pb.RequestType_PUT_STREAMING.Enum(),
		Streaming: &pb.Streaming{
			Id:      pb.StreamIdentifier_MOBILE.Enum(),
			Status:  pb.Streaming_STOPPED.Enum(),
			RtmpUrl: &empty,
		},
	}
	if err := c.SendCommandNoWait(ctx, req); err != nil {
		return err
	}
	c.store.SetStream(false, "")
	return nil
}
