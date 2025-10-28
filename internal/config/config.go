package config

import "os"

var (
	DataDir  string
	BindPort string
)

func LoadConfig() {
	DataDir = getenv("DATA_DIR", "./data")
	BindPort = getenv("PORT", getenv("BIND_PORT", "8080"))
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
