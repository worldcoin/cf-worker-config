package main

import (
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// Load configuration from environment variables with defaults
func init() {
	port = getEnv("PORT", ":8080")
	healthPort = getEnv("HEALTH_PORT", ":8081")
	tickerTime = getEnvAsInt("TICKER_TIME", 30)
	timeout = getEnvAsInt("TIMEOUT", 10)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
		log.Warn().Str("key", key).Str("value", value).Msg("Invalid integer value for environment variable, using default")
	}
	return defaultValue
}
