package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Nanit   NanitConfig   `yaml:"nanit"`
	HTTP    HTTPConfig    `yaml:"http"`
	MQTT    MQTTConfig    `yaml:"mqtt"`
	HLS     HLSConfig     `yaml:"hls"`
	Session SessionConfig `yaml:"session"`
	Log     LogConfig     `yaml:"log"`
}

// NanitConfig holds Nanit cloud API configuration.
type NanitConfig struct {
	APIBase   string `yaml:"api_base"`
	BabyUID   string `yaml:"baby_uid"`
	CameraUID string `yaml:"camera_uid"`
	CameraIP  string `yaml:"camera_ip"`
}

// MQTTConfig holds MQTT broker configuration.
type MQTTConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Broker      string `yaml:"broker"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	TopicPrefix string `yaml:"topic_prefix"`
	DeviceID    string `yaml:"device_id"`
	BabyName    string `yaml:"baby_name"`
	CameraModel string `yaml:"camera_model"`
}

// HLSConfig holds HLS proxy configuration.
type HLSConfig struct {
	Enabled      bool   `yaml:"enabled"`
	OutputDir    string `yaml:"output_dir"`
	SegmentTime  int    `yaml:"segment_time"`
	PlaylistSize int    `yaml:"playlist_size"`
	FFmpegPath   string `yaml:"ffmpeg_path"`
}

// HTTPConfig holds HTTP server configuration.
type HTTPConfig struct {
	Addr    string `yaml:"addr"`
	UIDir   string `yaml:"ui_dir"`
	CORSAll bool   `yaml:"cors_allow_all"`
}

// SessionConfig holds session file path configuration.
type SessionConfig struct {
	Path string `yaml:"path"`
}

// LogConfig holds logging configuration.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		Nanit: NanitConfig{
			APIBase: "https://api.nanit.com",
		},
		HTTP: HTTPConfig{
			Addr: ":8080",
		},
		MQTT: MQTTConfig{
			TopicPrefix: "nanit",
			DeviceID:    "nanit_cam_01",
			CameraModel: "Pro Camera",
		},
		HLS: HLSConfig{
			SegmentTime:  2,
			PlaylistSize: 5,
			FFmpegPath:   "ffmpeg",
		},
		Session: SessionConfig{
			Path: "/data/session.json",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load reads configuration from a YAML file at path, then overlays environment variables.
// If path is empty, only defaults + env vars are used.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return cfg, fmt.Errorf("config: read %s: %w", path, err)
			}
			// file not found is ok, use defaults
		} else {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("config: parse %s: %w", path, err)
			}
		}
	}

	applyEnv(&cfg)
	return cfg, nil
}

// applyEnv overlays environment variables on top of the config.
// Env vars take precedence over YAML values.
func applyEnv(cfg *Config) {
	if v := os.Getenv("NANIT_API_BASE"); v != "" {
		cfg.Nanit.APIBase = v
	}
	if v := os.Getenv("NANIT_BABY_UID"); v != "" {
		cfg.Nanit.BabyUID = v
	}
	if v := os.Getenv("NANIT_CAMERA_UID"); v != "" {
		cfg.Nanit.CameraUID = v
	}
	if v := os.Getenv("NANIT_CAMERA_IP"); v != "" {
		cfg.Nanit.CameraIP = v
	}
	if v := os.Getenv("NANIT_HTTP_ADDR"); v != "" {
		cfg.HTTP.Addr = v
	}
	if v := os.Getenv("NANIT_UI_DIR"); v != "" {
		cfg.HTTP.UIDir = v
	}
	if v := os.Getenv("NANIT_CORS_ALLOW_ALL"); v != "" {
		cfg.HTTP.CORSAll = parseBool(v)
	}
	if v := os.Getenv("NANIT_MQTT_ENABLED"); v != "" {
		cfg.MQTT.Enabled = parseBool(v)
	}
	if v := os.Getenv("NANIT_MQTT_BROKER"); v != "" {
		cfg.MQTT.Broker = v
	}
	if v := os.Getenv("NANIT_MQTT_USERNAME"); v != "" {
		cfg.MQTT.Username = v
	}
	if v := os.Getenv("NANIT_MQTT_PASSWORD"); v != "" {
		cfg.MQTT.Password = v
	}
	if v := os.Getenv("NANIT_MQTT_TOPIC_PREFIX"); v != "" {
		cfg.MQTT.TopicPrefix = v
	}
	if v := os.Getenv("NANIT_MQTT_DEVICE_ID"); v != "" {
		cfg.MQTT.DeviceID = v
	}
	if v := os.Getenv("NANIT_HLS_ENABLED"); v != "" {
		cfg.HLS.Enabled = parseBool(v)
	}
	if v := os.Getenv("NANIT_HLS_OUTPUT_DIR"); v != "" {
		cfg.HLS.OutputDir = v
	}
	if v := os.Getenv("NANIT_SESSION_PATH"); v != "" {
		cfg.Session.Path = v
	}
	if v := os.Getenv("NANIT_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("NANIT_LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	b, _ := strconv.ParseBool(s)
	return b
}
