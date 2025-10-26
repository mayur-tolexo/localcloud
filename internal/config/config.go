package config

import "os"

var (
	DataDir   string
	AIService string
	QdrantURL string
	GerberaIP string
	BindPort  string
)

func LoadConfig() {
	DataDir = getenv("DATA_DIR", "/data")
	AIService = getenv("AI_SERVICE_URL", "http://ai-service:5000")
	QdrantURL = getenv("QDRANT_URL", "http://qdrant:6333")
	// IP or hostname of the Pi for DLNA URL creation (set PI_IP or defaults to localhost)
	GerberaIP = getenv("PI_IP", "localhost")
	BindPort = getenv("BIND_PORT", "8080")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
