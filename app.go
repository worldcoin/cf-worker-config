package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

func businessLogic() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Run some code here
	log.Info().Msg(fmt.Sprintf("Business logic ctx: %v", ctx))

	// Update metrics
	myGauge.Set(float64(time.Now().Unix() % 100))
	myCounter.Inc()

	log.Info().Msg("Business logic executed and metrics updated")
}
