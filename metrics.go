package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	myGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "myapp_my_gauge",
		Help: "Gauge example metric",
	})
	myCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "myapp_my_counter",
		Help: "Counter example metric",
	})
)
