package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/trymwestin/nanit/internal/core/transport/pb"
	"google.golang.org/protobuf/proto"
)

// Conn represents a WebSocket connection that sends/receives protobuf Messages.
type Conn interface {
	// Send sends a protobuf Message over the wire.
	Send(ctx context.Context, msg *pb.Message) error
	// Recv blocks until a Message is received or context is cancelled.
	Recv(ctx context.Context) (*pb.Message, error)
	// Close closes the underlying connection.
	Close() error
	// Ping sends a WebSocket-level ping frame.
	Ping() error
	// SetReadDeadline sets the read deadline on the underlying connection.
	SetReadDeadline(t time.Time) error
}

// Dialer creates WebSocket connections to cameras.
type Dialer interface {
	Dial(ctx context.Context, cameraUID string, authToken string) (Conn, error)
}

// --- WebSocket Conn implementation ---

type wsConn struct {
	ws  *websocket.Conn
	mu  sync.Mutex // protects writes
	log *slog.Logger
}

func newWSConn(ws *websocket.Conn, log *slog.Logger) *wsConn {
	c := &wsConn{ws: ws, log: log}
	ws.SetPongHandler(func(appData string) error {
		return ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	return c
}

func (c *wsConn) Send(_ context.Context, msg *pb.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: marshal: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ws.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return fmt.Errorf("transport: write: %w", err)
	}
	return nil
}

func (c *wsConn) Recv(_ context.Context) (*pb.Message, error) {
	msgType, data, err := c.ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("transport: read: %w", err)
	}

	if msgType != websocket.BinaryMessage {
		return nil, fmt.Errorf("transport: unexpected message type %d", msgType)
	}

	var msg pb.Message
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("transport: unmarshal: %w", err)
	}
	return &msg, nil
}

func (c *wsConn) Close() error {
	return c.ws.Close()
}

func (c *wsConn) Ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(5*time.Second))
}

func (c *wsConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

// --- Cloud Dialer ---

// CloudDialer connects to cameras via the Nanit cloud relay.
type CloudDialer struct {
	apiBase string
	log     *slog.Logger
}

// NewCloudDialer creates a cloud relay dialer.
func NewCloudDialer(apiBase string, log *slog.Logger) *CloudDialer {
	return &CloudDialer{apiBase: apiBase, log: log}
}

// Dial connects to the camera via the Nanit cloud WebSocket relay.
func (d *CloudDialer) Dial(ctx context.Context, cameraUID string, authToken string) (Conn, error) {
	// Convert https:// to wss://
	wsBase := d.apiBase
	if len(wsBase) > 8 && wsBase[:8] == "https://" {
		wsBase = "wss://" + wsBase[8:]
	} else if len(wsBase) > 7 && wsBase[:7] == "http://" {
		wsBase = "ws://" + wsBase[7:]
	}

	url := fmt.Sprintf("%s/focus/cameras/%s/user_connect", wsBase, cameraUID)

	header := http.Header{}
	header.Set("Authorization", "Bearer "+authToken)

	d.log.Info("dialing camera", "url", url, "camera_uid", cameraUID)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	ws, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("transport: dial %s: HTTP %d: %w", url, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("transport: dial %s: %w", url, err)
	}

	d.log.Info("connected to camera", "camera_uid", cameraUID)
	return newWSConn(ws, d.log), nil
}

// --- Local Dialer ---

// LocalDialer connects directly to the camera on the LAN via port 442.
type LocalDialer struct {
	cameraIP string
	log      *slog.Logger
}

// NewLocalDialer creates a local LAN dialer for the given camera IP.
func NewLocalDialer(cameraIP string, log *slog.Logger) *LocalDialer {
	return &LocalDialer{cameraIP: cameraIP, log: log}
}

// Dial connects to the camera directly over the local network.
func (d *LocalDialer) Dial(ctx context.Context, cameraUID string, authToken string) (Conn, error) {
	url := fmt.Sprintf("wss://%s:442", d.cameraIP)

	header := http.Header{}
	header.Set("Authorization", "token "+authToken)

	d.log.Info("dialing camera locally", "url", url, "camera_uid", cameraUID, "camera_ip", d.cameraIP)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // camera uses self-signed cert
	}

	ws, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("transport: local dial %s: HTTP %d: %w", url, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("transport: local dial %s: %w", url, err)
	}

	d.log.Info("connected to camera locally", "camera_uid", cameraUID, "camera_ip", d.cameraIP)
	return newWSConn(ws, d.log), nil
}

// --- Fallback Dialer ---

// FallbackDialer tries a local connection first, falling back to cloud on failure.
type FallbackDialer struct {
	local *LocalDialer
	cloud *CloudDialer
	log   *slog.Logger
}

// NewFallbackDialer creates a dialer that tries local first, then cloud.
func NewFallbackDialer(local *LocalDialer, cloud *CloudDialer, log *slog.Logger) *FallbackDialer {
	return &FallbackDialer{local: local, cloud: cloud, log: log}
}

// Dial attempts a local connection first; if it fails, falls back to the cloud relay.
func (d *FallbackDialer) Dial(ctx context.Context, cameraUID string, authToken string) (Conn, error) {
	conn, err := d.local.Dial(ctx, cameraUID, authToken)
	if err == nil {
		return conn, nil
	}

	d.log.Warn("local dial failed, falling back to cloud", "camera_uid", cameraUID, "error", err)

	return d.cloud.Dial(ctx, cameraUID, authToken)
}
